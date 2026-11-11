package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/limits"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

// mockUpstream is a minimal OpenAI-compatible provider. fail=true answers 500.
func mockUpstream(t *testing.T, fail bool, hits *atomic.Int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail {
			http.Error(w, `{"error":{"message":"boom"}}`, 500)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path == "/v1/embeddings" {
			fmt.Fprint(w, `{"object":"list","model":"emb","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
			return
		}
		msgs := req["messages"].([]any)
		last := msgs[len(msgs)-1].(map[string]any)["content"]
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, part := range []string{"hel", "lo"} {
				fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
			}
			fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"echo: %v | max=%v"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000000,"completion_tokens":500000,"total_tokens":1500000}}`,
			last, req["max_completion_tokens"])
	}))
	t.Cleanup(srv.Close)
	return srv
}

type fixture struct {
	g       *Gateway
	srv     *httptest.Server
	org     *store.Org
	key     string
	keyRec  *store.APIKey
	badHits *atomic.Int64
	okHits  *atomic.Int64
}

func setup(t *testing.T) *fixture { t.Helper(); return setupWith(t, nil) }

// setupWith is setup with a hook that adjusts the gateway Config before New.
func setupWith(t *testing.T, adjust func(*Config)) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := store.NewBox("test-master-key-123456")
	s := store.New(kv, box)
	f := &fixture{badHits: &atomic.Int64{}, okHits: &atomic.Int64{}}
	bad := mockUpstream(t, true, f.badHits)
	ok := mockUpstream(t, false, f.okHits)

	pBad := &store.Provider{Name: "bad", Type: "openai", BaseURL: bad.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k1"}}}
	pOK := &store.Provider{Name: "ok", Type: "openai", BaseURL: ok.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k2"}}}
	for _, p := range []*store.Provider{pBad, pOK} {
		if err := s.PutProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	models := []*store.Model{
		{ID: "test/chat", Name: "Chat", Enabled: true, InputPrice: 1_000_000, OutputPrice: 2_000_000,
			Targets: []store.Target{{ProviderID: pBad.ID, Model: "up", Weight: 100}, {ProviderID: pOK.ID, Model: "up", Weight: 0.0001}}},
		{ID: "test/ok", Name: "OK", Enabled: true, Targets: []store.Target{{ProviderID: pOK.ID, Model: "up"}}},
		{ID: "test/emb", Name: "Emb", Kind: "embedding", Enabled: true, Targets: []store.Target{{ProviderID: pOK.ID, Model: "emb"}}},
	}
	for _, m := range models {
		if err := s.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	f.org = &store.Org{Name: "acme"}
	if err := s.CreateOrg(ctx, f.org, ""); err != nil {
		t.Fatal(err)
	}
	f.key = KeyPrefix + "testsecret"
	f.keyRec = &store.APIKey{OrgID: f.org.ID, Name: "k"}
	if err := s.CreateKey(ctx, f.keyRec, HashKey(f.key)); err != nil {
		t.Fatal(err)
	}
	up := usage.New(kv, 100, 20*time.Millisecond, usage.StoreSink{S: s})
	t.Cleanup(up.Close)
	cfg := Config{MaxBodyBytes: 1 << 20, Concurrency: 4, Buffer: 16, CacheMB: 4, PollInterval: 50 * time.Millisecond}
	if adjust != nil {
		adjust(&cfg)
	}
	f.g, err = New(ctx, s, limits.NewLocal(), up, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.g.BF.Shutdown)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", f.g.ChatCompletions)
	mux.HandleFunc("POST /v1/embeddings", f.g.Embeddings)
	mux.HandleFunc("GET /v1/models", f.g.Models)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) post(t *testing.T, path, key, body string, hdr ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readJSON(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	defer r.Body.Close()
	var m map[string]any
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not json (%d): %s", r.StatusCode, raw)
	}
	return m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

const chatBody = `{"model":"test/chat","messages":[{"role":"user","content":"hi"}]}`

func TestChatFailoverPricingAndBreaker(t *testing.T) {
	f := setup(t)
	t0 := time.Now()
	resp := f.post(t, "/v1/chat/completions", f.key, chatBody)
	m := readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, m)
	}
	if m["model"] != "test/chat" || m["extra_fields"] != nil {
		t.Fatalf("response shape: %v", m)
	}
	choice := m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if !strings.HasPrefix(choice["content"].(string), "echo: hi") {
		t.Fatalf("content: %v", choice)
	}
	if f.badHits.Load() == 0 || f.okHits.Load() != 1 {
		t.Fatalf("failover: bad=%d ok=%d", f.badHits.Load(), f.okHits.Load())
	}
	if got := resp.Header.Get("x-gateway-provider"); got != "ok" || m["provider"] != "ok" {
		t.Fatalf("serving provider: header %q body %v", got, m["provider"])
	}
	if got := resp.Header.Get("X-GW-Route"); got != "weighted; ok/up" {
		t.Fatalf("X-GW-Route = %q", got)
	}
	// after 3 failures the bad target's circuit opens and it is no longer tried first
	f.post(t, "/v1/chat/completions", f.key, chatBody).Body.Close()
	f.post(t, "/v1/chat/completions", f.key, chatBody).Body.Close()
	before := f.badHits.Load()
	f.post(t, "/v1/chat/completions", f.key, chatBody).Body.Close()
	if f.badHits.Load() != before {
		t.Fatalf("circuit did not open: bad hits %d -> %d", before, f.badHits.Load())
	}
	// cost: 1M in at $1/M + 0.5M out at $2/M = $2 = 2_000_000 micros per request
	f.g.Usage.Close() // flush
	a, _ := f.g.S.KV.GetAgg(context.Background(), usage.OrgSpendKey(f.org.ID))
	if a[store.AggCostMicros] != 4*2_000_000 || a[store.AggRequests] != 4 {
		t.Fatalf("spend: %v", a)
	}
	// provider health: the bad target failed 3 times (then skipped), the good one served all 4
	ids := map[string]string{}
	for id, p := range f.g.Snapshot().Providers {
		ids[p.Name] = id
	}
	if !f.g.TargetOpen(ids["bad"], "up") || f.g.TargetOpen(ids["ok"], "up") {
		t.Fatal("TargetOpen does not reflect the breaker")
	}
	health := func(prov string) store.Agg { // the test may straddle an hour boundary
		a, _ := f.g.S.KV.GetAgg(context.Background(), usage.HealthKey("test/chat", prov, t0))
		if now := time.Now(); usage.HealthHour(now) != usage.HealthHour(t0) {
			b, _ := f.g.S.KV.GetAgg(context.Background(), usage.HealthKey("test/chat", prov, now))
			a.Add(b)
		}
		return a
	}
	bad, good := health(ids["bad"]), health(ids["ok"])
	if bad[store.AggRequests] != 3 || bad[store.AggErrors] != 3 || bad[store.AggLatencyMs] != 0 {
		t.Fatalf("bad target health: %v", bad)
	}
	if good[store.AggRequests] != 4 || good[store.AggErrors] != 0 || good[store.AggOutTokens] != 4*500_000 || good[store.AggLatencyMs] <= 0 {
		t.Fatalf("good target health: %v", good)
	}
}

func TestStreaming(t *testing.T) {
	f := setup(t)
	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if got := resp.Header.Get("X-GW-Route"); got != "weighted; ok/up" {
		t.Fatalf("stream X-GW-Route = %q", got)
	}
	var text strings.Builder
	var done bool
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		if line == "[DONE]" {
			done = true
			continue
		}
		if line == "" {
			continue
		}
		var c struct {
			Model   string
			Choices []struct{ Delta struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("bad chunk %q", line)
		}
		if c.Model != "test/ok" {
			t.Fatalf("chunk model %q", c.Model)
		}
		for _, ch := range c.Choices {
			text.WriteString(ch.Delta.Content)
		}
	}
	if text.String() != "hello" || !done {
		t.Fatalf("stream text %q done=%v", text.String(), done)
	}
}

func TestEmbeddingsAndModels(t *testing.T) {
	f := setup(t)
	resp := f.post(t, "/v1/embeddings", f.key, `{"model":"test/emb","input":"hi"}`)
	m := readJSON(t, resp)
	if resp.StatusCode != 200 || len(m["data"].([]any)) != 1 {
		t.Fatalf("embeddings %d: %v", resp.StatusCode, m)
	}
	// chat on an embedding model is rejected
	if r := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/emb","messages":[{"role":"user","content":"x"}]}`); errCode(readJSON(t, r)) != "wrong_model_kind" {
		t.Fatal("kind check")
	}
	req, _ := http.NewRequest("GET", f.srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	r, _ := http.DefaultClient.Do(req)
	if ms := readJSON(t, r)["data"].([]any); len(ms) != 3 {
		t.Fatalf("models: %v", ms)
	}
}

func TestPolicies(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		key    string
		body   string
		status int
		code   string
	}{
		{"no key", "", chatBody, 401, "invalid_api_key"},
		{"wrong key", KeyPrefix + "nope", chatBody, 401, "invalid_api_key"},
		{"bad json", f.key, `{`, 400, "bad_request"},
		{"unknown model", f.key, `{"model":"x/y","messages":[{"role":"user","content":"hi"}]}`, 404, "model_not_found"},
	}
	for _, c := range cases {
		r := f.post(t, "/v1/chat/completions", c.key, c.body)
		if got := errCode(readJSON(t, r)); r.StatusCode != c.status || got != c.code {
			t.Errorf("%s: %d %s", c.name, r.StatusCode, got)
		}
	}

	// guardrails: blocked pattern, PII masking, max_tokens cap
	_, err := f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error {
		o.Guardrails = store.Guardrails{BlockedPatterns: []string{`(?i)secret plan`}, MaskPII: true}
		o.MaxTokensCap = 50
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	r := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"the SECRET PLAN"}]}`)
	if code := errCode(readJSON(t, r)); r.StatusCode != 400 || code != "guardrail_blocked" {
		t.Fatalf("guardrail: %d %s", r.StatusCode, code)
	}
	r = f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","max_tokens":4000,"messages":[{"role":"user","content":"mail bob@example.com"}]}`)
	content := readJSON(t, r)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if content != "echo: mail [email] | max=50" {
		t.Fatalf("pii mask / cap: %q", content)
	}

	// response cache
	hdr := []string{"x-gateway-cache", "60"}
	f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"cache me"}]}`, hdr...).Body.Close()
	hits := f.okHits.Load()
	r = f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"cache me"}]}`, hdr...)
	r.Body.Close()
	if r.Header.Get("x-gateway-cache-hit") != "true" || f.okHits.Load() != hits {
		t.Fatal("cache miss on identical request")
	}

	// rate limit
	if _, err := f.g.S.UpdateKey(ctx, f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.RPM = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	_ = f.g.Reload(ctx)
	f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"a"}]}`).Body.Close()
	r = f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"b"}]}`)
	if code := errCode(readJSON(t, r)); r.StatusCode != 429 || code != "rpm_exceeded" {
		t.Fatalf("rpm: %d %s", r.StatusCode, code)
	}

	// key monthly budget: one priced request spends $2, budget is $1
	_, _ = f.g.S.UpdateKey(ctx, f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.RPM, k.MonthlyBudget = 0, 1_000_000; return nil })
	_ = f.g.Reload(ctx)
	f.post(t, "/v1/chat/completions", f.key, `{"model":"test/chat","messages":[{"role":"user","content":"spend"}]}`).Body.Close()
	r = f.post(t, "/v1/chat/completions", f.key, chatBody)
	if code := errCode(readJSON(t, r)); r.StatusCode != 402 || code != "key_budget_exceeded" {
		t.Fatalf("key budget: %d %s", r.StatusCode, code)
	}
	_, _ = f.g.S.UpdateKey(ctx, f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.MonthlyBudget = 0; return nil })

	// prepaid org without credits
	_, _ = f.g.S.UpdateKey(ctx, f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.RPM = 0; return nil })
	_, _ = f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error { o.Prepaid = true; return nil })
	_ = f.g.Reload(ctx)
	r = f.post(t, "/v1/chat/completions", f.key, chatBody)
	if code := errCode(readJSON(t, r)); r.StatusCode != 402 || code != "insufficient_credits" {
		t.Fatalf("credits: %d %s", r.StatusCode, code)
	}

	// revoked key
	_, _ = f.g.S.UpdateKey(ctx, f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.Revoked = true; return nil })
	_ = f.g.Reload(ctx)
	if r := f.post(t, "/v1/chat/completions", f.key, chatBody); r.StatusCode != 401 {
		t.Fatalf("revoked key: %d", r.StatusCode)
	}
}
