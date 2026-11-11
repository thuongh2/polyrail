package gateway

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"

	"github.com/polyrail/polyrail/internal/store"
)

func TestLogCostCachePricing(t *testing.T) {
	plain := store.Model{InputPrice: 3_000_000, OutputPrice: 15_000_000} // $3 / $15 per 1M
	cached, readOnly, writeOnly := plain, plain, plain
	cached.CacheReadPrice, cached.CacheWritePrice = 300_000, 3_750_000 // $0.30 / $3.75
	readOnly.CacheReadPrice = 300_000
	writeOnly.CacheWritePrice = 3_750_000
	l := func(in, out, r, w, reas int) store.RequestLog {
		return store.RequestLog{InTokens: in, OutTokens: out, CacheReadTokens: r, CacheWriteTokens: w, ReasoningTokens: reas}
	}
	for _, c := range []struct {
		name string
		m    store.Model
		l    store.RequestLog
		want int64
	}{
		{"no cache price: cached tokens at input price (unchanged)", plain, l(1000, 100, 800, 100, 0), 4500},
		{"no cache tokens", cached, l(1000, 100, 0, 0, 0), 4500},
		{"read and write", cached, l(1000, 100, 800, 100, 0), 300 + 240 + 375 + 1500},
		{"read price only: writes at input", readOnly, l(1000, 100, 800, 100, 0), 300 + 240 + 300 + 1500},
		{"write price only: reads at input", writeOnly, l(1000, 100, 800, 100, 0), 300 + 2400 + 375 + 1500},
		{"reasoning is part of out, not added", cached, l(0, 100, 0, 0, 60), 1500},
		{"more cached than prompt clamps uncached at 0", cached, l(10, 0, 20, 0, 0), 6},
		{"one rounding of the sum", cached, l(1, 0, 1, 1, 0), 4}, // 0.3+3.75 = 4.05 -> 4
		{"rounds half up", readOnly, l(5, 0, 5, 0, 0), 2},        // 1.5 -> 2
	} {
		if got := logCost(c.m, c.l); got != c.want {
			t.Errorf("%s: logCost = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTokensFromBifrostUsage(t *testing.T) {
	cu := &schemas.BifrostLLMUsage{PromptTokens: 100, CompletionTokens: 50,
		PromptTokensDetails:     &schemas.ChatPromptTokensDetails{CachedReadTokens: 60, CachedWriteTokens: 20},
		CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{ReasoningTokens: 30}}
	var l store.RequestLog
	chatTokens(cu).into(&l)
	if l.InTokens != 100 || l.OutTokens != 50 || l.CacheReadTokens != 60 || l.CacheWriteTokens != 20 || l.ReasoningTokens != 30 {
		t.Fatalf("chat: %+v", l)
	}
	ru := &schemas.ResponsesResponseUsage{InputTokens: 9, OutputTokens: 4,
		InputTokensDetails:  &schemas.ResponsesResponseInputTokens{CachedReadTokens: 5, CachedWriteTokens: 1},
		OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 2}}
	l = store.RequestLog{}
	respTokens(ru).into(&l)
	if l.InTokens != 9 || l.OutTokens != 4 || l.CacheReadTokens != 5 || l.CacheWriteTokens != 1 || l.ReasoningTokens != 2 {
		t.Fatalf("responses: %+v", l)
	}
	chatTokens(nil).into(&l) // nil usage leaves the log alone
	if l.InTokens != 9 {
		t.Fatal("nil usage changed the log")
	}
}

func TestInfoCachePricing(t *testing.T) {
	m := store.Model{ID: "a/b", InputPrice: 3_000_000, OutputPrice: 15_000_000, CacheReadPrice: 300_000, CacheWritePrice: 3_750_000}
	if p := Info(m).Pricing; p.InputCacheRead != "0.3" || p.InputCacheWrite != "3.75" {
		t.Fatalf("pricing %+v", p)
	}
	m.CacheReadPrice, m.CacheWritePrice = 0, 0
	raw, _ := json.Marshal(Info(m).Pricing)
	if strings.Contains(string(raw), "cache") {
		t.Fatalf("unset cache prices must be omitted: %s", raw)
	}
}

func TestBatchUsageCacheTokens(t *testing.T) {
	item := func(u map[string]any) schemas.BatchResultItem {
		return schemas.BatchResultItem{Response: &schemas.BatchResultResponse{StatusCode: 200, Body: map[string]any{"usage": u}}}
	}
	l := batchUsage([]schemas.BatchResultItem{
		item(map[string]any{"prompt_tokens": 100.0, "completion_tokens": 10.0, "prompt_tokens_details": map[string]any{"cached_tokens": 64.0}}),
		item(map[string]any{"input_tokens": 5.0, "output_tokens": 7.0, "cache_read_input_tokens": 20.0, "cache_creation_input_tokens": 30.0}),
	})
	if l.InTokens != 155 || l.OutTokens != 17 || l.CacheReadTokens != 84 || l.CacheWriteTokens != 30 {
		t.Fatalf("batch usage %+v", l)
	}
}

// Every token endpoint bills the 5 cached prompt tokens the upstream reports at the cache read price:
// 999,995 × $1/M + 5 × $0.10/M + 1M out × $2/M = 2,999,995.5 → 2,999,996 micros (3,000,000 without).
func TestCachePricingOnEveryEndpoint(t *testing.T) {
	f := setupRoute(t)
	ctx := context.Background()
	m := &store.Model{ID: "r/cache", Enabled: true, InputPrice: 1_000_000, OutputPrice: 2_000_000, CacheReadPrice: 100_000,
		Targets: []store.Target{{ProviderID: f.alphaP.ID, Model: "up"}}}
	if err := f.g.S.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"/v1/chat/completions": `{"model":"r/cache","messages":[{"role":"user","content":"hi"}]}`,
		"/v1/completions":      `{"model":"r/cache","prompt":"hi"}`,
		"/v1/responses":        `{"model":"r/cache","input":"hi"}`,
	} {
		r := f.post(t, path, f.key, body)
		r.Body.Close()
		if got := r.Header.Get("X-GW-Cost-Micros"); r.StatusCode != 200 || got != "2999996" {
			t.Errorf("%s: status %d cost %q, want 2999996", path, r.StatusCode, got)
		}
	}
	// stream: the usage chunk's cost
	r := f.post(t, "/v1/chat/completions", f.key, `{"model":"r/cache","stream":true,"usage":{"include":true},"messages":[{"role":"user","content":"hi"}]}`)
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(raw), `"cost_micros":2999996`) || !strings.Contains(string(raw), `"cached_tokens":5`) {
		t.Fatalf("stream usage: %s", raw)
	}
}
