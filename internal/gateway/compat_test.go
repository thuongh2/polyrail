package gateway

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// Org providers of OpenAI-compatible vendor types run on Bifrost's openai base with the vendor's
// fixed URL; platform providers of the same types keep Bifrost's native adapter.
func TestOrgOpenAICompatibleConfig(t *testing.T) {
	org := store.Provider{ID: "og", OrgID: "o", Type: "groq", Enabled: true, Keys: []store.ProviderKey{{ID: "k", Value: "gk"}}}
	ds := store.Provider{ID: "ds", OrgID: "o", Type: "deepseek", Enabled: true, Keys: []store.ProviderKey{{ID: "k", Value: "dk"}}}
	plat := store.Provider{ID: "pg", Type: "groq", Enabled: true, Keys: []store.ProviderKey{{ID: "k", Value: "pk"}}}
	snap := &Snapshot{Providers: map[string]store.Provider{}, byBifrost: map[schemas.ModelProvider]string{}}
	for _, p := range []store.Provider{org, ds, plat} {
		snap.Providers[p.ID], snap.byBifrost[BifrostKey(p)] = p, p.ID
	}
	a := &account{snap: func() *Snapshot { return snap }}
	if BifrostKey(org) != "p-og" || BifrostKey(plat) != schemas.Groq {
		t.Fatalf("bifrost keys: org %s platform %s", BifrostKey(org), BifrostKey(plat))
	}
	cfg, err := a.GetConfigForProvider(BifrostKey(org))
	if err != nil || cfg.CustomProviderConfig == nil || cfg.CustomProviderConfig.BaseProviderType != schemas.OpenAI ||
		cfg.CustomProviderConfig.IsKeyLess || cfg.NetworkConfig.BaseURL != "https://api.groq.com/openai" || cfg.NetworkConfig.AllowPrivateNetwork {
		t.Fatalf("org groq config: %+v %+v %v", cfg.CustomProviderConfig, cfg.NetworkConfig, err)
	}
	cfg, _ = a.GetConfigForProvider(BifrostKey(ds))
	if p := cfg.CustomProviderConfig.RequestPathOverrides; cfg.NetworkConfig.BaseURL != "https://api.deepseek.com" ||
		p[schemas.ChatCompletionStreamRequest] != "/chat/completions" || p[schemas.ListModelsRequest] != "/models" {
		t.Fatalf("org deepseek keeps its native paths: %s %v", cfg.NetworkConfig.BaseURL, p)
	}
	cfg, _ = a.GetConfigForProvider(BifrostKey(plat))
	if cfg.CustomProviderConfig != nil || cfg.NetworkConfig.BaseURL != "" {
		t.Fatalf("platform groq stays native: %+v %q", cfg.CustomProviderConfig, cfg.NetworkConfig.BaseURL)
	}
	for typ, u := range OpenAICompatible {
		if MultiInstance(typ) || !strings.HasPrefix(u, "https://") || strings.HasSuffix(u, "/") || strings.HasSuffix(u, "/v1") {
			t.Errorf("%s: %q (the openai base appends /v1/...)", typ, u)
		}
		if !OrgType(typ) {
			t.Errorf("%s is not an org type", typ)
		}
	}
	if !OrgType("openai") || !OrgType("bedrock") || OrgType("elevenlabs") || OrgType("vllm") || OrgType("azure") {
		t.Fatal("OrgType: multi-instance types plus the OpenAI-compatible table only")
	}
}

// End to end: an org groq provider serves chat (stream and non-stream) through the openai base,
// at the vendor URL (here an httptest stand-in), with the org's key and no private-network grant.
func TestOrgOpenAICompatibleChat(t *testing.T) {
	var hits atomic.Int64
	inner := mockUpstream(t, false, &hits)
	var mu sync.Mutex
	var seen []string
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(vendor.Close)
	old := OpenAICompatible["groq"]
	OpenAICompatible["groq"] = vendor.URL
	t.Cleanup(func() { OpenAICompatible["groq"] = old }) // registered first, so it runs after the gateway shuts down
	f := setup(t)

	ctx := context.Background()
	p := &store.Provider{OrgID: f.org.ID, Name: "my-groq", Type: "groq", Enabled: true, Keys: []store.ProviderKey{{Value: "gsk-org"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	m := &store.Model{OrgID: f.org.ID, ID: "acme/llama", Name: "Llama", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "llama-3.3-70b"}}}
	if err := f.g.S.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"acme/llama","messages":[{"role":"user","content":"hi"}]}`)
	out := readJSON(t, resp)
	if resp.StatusCode != 200 || !strings.HasPrefix(out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string), "echo: hi") {
		t.Fatalf("non-stream %d: %v", resp.StatusCode, out)
	}
	resp = f.post(t, "/v1/chat/completions", f.key, `{"model":"acme/llama","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	var body strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		body.WriteString(sc.Text() + "\n")
	}
	if resp.StatusCode != 200 || !strings.Contains(body.String(), `"hel"`) || !strings.Contains(body.String(), "[DONE]") {
		t.Fatalf("stream %d: %s", resp.StatusCode, body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "/v1/chat/completions Bearer gsk-org" || seen[1] != seen[0] {
		t.Fatalf("vendor saw %v", seen)
	}
}
