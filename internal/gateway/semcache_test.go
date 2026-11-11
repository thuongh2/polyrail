package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// semUpstream embeds "cat" as [1,0], "kitten" as [0.95,0.312] (cosine 0.95 to cat), anything
// else as [0,1]; chat answers carry a call counter so cached answers are recognisable.
func semUpstream(t *testing.T, chats, embeds *atomic.Int64, embedFail bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/embeddings" {
			embeds.Add(1)
			if embedFail {
				http.Error(w, `{"error":{"message":"down"}}`, 500)
				return
			}
			in := fmt.Sprint(req["input"])
			vec := "[0,1]"
			switch {
			case strings.Contains(in, "kitten"):
				vec = "[0.95,0.312]"
			case strings.Contains(in, "cat"):
				vec = "[1,0]"
			}
			fmt.Fprintf(w, `{"object":"list","model":"e","data":[{"object":"embedding","index":0,"embedding":%s}],"usage":{"prompt_tokens":5,"total_tokens":5}}`, vec)
			return
		}
		n := chats.Add(1)
		fmt.Fprintf(w, `{"id":"c%d","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"answer %d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, n, n)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type semFixture struct {
	*fixture
	chats, embeds *atomic.Int64
}

func setupSem(t *testing.T, embedFail bool, sc *store.SemanticCache) *semFixture {
	f := &semFixture{fixture: setup(t), chats: &atomic.Int64{}, embeds: &atomic.Int64{}}
	ctx := context.Background()
	up := semUpstream(t, f.chats, f.embeds, embedFail)
	p := &store.Provider{Name: "sem", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.Model{
		{ID: "sem/chat", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "up"}}},
		{ID: "sem/emb", Kind: "embedding", Enabled: true, InputPrice: 1_000_000, Targets: []store.Target{{ProviderID: p.ID, Model: "e"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error { o.SemanticCache = sc; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	// the key may only use sem/chat: the org-level embedding model still applies
	if _, err := f.g.S.UpdateKey(ctx, f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.Models = []string{"sem/chat"}; return nil }); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	return f
}

func (f *semFixture) ask(t *testing.T, text, extra string) (string, *http.Response) {
	t.Helper()
	body := fmt.Sprintf(`{"model":"sem/chat","messages":[{"role":"system","content":"be brief"},{"role":"user","content":%q}]%s}`, text, extra)
	resp := f.post(t, "/v1/chat/completions", f.key, body)
	m := readJSON(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %v", resp.StatusCode, m)
	}
	c, _ := m["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	return c, resp
}

const temp0 = `,"temperature":0`

func TestSemanticCacheHitMissAndGating(t *testing.T) {
	f := setupSem(t, false, &store.SemanticCache{Model: "sem/emb"})
	a, r := f.ask(t, "tell me about cat", temp0)
	if a != "answer 1" || r.Header.Get("X-GW-Cache") != "" {
		t.Fatalf("first request must miss: %q %v", a, r.Header)
	}
	a, r = f.ask(t, "tell me about cat", temp0)
	if a != "answer 1" || r.Header.Get("X-GW-Cache") != "semantic" || r.Header.Get("X-GW-Cache-Score") != "1.0000" || r.Header.Get("X-GW-Cost-Micros") != "0" {
		t.Fatalf("identical prompt must hit: %q %v", a, r.Header)
	}
	a, r = f.ask(t, "tell me about kitten", temp0)
	if a != "answer 1" || r.Header.Get("X-GW-Cache-Score") != "0.9501" {
		t.Fatalf("similar prompt must hit at 0.95: %q %v", a, r.Header)
	}
	if a, _ = f.ask(t, "tell me about car", temp0); a != "answer 2" {
		t.Fatalf("dissimilar prompt must miss: %q", a)
	}
	if f.embeds.Load() != 4 || f.chats.Load() != 2 {
		t.Fatalf("embeds=%d chats=%d", f.embeds.Load(), f.chats.Load())
	}
	// not eligible: no explicit temperature, temperature > 0, tools, json_schema
	for _, extra := range []string{"", `,"temperature":0.7`,
		temp0 + `,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]`,
		temp0 + `,"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{"type":"object"}}}`} {
		before := f.chats.Load()
		if _, r := f.ask(t, "tell me about cat", extra); r.Header.Get("X-GW-Cache") != "" || f.chats.Load() != before+1 {
			t.Fatalf("extra %s must bypass the semantic cache", extra)
		}
	}
	if f.embeds.Load() != 4 {
		t.Fatalf("ineligible requests must not embed: %d", f.embeds.Load())
	}
	// the hit is logged as a cache hit and the embedding calls are billed
	deadline := time.Now().Add(3 * time.Second)
	for {
		logs, _ := f.g.S.ListLogs(context.Background(), f.org.ID, 1, 100)
		var cached, emb int
		for _, l := range logs {
			if l.Cached && l.Model == "sem/chat" {
				cached++
			}
			if l.Model == "sem/emb" && l.Cost > 0 {
				emb++
			}
		}
		if cached == 2 && emb == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs: cached=%d billed embeds=%d", cached, emb)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSemanticCacheThresholdAndFailOpen(t *testing.T) {
	f := setupSem(t, false, &store.SemanticCache{Model: "sem/emb", Threshold: 0.96})
	f.ask(t, "cat", temp0)
	if a, r := f.ask(t, "kitten", temp0); a != "answer 2" || r.Header.Get("X-GW-Cache") != "" {
		t.Fatalf("0.95 < threshold 0.96 must miss: %q", a)
	}
	broken := setupSem(t, true, &store.SemanticCache{Model: "sem/emb"})
	broken.ask(t, "cat", temp0)
	if a, _ := broken.ask(t, "cat", temp0); a != "answer 2" || broken.embeds.Load() == 0 {
		t.Fatalf("embedding errors must fail open to the provider: %q", a)
	}
}

func TestSemCacheLRUAndTTL(t *testing.T) {
	c := newSemCache(1 << 20)
	now := time.Now()
	for i := range semMaxPerOrg + 1 {
		c.add("o", &semEntry{model: "m", emb: "e", vec: []float32{1, 0}, resp: []byte(fmt.Sprint(i)),
			exp: now.Add(time.Hour), used: now.Add(time.Duration(i) * time.Millisecond)})
	}
	if n := len(c.orgs["o"].entries); n != semMaxPerOrg {
		t.Fatalf("entries = %d", n)
	}
	if c.orgs["o"].entries[0].resp[0] == '0' && len(c.orgs["o"].entries[0].resp) == 1 {
		t.Fatal("least recently used entry not evicted")
	}
	if _, _, ok := c.lookup("o", "m", "e", []float32{1, 0}, 0.9, now.Add(2*time.Hour)); ok || len(c.orgs["o"].entries) != 0 || c.bytes.Load() != 0 {
		t.Fatal("expired entries must be dropped and their bytes released")
	}
	small := newSemCache(300)
	small.add("o", &semEntry{resp: make([]byte, 1000), exp: now.Add(time.Hour)})
	if len(small.orgs["o"].entries) != 0 {
		t.Fatal("entry larger than the byte budget stored")
	}
}

type hookRec struct {
	mu  sync.Mutex
	evs []string
}

func (h *hookRec) Emit(org, typ string, data map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.evs = append(h.evs, fmt.Sprint(typ, " ", data["model"], " ", data["code"]))
}

func TestWebhookEventsFromGateway(t *testing.T) {
	f := setup(t)
	rec := &hookRec{}
	f.g.Hooks = rec
	ctx := context.Background()
	var bad string
	for id, p := range f.g.Snapshot().Providers {
		if p.Name == "bad" {
			bad = id
		}
	}
	if err := f.g.S.PutModel(ctx, &store.Model{ID: "test/bad", Enabled: true, Targets: []store.Target{{ProviderID: bad, Model: "up"}}}); err != nil {
		t.Fatal(err)
	}
	_ = f.g.Reload(ctx)
	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/bad","messages":[{"role":"user","content":"secret prompt"}]}`)
	resp.Body.Close()
	if _, err := f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error { o.Prepaid = true; return nil }); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	resp = f.post(t, "/v1/chat/completions", f.key, chatBody)
	resp.Body.Close()
	if resp.StatusCode != 402 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	got := strings.Join(rec.evs, "|")
	if got != "request.failed test/bad provider_error|budget.exceeded <nil> insufficient_credits" {
		t.Fatalf("events: %s", got)
	}
}
