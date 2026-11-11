package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// wsFix: org with workspaces A and B, a key in each and one legacy key (no workspace = default).
// WS-A holds a BYOK provider, a private model on it and a preset.
type wsFix struct {
	*fixture
	keyA, keyB string
	recA, recB *store.APIKey
	prov       *store.Provider
	wsA, wsB   *store.Workspace
}

func setupWS(t *testing.T) *wsFix {
	t.Helper()
	f := &wsFix{fixture: setupWith(t, func(c *Config) { c.AllowPrivateURLs = true })}
	ctx := context.Background()
	s := f.g.S
	f.wsA, f.wsB = &store.Workspace{OrgID: f.org.ID, Name: "A", Slug: "a"}, &store.Workspace{OrgID: f.org.ID, Name: "B", Slug: "b"}
	for _, w := range []*store.Workspace{f.wsA, f.wsB} {
		if err := s.CreateWorkspace(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	// the default fixture's ok upstream, as a BYOK provider of WS-A
	pub, _ := s.ListProviders(ctx, false)
	var base string
	for _, p := range pub {
		if p.Name == "ok" {
			base = p.BaseURL
		}
	}
	f.prov = &store.Provider{OrgID: f.org.ID, WorkspaceID: f.wsA.ID, Name: "byok", Type: "openai", BaseURL: base, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := s.PutProvider(ctx, f.prov); err != nil {
		t.Fatal(err)
	}
	m := &store.Model{OrgID: f.org.ID, WorkspaceID: f.wsA.ID, ID: "acme/priv", Name: "Priv", Enabled: true, Targets: []store.Target{{ProviderID: f.prov.ID, Model: "up"}}}
	if err := s.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPreset(ctx, &store.Preset{OrgID: f.org.ID, WorkspaceID: f.wsA.ID, Slug: "fast", Model: "test/ok"}, true); err != nil {
		t.Fatal(err)
	}
	f.keyA, f.keyB = KeyPrefix+"wsA", KeyPrefix+"wsB"
	f.recA, f.recB = &store.APIKey{OrgID: f.org.ID, WorkspaceID: f.wsA.ID, Name: "a"}, &store.APIKey{OrgID: f.org.ID, WorkspaceID: f.wsB.ID, Name: "b"}
	for k, rec := range map[string]*store.APIKey{f.keyA: f.recA, f.keyB: f.recB} {
		if err := s.CreateKey(ctx, rec, HashKey(k)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *wsFix) chat(t *testing.T, key, model string) (int, string) {
	t.Helper()
	r := f.post(t, "/v1/chat/completions", key, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	m := readJSON(t, r)
	return r.StatusCode, errCode(m)
}

func TestWorkspaceIsolation(t *testing.T) {
	f := setupWS(t)
	if c, code := f.chat(t, f.keyA, "acme/priv"); c != 200 {
		t.Fatalf("WS-A key on its private model: %d %s", c, code)
	}
	if c, code := f.chat(t, f.keyB, "acme/priv"); c != 404 || code != "model_not_found" {
		t.Fatalf("WS-B key on WS-A's private model: %d %s", c, code)
	}
	if c, _ := f.chat(t, f.key, "acme/priv"); c != 404 {
		t.Fatalf("legacy (default workspace) key on WS-A's private model: %d", c)
	}
	if c, _ := f.chat(t, f.key, "test/ok"); c != 200 {
		t.Fatalf("legacy key on a public model: %d", c)
	}
	if c, _ := f.chat(t, f.keyA, "@preset/fast"); c != 200 {
		t.Fatalf("WS-A preset from WS-A: %d", c)
	}
	if c, code := f.chat(t, f.keyB, "@preset/fast"); c != 404 || code != "preset_not_found" {
		t.Fatalf("WS-A preset from WS-B: %d %s", c, code)
	}
	// /v1/models lists the private model only in its workspace
	for key, want := range map[string]bool{f.keyA: true, f.keyB: false, f.key: false} {
		req, _ := http.NewRequest("GET", f.srv.URL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if got := strings.Contains(string(raw), `"acme/priv"`); got != want {
			t.Errorf("key %s lists acme/priv: %v, want %v", key, got, want)
		}
	}

	// a WS-B model may not target WS-A's unshared provider; sharing lets it
	ctx := context.Background()
	mb := &store.Model{OrgID: f.org.ID, WorkspaceID: f.wsB.ID, ID: "acme/b", Name: "B", Enabled: true, Targets: []store.Target{{ProviderID: f.prov.ID, Model: "up"}}}
	if err := f.g.S.PutModel(ctx, mb); err == nil || !strings.Contains(err.Error(), "another workspace") {
		t.Fatalf("WS-B model on WS-A provider: %v", err)
	}
	f.prov.SharedAcrossWorkspaces = true
	if err := f.g.S.PutProvider(ctx, f.prov); err != nil {
		t.Fatal(err)
	}
	if err := f.g.S.PutModel(ctx, mb); err != nil {
		t.Fatalf("WS-B model on a shared provider: %v", err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if c, code := f.chat(t, f.keyB, "acme/b"); c != 200 {
		t.Fatalf("WS-B key through the shared provider: %d %s", c, code)
	}
	// unsharing takes effect at request time even for the model already saved
	f.prov.SharedAcrossWorkspaces = false
	if err := f.g.S.PutProvider(ctx, f.prov); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.chat(t, f.keyB, "acme/b"); c != 404 {
		t.Fatalf("WS-B key after unsharing: %d", c)
	}
}

func TestWorkspaceBudget(t *testing.T) {
	f := setupWS(t)
	ctx := context.Background()
	f.putPolicy(t, store.Policy{Name: "ws-a budget", WorkspaceID: f.wsA.ID, IsWorkspaceDefault: true,
		Budget: &store.PolicyBudget{LimitMicros: 1, Interval: "monthly"}})
	ep, err := f.g.EffectivePolicy(ctx, *f.recA)
	if err != nil || len(ep.Budgets) != 1 || ep.Budgets[0].Scope != "workspace" || ep.Budgets[0].Subject != f.wsA.ID {
		t.Fatalf("WS-A key budgets: %+v %v", ep, err)
	}
	if ep, _ := f.g.EffectivePolicy(ctx, *f.recB); len(ep.Policies) != 0 {
		t.Fatalf("WS-A policy applies to a WS-B key: %+v", ep)
	}
	// a second WS-A key shares the workspace counter: spend through one blocks the other
	k2 := KeyPrefix + "wsA2"
	if err := f.g.S.CreateKey(ctx, &store.APIKey{OrgID: f.org.ID, WorkspaceID: f.wsA.ID, Name: "a2"}, HashKey(k2)); err != nil {
		t.Fatal(err)
	}
	f.g.Usage.AddSpend(5, ep.Budgets[0].counter(f.org.ID, f.g.clock()))
	r := f.post(t, "/v1/chat/completions", k2, chatBody)
	if m := readJSON(t, r); r.StatusCode != 403 || errCode(m) != "policy_budget_exceeded" {
		t.Fatalf("second WS-A key over the workspace budget: %d %v", r.StatusCode, m)
	}
	if c, _ := f.chat(t, f.keyB, "test/ok"); c != 200 {
		t.Fatalf("WS-B key unaffected: %d", c)
	}

}
