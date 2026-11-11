package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

func TestCompilePolicyCombination(t *testing.T) {
	k := store.APIKey{ID: "k1", OrgID: "o", CreatedBy: "u1"}
	pols := []store.Policy{
		{ID: "p1", Name: "org", IsOrgDefault: true, AllowedModels: []string{"openai/*", "anthropic/*"}, Filters: []store.PolicyFilter{{Pattern: "secret", Action: "redact"}}},
		{ID: "p2", Name: "member", MemberIDs: []string{"u1"}, AllowedModels: []string{"openai/gpt-4o*"}, RequireZDR: true,
			Filters: []store.PolicyFilter{{Pattern: "secret", Action: "block"}}},
		{ID: "p3", Name: "key", KeyIDs: []string{"k1"}, DenyDataCollection: true, AllowedProviders: []string{"openai", "Groq"}},
		{ID: "p4", Name: "other member", MemberIDs: []string{"u2"}, AllowedModels: []string{"none/*"}},
		{ID: "p5", Name: "opted out", IsOrgDefault: true, OptedOutKeyIDs: []string{"k1"}, AllowedModels: []string{"none/*"}},
		{ID: "p6", Name: "opted out member", IsOrgDefault: true, OptedOutMemberIDs: []string{"u1"}, AllowedModels: []string{"none/*"}},
	}
	ep, err := compilePolicy(k, pols)
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.Policies) != 3 {
		t.Fatalf("want 3 applied policies (org, member, key), got %+v", ep.Policies)
	}
	// allowlists intersect
	for id, want := range map[string]bool{"openai/gpt-4o-mini": true, "openai/o1": false, "anthropic/claude": false, "none/x": false} {
		if got := ep.AllowModel(id); got != want {
			t.Errorf("AllowModel(%s) = %v, want %v", id, got, want)
		}
	}
	if ep.modelDenier("openai/o1") != "member" {
		t.Errorf("denier: %q", ep.modelDenier("openai/o1"))
	}
	// privacy flags OR
	if !ep.RequireZDR || !ep.DenyDataCollection {
		t.Fatalf("privacy flags must OR: %+v", ep)
	}
	zdr := &store.ProviderPrivacy{ZDR: true}
	for _, c := range []struct {
		p    store.Provider
		want bool
	}{
		{store.Provider{Name: "a", Type: "openai", Privacy: zdr}, true},
		{store.Provider{Name: "groq-eu", Type: "groq", Privacy: zdr}, true}, // type match, case-insensitive
		{store.Provider{Name: "Groq", Type: "openai", Privacy: zdr}, true},  // name match
		{store.Provider{Name: "b", Type: "openai"}, false},                  // undeclared privacy
		{store.Provider{Name: "c", Type: "openai", Privacy: &store.ProviderPrivacy{ZDR: true, TrainsOnData: true}}, false},
		{store.Provider{Name: "d", Type: "openai", Privacy: &store.ProviderPrivacy{}}, false}, // not ZDR
		{store.Provider{Name: "e", Type: "anthropic", Privacy: zdr}, false},                   // provider not allowed
	} {
		if got := ep.AllowTarget(c.p); got != c.want {
			t.Errorf("AllowTarget(%s/%s) = %v, want %v", c.p.Name, c.p.Type, got, c.want)
		}
	}
	// filters union, block beats redact
	s := "my secret here"
	if f := ep.filter([]*string{&s}); f != "member" {
		t.Fatalf("block must win: %q", f)
	}
	// nil policy allows everything
	var none *EffectivePolicy
	if !none.AllowModel("x") || !none.AllowTarget(store.Provider{}) || none.filter([]*string{&s}) != "" {
		t.Fatal("nil policy must allow")
	}
	if ep, _ := compilePolicy(store.APIKey{ID: "k9", CreatedBy: "u9"}, pols[1:4]); ep != nil {
		t.Fatalf("no applicable policy must compile to nil, got %+v", ep)
	}
}

func TestPolicyRedact(t *testing.T) {
	ep, err := compilePolicy(store.APIKey{ID: "k"}, []store.Policy{{Name: "p", KeyIDs: []string{"k"},
		Filters: []store.PolicyFilter{{Name: "pin", Pattern: `\b\d{4}\b`, Action: "redact"}, {Pattern: `acct-\w+`, Action: "redact"}}}})
	if err != nil {
		t.Fatal(err)
	}
	s := "pin 1234 for acct-xyz"
	if f := ep.filter([]*string{&s}); f != "" || s != "pin [REDACTED:pin] for [REDACTED]" {
		t.Fatalf("redact: %q %q", f, s)
	}
}

func TestPolicyBudgetScopes(t *testing.T) {
	b := &store.PolicyBudget{LimitMicros: 10, Interval: "daily"}
	pols := []store.Policy{
		{ID: "d", Name: "default", IsOrgDefault: true, Budget: b},
		{ID: "k", Name: "per key", KeyIDs: []string{"k1"}, Budget: b},
		{ID: "mk", Name: "both", MemberIDs: []string{"u1"}, KeyIDs: []string{"k1"}, Budget: b},
	}
	ep, _ := compilePolicy(store.APIKey{ID: "k1", OrgID: "o", CreatedBy: "u1"}, pols)
	var got []string
	for _, x := range ep.Budgets {
		got = append(got, x.Policy+":"+x.Scope+":"+x.Subject)
	}
	want := "default:member:u1,per key:key:k1,both:member:u1,both:key:k1"
	if strings.Join(got, ",") != want {
		t.Fatalf("budgets %v, want %s", got, want)
	}
	// a key without a creator is budgeted per key
	ep, _ = compilePolicy(store.APIKey{ID: "k2", OrgID: "o"}, pols[:1])
	if len(ep.Budgets) != 1 || ep.Budgets[0].Scope != "key" {
		t.Fatalf("creatorless key: %+v", ep.Budgets)
	}
}

func TestBudgetPeriods(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	cases := []struct {
		interval, t, period, end string
	}{
		{"daily", "2026-11-11T23:59:59Z", "d20261111", "2026-11-12T00:00:00Z"},
		{"daily", "2026-11-12T00:00:00Z", "d20261112", "2026-11-13T00:00:00Z"},
		{"daily", "2026-11-11T23:30:00-05:00", "d20261112", "2026-11-13T00:00:00Z"}, // UTC
		{"weekly", "2026-11-15T23:59:59Z", "w20261109", "2026-11-16T00:00:00Z"},     // Sunday is still the week of Monday 9th
		{"weekly", "2026-11-16T00:00:00Z", "w20261116", "2026-11-23T00:00:00Z"},     // Monday starts a new week
		{"monthly", "2026-11-30T23:59:59Z", "m202611", "2026-12-01T00:00:00Z"},
		{"monthly", "2026-12-01T00:00:00Z", "m202612", "2027-01-01T00:00:00Z"},
		{"lifetime", "2026-12-01T00:00:00Z", "l", ""},
	}
	for _, c := range cases {
		p, end := budgetPeriod(c.interval, at(c.t))
		es := ""
		if !end.IsZero() {
			es = end.Format(time.RFC3339)
		}
		if p != c.period || es != c.end {
			t.Errorf("%s %s: got %s %s, want %s %s", c.interval, c.t, p, es, c.period, c.end)
		}
	}
}

// putPolicy stores a policy and drops cached principals, as the admin API does.
func (f *fixture) putPolicy(t *testing.T, p store.Policy) *store.Policy {
	t.Helper()
	p.OrgID = f.org.ID
	if err := f.g.S.PutPolicy(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	return &p
}

func errBody(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	var out struct {
		Error map[string]any `json:"error"`
	}
	_ = json.NewDecoder(r).Decode(&out)
	return out.Error
}

const polChat = `{"model":"test/chat","messages":[{"role":"user","content":"hi"}]}`

func TestPolicyBudgetIntervalRollover(t *testing.T) {
	f := setup(t)
	now := time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC)
	f.g.now = func() time.Time { return now }
	f.putPolicy(t, store.Policy{Name: "daily cap", IsOrgDefault: true, Budget: &store.PolicyBudget{LimitMicros: 1_000_000, Interval: "daily"}})
	resp := f.post(t, "/v1/chat/completions", f.key, polChat) // costs 2 USD: over the 1 USD cap after it
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("first request: %d", resp.StatusCode)
	}
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	e := errBody(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || e["code"] != "policy_budget_exceeded" || e["policy"] != "daily cap" {
		t.Fatalf("over budget: %d %v", resp.StatusCode, e)
	}
	now = now.Add(time.Hour) // next UTC day
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("new day must reset the budget: %d", resp.StatusCode)
	}
}

func TestPolicyBudgetPerMemberVsPerKey(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// two keys of member u1, one key of member u2
	mk := func(name, user string) string {
		secret := KeyPrefix + name
		if err := f.g.S.CreateKey(ctx, &store.APIKey{OrgID: f.org.ID, Name: name, CreatedBy: user}, HashKey(secret)); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	a1, a2, b1 := mk("a1secret", "u1"), mk("a2secret", "u1"), mk("b1secret", "u2")
	f.putPolicy(t, store.Policy{Name: "per member", MemberIDs: []string{"u1", "u2"}, Budget: &store.PolicyBudget{LimitMicros: 1, Interval: "monthly"}})
	code := func(key string) int {
		resp := f.post(t, "/v1/chat/completions", key, polChat)
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := code(a1); c != 200 {
		t.Fatalf("a1: %d", c)
	}
	if c := code(a2); c != 403 {
		t.Fatalf("a2 shares u1's member budget: want 403, got %d", c)
	}
	if c := code(b1); c != 200 {
		t.Fatalf("u2 has its own budget: %d", c)
	}
	// a per-key policy budget does not spill over to the member's other key
	f2 := setup(t)
	k1 := KeyPrefix + "k1secret"
	k1rec := &store.APIKey{OrgID: f2.org.ID, Name: "k1", CreatedBy: "u1"}
	if err := f2.g.S.CreateKey(ctx, k1rec, HashKey(k1)); err != nil {
		t.Fatal(err)
	}
	k2 := KeyPrefix + "k2secret"
	if err := f2.g.S.CreateKey(ctx, &store.APIKey{OrgID: f2.org.ID, Name: "k2", CreatedBy: "u1"}, HashKey(k2)); err != nil {
		t.Fatal(err)
	}
	f2.putPolicy(t, store.Policy{Name: "per key", KeyIDs: []string{k1rec.ID}, Budget: &store.PolicyBudget{LimitMicros: 1, Interval: "lifetime"}})
	for i, want := range []int{200, 403} {
		resp := f2.post(t, "/v1/chat/completions", k1, polChat)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("k1 request %d: %d", i, resp.StatusCode)
		}
	}
	resp := f2.post(t, "/v1/chat/completions", k2, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("k2 is not under the per-key budget: %d", resp.StatusCode)
	}
}

func TestPolicyModelAndProviderEnforcement(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.putPolicy(t, store.Policy{Name: "models", IsOrgDefault: true, AllowedModels: []string{"test/ok", "test/emb"}})
	resp := f.post(t, "/v1/chat/completions", f.key, polChat)
	e := errBody(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || e["code"] != "policy_model_not_allowed" || e["policy"] != "models" {
		t.Fatalf("model allowlist: %d %v", resp.StatusCode, e)
	}
	// /v1/models lists only allowed models
	r2, _ := newGet(f, "/v1/models")
	var list struct {
		Data []ModelInfo `json:"data"`
	}
	_ = json.NewDecoder(r2.Body).Decode(&list)
	r2.Body.Close()
	for _, m := range list.Data {
		if m.ID == "test/chat" {
			t.Fatal("/v1/models lists a model the policy denies")
		}
	}

	// ZDR: test/chat has targets "bad" (fails) and "ok"; only "ok" declares ZDR, so only it is tried
	f = setup(t)
	snap := f.g.Snapshot()
	var okID, badID string
	for id, p := range snap.Providers {
		if p.Name == "ok" {
			okID = id
		} else {
			badID = id
		}
	}
	setPrivacy := func(id string, pv *store.ProviderPrivacy) {
		p, _ := f.g.S.GetProviderSecrets(ctx, id)
		p.Privacy = pv
		for i := range p.Keys {
			p.Keys[i].Sealed = ""
		}
		if err := f.g.S.PutProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	setPrivacy(okID, &store.ProviderPrivacy{ZDR: true})
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	f.putPolicy(t, store.Policy{Name: "zdr", IsOrgDefault: true, RequireZDR: true})
	before := f.badHits.Load()
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 || f.badHits.Load() != before {
		t.Fatalf("ZDR must skip the undeclared provider: %d, bad hits %d->%d", resp.StatusCode, before, f.badHits.Load())
	}
	// no eligible provider: refuse, do not fall back
	setPrivacy(okID, &store.ProviderPrivacy{ZDR: false})
	setPrivacy(badID, nil)
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	okBefore := f.okHits.Load()
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	e = errBody(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || e["code"] != "policy_no_eligible_provider" || e["policy"] != "zdr" || f.okHits.Load() != okBefore {
		t.Fatalf("no eligible provider: %d %v", resp.StatusCode, e)
	}
	// provider allowlist
	f.putPolicy(t, store.Policy{Name: "only ok", KeyIDs: []string{f.keyRec.ID}, AllowedProviders: []string{"ok"}})
	ps, _ := f.g.S.ListPolicies(ctx, f.org.ID)
	for _, p := range ps {
		if p.Name == "zdr" {
			_ = f.g.S.DeletePolicy(ctx, f.org.ID, p.ID)
		}
	}
	f.g.InvalidateAuth()
	before = f.badHits.Load()
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 || f.badHits.Load() != before {
		t.Fatalf("provider allowlist: %d, bad hits %d->%d", resp.StatusCode, before, f.badHits.Load())
	}
}

func newGet(f *fixture, path string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	return http.DefaultClient.Do(req)
}

func TestPolicyFiltersAndCacheInvalidation(t *testing.T) {
	f := setup(t)
	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"card 4111-1111"}]}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("before policy: %d", resp.StatusCode)
	}
	// the principal is cached now; a policy write must apply on the next request
	p := f.putPolicy(t, store.Policy{Name: "pci", IsOrgDefault: true, Filters: []store.PolicyFilter{{Name: "card", Pattern: `\d{4}-\d{4}`, Action: "redact"}}})
	resp = f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"card 4111-1111"}]}`)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "card [REDACTED:card]") {
		t.Fatalf("redact: %d %s", resp.StatusCode, b)
	}
	p.Filters[0].Action = "block"
	if err := f.g.S.PutPolicy(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	resp = f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"card 4111-1111"}]}`)
	e := errBody(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || e["code"] != "policy_filter_blocked" || e["policy"] != "pci" {
		t.Fatalf("block: %d %v", resp.StatusCode, e)
	}
	// embeddings go through the same filter
	resp = f.post(t, "/v1/embeddings", f.key, `{"model":"test/emb","input":"card 4111-1111"}`)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("embeddings filter: %d", resp.StatusCode)
	}
}

func TestPolicyCrossOrgIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := &store.Org{Name: "other"}
	if err := f.g.S.CreateOrg(ctx, other, ""); err != nil {
		t.Fatal(err)
	}
	p := store.Policy{OrgID: other.ID, Name: "deny all", IsOrgDefault: true, AllowedModels: []string{"nothing/*"}, KeyIDs: []string{f.keyRec.ID}}
	if err := f.g.S.PutPolicy(ctx, &p); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	resp := f.post(t, "/v1/chat/completions", f.key, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("another org's policy (even naming this key) must not apply: %d", resp.StatusCode)
	}
}

func TestPolicySpendBYOK(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := func(byok bool) store.Policy {
		return store.Policy{ID: "p", Name: "b", KeyIDs: []string{f.keyRec.ID}, Budget: &store.PolicyBudget{LimitMicros: 100, Interval: "lifetime", IncludeBYOK: byok}}
	}
	for _, c := range []struct {
		byok        bool
		orgModel    string
		wantCounted bool
	}{{false, "", true}, {false, "org", false}, {true, "org", true}} {
		ep, _ := compilePolicy(*f.keyRec, []store.Policy{b(c.byok)})
		p := &principal{Key: *f.keyRec, Org: *f.org, pol: ep}
		ctr := ep.Budgets[0].counter(f.org.ID, time.Now())
		before, _ := f.g.Usage.Spent(ctx, ctr)
		f.g.recordPolicySpend(p, store.Model{OrgID: c.orgModel}, 7)
		after, _ := f.g.Usage.Spent(ctx, ctr)
		if counted := after-before == 7; counted != c.wantCounted {
			t.Errorf("include_byok=%v org model=%q: counted=%v", c.byok, c.orgModel, counted)
		}
	}
}
