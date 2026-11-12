package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// upstreamRecorder is a vendor stand-in that answers 404 and records "PATH auth" of each request.
func upstreamRecorder(t *testing.T) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, `{"error":{"message":"nope"}}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := seen
		seen = nil
		return out
	}
}

// The contract with Bifrost's adapters: whichever way a base URL is written, the real adapter of
// each provider type must call the vendor's chat path, once, not /v1/v1/... or an unversioned
// path. It drives Bifrost itself, so a core upgrade that moves a path fails here.
func TestUpstreamBaseContract(t *testing.T) {
	v1 := []string{"", "/", "/v1", "/v1/"}
	cases := map[string]struct {
		forms []string
		chat  string
	}{
		"anthropic": {v1, "/v1/messages"},
		"gemini":    {[]string{"", "/v1beta", "/v1beta/"}, "/v1beta/models/m1:generateContent"},
		"cohere":    {[]string{"", "/v1", "/v2"}, "/v2/chat"},
		"replicate": {v1, "/v1/models/m1/predictions"},
		"byteplus":  {[]string{"", "/api/v3", "/api/v3/"}, "/api/v3/chat/completions"},
		// their APIs serve chat at the root as well as under /v1
		"perplexity": {v1, "/chat/completions"},
		"deepseek":   {v1, "/chat/completions"},
	}
	for _, typ := range []string{"openai", "mistral", "groq", "ollama", "cerebras", "openrouter", "nebius", "parasail", "sgl",
		"xai", "fireworks", "vllm", "minimax", "sarvam", "opencode-go", "opencode-zen", "wafer", "runware"} {
		cases[typ] = struct {
			forms []string
			chat  string
		}{v1, "/v1/chat/completions"}
	}
	// no chat request to observe here; TestUpstreamBase covers their rule
	noChat := []string{"elevenlabs", "runway", "typesafe", "huggingface"}
	for typ := range addsV1 {
		if _, ok := cases[typ]; !ok && !slices.Contains(noChat, typ) {
			t.Errorf("%s has a base URL rule but no contract case", typ)
		}
	}
	for typ := range wantsVersion {
		if _, ok := cases[typ]; !ok {
			t.Errorf("%s has a base URL rule but no contract case", typ)
		}
	}

	srv, seen := upstreamRecorder(t)
	f := setup(t)
	ctx := context.Background()
	for typ, c := range cases {
		p := &store.Provider{Name: "contract-" + typ, Type: typ, Enabled: true}
		for _, form := range c.forms {
			p.BaseURL = srv.URL + form
			p.Keys = []store.ProviderKey{{Value: "contract-key"}}
			if err := f.g.S.PutProvider(ctx, p); err != nil {
				t.Fatal(typ, err)
			}
			if err := f.g.Reload(ctx); err != nil {
				t.Fatal(err)
			}
			seen()
			if _, err := f.g.TestKey(ctx, p.ID, p.Keys[0].ID, "m1"); err != nil {
				t.Fatal(typ, err)
			}
			got := seen()
			if path, _, _ := strings.Cut(strings.Join(got, ","), " "); len(got) != 1 || path != c.chat {
				t.Errorf("%s with base %q: upstream saw %v, want path %s", typ, "<host>"+form, got, c.chat)
			}
		}
	}
}

// Bifrost builds a provider once and keeps it; the gateway must rebuild it whenever the stored
// provider would configure it differently, not only when a loaded provider's URL is edited.
func TestBifrostProviderFollowsStore(t *testing.T) {
	a, seenA := upstreamRecorder(t)
	b, seenB := upstreamRecorder(t)
	f := setup(t)
	ctx := context.Background()
	save := func(p *store.Provider, key string) {
		t.Helper()
		p.Keys = nil
		if key != "" {
			p.Keys = []store.ProviderKey{{Value: key}}
		}
		if err := f.g.S.PutProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := f.g.S.PutModel(ctx, &store.Model{ID: "sync/" + p.Name, Name: p.Name, Enabled: true,
			Targets: []store.Target{{ProviderID: p.ID, Model: "m"}}}); err != nil {
			t.Fatal(err)
		}
		if err := f.g.Reload(ctx); err != nil {
			t.Fatal(err)
		}
	}
	chat := func(p *store.Provider) {
		t.Helper()
		f.post(t, "/v1/chat/completions", f.key, `{"model":"sync/`+p.Name+`","messages":[{"role":"user","content":"hi"}]}`).Body.Close()
	}

	// a keyless provider that served a request gets its first key: the key must be sent
	kl := &store.Provider{Name: "keyless", Type: "openai", BaseURL: a.URL, Enabled: true}
	save(kl, "")
	chat(kl)
	if got := seenA(); len(got) != 1 || got[0] != "/v1/chat/completions " {
		t.Fatalf("keyless request: %v", got)
	}
	save(kl, "first-key")
	chat(kl)
	if got := seenA(); len(got) != 1 || got[0] != "/v1/chat/completions Bearer first-key" {
		t.Fatalf("after adding a key the upstream saw %v", got)
	}

	// a provider moved to another host while disabled must not come back on the old one
	gq := &store.Provider{Name: "moved", Type: "groq", BaseURL: a.URL, Enabled: true}
	save(gq, "k")
	chat(gq)
	gq.Enabled = false
	save(gq, "k")
	gq.Enabled, gq.BaseURL = true, b.URL
	save(gq, "k")
	seenA()
	chat(gq)
	if onA, onB := seenA(), seenB(); len(onA) != 0 || len(onB) != 1 {
		t.Fatalf("re-enabled at a new URL: old host saw %v, new host saw %v", onA, onB)
	}

	// a new provider taking a deleted one's type slot ("groq") gets its own config
	if err := f.g.S.DeleteProvider(ctx, gq.ID); err != nil {
		t.Fatal(err)
	}
	next := &store.Provider{Name: "successor", Type: "groq", BaseURL: a.URL, Enabled: true}
	save(next, "k2")
	chat(next)
	if onA, onB := seenA(), seenB(); len(onA) != 1 || onA[0] != "/v1/chat/completions Bearer k2" || len(onB) != 0 {
		t.Fatalf("successor provider: its host saw %v, the deleted one's host saw %v", onA, onB)
	}
}

// A key test without a model must not pass on types whose model list never reaches the vendor.
func TestKeyTestNeedsModelWithoutLiveList(t *testing.T) {
	srv, seen := upstreamRecorder(t)
	f := setup(t)
	ctx := context.Background()
	p := &store.Provider{Name: "fw", Type: "fireworks", BaseURL: srv.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "wrong"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := f.g.TestKey(ctx, p.ID, p.Keys[0].ID, "")
	if err != nil || res.OK || res.Error == "" || len(seen()) != 0 {
		t.Fatalf("model-less test on fireworks: %+v %v", res, err)
	}
	if res, _ = f.g.TestKey(ctx, p.ID, p.Keys[0].ID, "m1"); res.OK || res.Status != 404 || len(seen()) != 1 {
		t.Fatalf("with a model the vendor is called: %+v", res)
	}
}
