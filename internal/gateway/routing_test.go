package gateway

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

func routeFixture() (*Snapshot, store.Model) {
	snap := &Snapshot{Providers: map[string]store.Provider{"a": {ID: "a", Type: "openai"}, "b": {ID: "b", Type: "openai"}, "c": {ID: "c", Type: "openai"}}}
	m := store.Model{ID: "x/m", OrgID: "org", InputPrice: 10, OutputPrice: 10, Targets: []store.Target{
		{ProviderID: "a", Model: "u", Weight: 1, InputPrice: 5, OutputPrice: 50},
		{ProviderID: "b", Model: "u", Weight: 1},
		{ProviderID: "c", Model: "u", Weight: 1, InputPrice: 1},
	}}
	return snap, m
}

func ids(ts []store.Target) string {
	var s []string
	for _, t := range ts {
		s = append(s, t.ProviderID)
	}
	return strings.Join(s, "")
}

// seq returns a deterministic random source yielding vals in turn.
func seq(vals ...float64) func() float64 {
	i := 0
	return func() float64 { v := vals[i%len(vals)]; i++; return v }
}

func TestStrategyOrders(t *testing.T) {
	_, base := routeFixture()
	a, b, c := base.Targets[0], base.Targets[1], base.Targets[2]
	for _, tc := range []struct {
		strategy string
		setup    func(rt *router)
		want     string
	}{
		{"priority", nil, "abc"},
		{"random", func(rt *router) { rt.f64 = seq(0) }, "bca"}, // Fisher-Yates with j=0 every step
		{"p2c", func(rt *router) { // picks a (i=0) and c (j=2); c is healthier so it goes first
			rt.f64 = seq(0, 0.9)
			for range 5 { // a: 67% errors, unknown latency → 0.33+1; c: 50ms, no errors → 1+0.56
				rt.observe(a, false, 0)
			}
			rt.observe(c, true, 50*time.Millisecond)
		}, "cab"},
		{"least-used", func(rt *router) { rt.begin(a); rt.begin(a); rt.begin(b) }, "cba"},
		{"latency", func(rt *router) { // c unknown (0) first; b 100ms + 20% errors = 300 > a 250
			rt.observe(a, true, 250*time.Millisecond)
			rt.observe(b, true, 100*time.Millisecond)
			rt.observe(b, false, 0)
		}, "cab"},
		{"cost", nil, "cba"}, // c 1+10, b 10+10 (model price), a 5+50
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			snap, m := routeFixture()
			m.Strategy = tc.strategy
			rt := newRouter()
			if tc.setup != nil {
				tc.setup(rt)
			}
			if got := ids(plan(m, snap, rt, nil)); got != tc.want {
				t.Fatalf("%s order = %s, want %s", tc.strategy, got, tc.want)
			}
		})
	}
}

func TestRoundRobinAndUnhealthyLast(t *testing.T) {
	snap, m := routeFixture()
	m.Strategy = "round-robin"
	rt := newRouter()
	var got []string
	for range 4 {
		got = append(got, ids(plan(m, snap, rt, nil)))
	}
	if strings.Join(got, ",") != "abc,bca,cab,abc" {
		t.Fatalf("round-robin rotation: %v", got)
	}
	rt.br.openFor(targetID(m.Targets[1]), time.Minute)
	m.Strategy = "priority"
	if got := ids(plan(m, snap, rt, nil)); got != "acb" {
		t.Fatalf("open target must be last: %s", got)
	}
}

func TestCacheAffinityStableAndSpread(t *testing.T) {
	snap, m := routeFixture()
	m.Strategy = "cache-affinity"
	key := func(k string) func() string { return func() string { return k } }
	first := map[string]int{}
	for i := range 600 {
		k := fmt.Sprintf("tenant-%d", i)
		p1 := ids(plan(m, snap, newRouter(), key(k)))
		if p2 := ids(plan(m, snap, newRouter(), key(k))); p1 != p2 {
			t.Fatalf("key %s: %s then %s", k, p1, p2)
		}
		first[p1[:1]]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if first[id] < 120 { // expect ~200 each
			t.Fatalf("affinity spread: %v", first)
		}
	}
	if got := plan(m, snap, newRouter(), key("")); len(got) != 3 {
		t.Fatalf("no key falls back to weighted: %v", got)
	}
	// dropping a target only moves the keys that were on it
	k := "tenant-7"
	full := ids(plan(m, snap, newRouter(), key(k)))
	m.Targets = slices.DeleteFunc(slices.Clone(m.Targets), func(t store.Target) bool { return t.ProviderID != full[:1] && t.ProviderID != full[1:2] })
	if got := ids(plan(m, snap, newRouter(), key(k))); got != full[:2] {
		t.Fatalf("rendezvous order changed: %s vs %s", got, full)
	}
}

func TestAffinityKey(t *testing.T) {
	pck := "conv-1"
	if affinityKey(&pck, "sys") != "conv-1" {
		t.Fatal("prompt_cache_key must win")
	}
	if affinityKey(nil, []int{}, nil, "") != "" {
		t.Fatal("no prefix means no key")
	}
	if a, b := affinityKey(nil, "you are a bot"), affinityKey(nil, "you are a bot"); a == "" || a != b || a == affinityKey(nil, "other") {
		t.Fatalf("prefix hash: %q %q", a, b)
	}
}

func TestBreakerCountsOnlyTargetFaults(t *testing.T) {
	snap, m := routeFixture()
	m.Targets = m.Targets[:1]
	id := targetID(m.Targets[0])
	for _, tc := range []struct {
		status int
		opens  bool
	}{{400, false}, {404, false}, {422, false}, {401, true}, {403, true}, {408, true}, {429, true}, {500, true}, {503, true}, {0, true}} {
		g := &Gateway{rt: newRouter()}
		be := &schemas.BifrostError{}
		if tc.status != 0 { // 0: no status, a network error
			be.StatusCode = &tc.status
		}
		for range 3 {
			g.settleErr(snap, m, m.Targets, be, time.Now())
		}
		if open := !g.rt.br.ok(id); open != tc.opens {
			t.Errorf("status %d: open=%v want %v", tc.status, open, tc.opens)
		}
	}
}

func TestRetryAfterOpensTarget(t *testing.T) {
	snap, m := routeFixture()
	g := &Gateway{rt: newRouter()}
	st := 429
	g.settleErr(snap, m, m.Targets, &schemas.BifrostError{StatusCode: &st, ExtraFields: schemas.BifrostErrorExtraFields{RetryAfter: 60_000}}, time.Now())
	if g.rt.br.ok(targetID(m.Targets[0])) || !g.rt.br.ok(targetID(m.Targets[1])) {
		t.Fatal("one 429 with Retry-After must open only the primary")
	}
	g.rt.br.openFor("x", 2*time.Hour)
	if until := g.rt.br.openUntil["x"]; time.Until(until) > 5*time.Minute+time.Second {
		t.Fatalf("Retry-After cap: %v", time.Until(until))
	}
}

func TestSettleErrUsesFallbackStatuses(t *testing.T) {
	snap, m := routeFixture()
	g := &Gateway{rt: newRouter()}
	bad, s503 := 400, 503
	be := &schemas.BifrostError{StatusCode: &bad, ExtraFields: schemas.BifrostErrorExtraFields{FallbackAttempts: []schemas.FallbackAttempt{
		{Provider: BifrostKey(snap.Providers["b"]), Model: "u", StatusCode: &s503},
		{Provider: BifrostKey(snap.Providers["c"]), Model: "u", Skipped: true},
	}}}
	for range 3 {
		g.settleErr(snap, m, m.Targets, be, time.Now())
	}
	if !g.rt.br.ok(targetID(m.Targets[0])) || g.rt.br.ok(targetID(m.Targets[1])) || !g.rt.br.ok(targetID(m.Targets[2])) {
		t.Fatal("only the fallback that answered 503 may open")
	}
}
