package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// W1 integration: a key under a require_zdr policy (A) sends `models` fallbacks and
// `provider.order` (B). Policy filter first, then request preferences: only ZDR targets are
// considered, the order is respected among them, and the fallback model serves when the first
// model's (ZDR) provider fails.
func TestPolicyComposesWithRequestRouting(t *testing.T) {
	f := setupRoute(t)
	ctx := context.Background()
	gamma := &recUpstream{tag: "gamma"}
	gammaP := &store.Provider{Name: "gamma", Type: "openai", BaseURL: gamma.serve(t).URL, Enabled: true,
		Keys: []store.ProviderKey{{Value: "kg"}}, Privacy: &store.ProviderPrivacy{ZDR: true}}
	if err := f.g.S.PutProvider(ctx, gammaP); err != nil {
		t.Fatal(err)
	}
	// the failing provider declares ZDR so the policy keeps it and the upstream failure is real
	bad, err := f.g.S.GetProviderSecrets(ctx, f.badID)
	if err != nil {
		t.Fatal(err)
	}
	bad.Privacy = &store.ProviderPrivacy{ZDR: true}
	for i := range bad.Keys {
		bad.Keys[i].Sealed = ""
	}
	if err := f.g.S.PutProvider(ctx, bad); err != nil {
		t.Fatal(err)
	}
	// c/second plans beta (not ZDR, cheapest) first, then alpha, then gamma
	for _, m := range []*store.Model{
		{ID: "c/first", Enabled: true, Targets: []store.Target{{ProviderID: f.badID, Model: "up"}}},
		{ID: "c/second", Enabled: true, Strategy: "priority", Targets: []store.Target{
			{ProviderID: f.betaP.ID, Model: "up"}, {ProviderID: f.alphaP.ID, Model: "up"}, {ProviderID: gammaP.ID, Model: "up"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	// without a policy the strategy's first provider (beta) is eligible for c/second
	if code, _, from := f.chat(t, "c/second", ""); code != 200 || from != "beta" {
		t.Fatalf("no policy: %d from %q", code, from)
	}
	f.putPolicy(t, store.Policy{Name: "zdr", IsOrgDefault: true, RequireZDR: true})

	snap := f.g.Snapshot()
	p := &principal{Org: *f.org, Key: *f.keyRec, rr: &reqRoute{
		models: []routeModel{{id: "c/first"}, {id: "c/second"}},
		prefs:  &store.ProviderPrefs{Order: []string{"gamma", "alpha"}},
	}}
	p.pol, err = f.g.loadPolicy(ctx, p.Key)
	if err != nil || p.pol == nil {
		t.Fatalf("policy: %v %v", p.pol, err)
	}
	m, ts, e := f.g.resolveRoute(p, "c/first", "chat", nil)
	if e != nil {
		t.Fatal(e.msg)
	}
	var names []string
	for _, tg := range ts {
		names = append(names, snap.Providers[tg.ProviderID].Name)
	}
	if m.ID != "c/first" || len(names) != 3 || names[0] != "bad" || names[1] != "gamma" || names[2] != "alpha" {
		t.Fatalf("planned chain: %s %v (want bad, gamma, alpha; beta filtered by policy)", m.ID, names)
	}

	// end to end: bad fails, gamma (first in order among ZDR targets) serves as c/second
	betaBefore, alphaBefore := f.beta.count(), f.alpha.count()
	r := f.post(t, "/v1/chat/completions", f.key,
		`{"model":"c/first","models":["c/first","c/second"],"provider":{"order":["gamma","alpha"]},"messages":[{"role":"user","content":"hi"}]}`)
	body := readJSON(t, r)
	if r.StatusCode != http.StatusOK || body["model"] != "c/second" || gamma.count() != 1 {
		t.Fatalf("fallback: %d %v gamma=%d", r.StatusCode, body, gamma.count())
	}
	if f.beta.count() != betaBefore || f.alpha.count() != alphaBefore {
		t.Fatalf("non-ZDR or lower-order provider called: beta %d->%d alpha %d->%d", betaBefore, f.beta.count(), alphaBefore, f.alpha.count())
	}

	// a request cannot widen the policy: only beta requested leaves nothing
	r = f.post(t, "/v1/chat/completions", f.key,
		`{"model":"c/second","provider":{"only":["beta"]},"messages":[{"role":"user","content":"hi"}]}`)
	if r.StatusCode < 400 || f.beta.count() != betaBefore {
		t.Fatalf("request widened the policy: %d beta %d", r.StatusCode, f.beta.count())
	}
	r.Body.Close()
}
