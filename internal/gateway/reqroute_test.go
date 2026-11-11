package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

// recUpstream is an OpenAI-compatible provider that records request bodies and names itself in
// every answer ("from <tag>"). Usage is 1M prompt + 1M completion tokens.
type recUpstream struct {
	tag    string
	mu     sync.Mutex
	bodies []map[string]any
}

func (u *recUpstream) last() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return u.bodies[len(u.bodies)-1]
}

func (u *recUpstream) count() int { u.mu.Lock(); defer u.mu.Unlock(); return len(u.bodies) }

func (u *recUpstream) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		u.mu.Lock()
		u.bodies = append(u.bodies, req)
		u.mu.Unlock()
		const usage = `"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000,"prompt_tokens_details":{"cached_tokens":5}}`
		switch r.URL.Path {
		case "/v1/completions":
			fmt.Fprintf(w, `{"id":"c","object":"text_completion","created":1,"model":"up","choices":[{"index":0,"text":"from %s","finish_reason":"stop"}],%s}`, u.tag, usage)
		case "/v1/responses":
			fmt.Fprintf(w, `{"id":"r","object":"response","created_at":1,"model":"up","status":"completed","output":[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"from %s","annotations":[]}]}],"usage":{"input_tokens":1000000,"output_tokens":1000000,"total_tokens":2000000}}`, u.tag)
		default:
			if req["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"from %s\"}}]}\n\n", u.tag)
				fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],%s}\n\n", usage)
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"c","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"from %s"},"finish_reason":"stop"}],%s}`, u.tag, usage)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type routeFix struct {
	*fixture
	alpha, beta   *recUpstream
	alphaP, betaP *store.Provider
	badID         string
}

// setupRoute adds providers alpha (ZDR, $1/$1 per 1M) and beta (trains on data, target price
// $0.5/$0.5) and models: r/multi (alpha, beta; priority), r/a (alpha; $1 in, $2 out), r/down (the
// failing provider only), r/flaky (failing provider, then alpha; priority), r/sticky
// (alpha, beta; cache-affinity).
func setupRoute(t *testing.T) *routeFix {
	t.Helper()
	f := &routeFix{fixture: setup(t), alpha: &recUpstream{tag: "alpha"}, beta: &recUpstream{tag: "beta"}}
	ctx := context.Background()
	f.alphaP = &store.Provider{Name: "alpha", Type: "openai", BaseURL: f.alpha.serve(t).URL, Enabled: true, Keys: []store.ProviderKey{{Value: "ka"}},
		Privacy: store.ProviderPrivacy{ZDR: true}}
	f.betaP = &store.Provider{Name: "beta", Type: "openai", BaseURL: f.beta.serve(t).URL, Enabled: true, Keys: []store.ProviderKey{{Value: "kb"}},
		Privacy: store.ProviderPrivacy{TrainsOnData: true, RetentionDays: 30}}
	for _, p := range []*store.Provider{f.alphaP, f.betaP} {
		if err := f.g.S.PutProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for id, p := range f.g.Snapshot().Providers {
		if p.Name == "bad" {
			f.badID = id
		}
	}
	a, b := store.Target{ProviderID: f.alphaP.ID, Model: "up"}, store.Target{ProviderID: f.betaP.ID, Model: "up", InputPrice: 500_000, OutputPrice: 500_000}
	for _, m := range []*store.Model{
		{ID: "r/multi", Enabled: true, Strategy: "priority", InputPrice: 1_000_000, OutputPrice: 1_000_000, Targets: []store.Target{a, b}},
		{ID: "r/a", Enabled: true, InputPrice: 1_000_000, OutputPrice: 2_000_000, Targets: []store.Target{a}},
		{ID: "r/down", Enabled: true, InputPrice: 9_000_000, Targets: []store.Target{{ProviderID: f.badID, Model: "up"}}},
		{ID: "r/flaky", Enabled: true, Strategy: "priority", Targets: []store.Target{{ProviderID: f.badID, Model: "up2"}, a}},
		{ID: "r/sticky", Enabled: true, Strategy: "cache-affinity", Targets: []store.Target{a, b}},
		{ID: "r/emb", Kind: "embedding", Enabled: true, Targets: []store.Target{a}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", f.g.ChatCompletions)
	mux.HandleFunc("POST /v1/completions", f.g.Completions)
	mux.HandleFunc("POST /v1/responses", f.g.Responses)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// chat posts a chat request with extra top-level JSON fields and returns status, body and the
// serving provider tag ("" on error).
func (f *routeFix) chat(t *testing.T, model, extra string) (int, map[string]any, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]%s}`, model, extra)
	r := f.post(t, "/v1/chat/completions", f.key, body)
	m := readJSON(t, r)
	from := ""
	if ch, ok := m["choices"].([]any); ok && len(ch) > 0 {
		from = strings.TrimPrefix(fmt.Sprint(ch[0].(map[string]any)["message"].(map[string]any)["content"]), "from ")
	}
	return r.StatusCode, m, from
}

func TestRouteModelsFallbackBillsUsedModel(t *testing.T) {
	f := setupRoute(t)
	ctx := context.Background()
	r := f.post(t, "/v1/chat/completions", f.key,
		`{"model":"r/down","models":["nope/x","r/emb","r/a"],"messages":[{"role":"user","content":"hi"}],"provider":{"sort":"price"},"usage":{"include":true},"user":"u-42"}`)
	route, costHdr := r.Header.Get("X-GW-Route"), r.Header.Get("X-GW-Cost-Micros")
	m := readJSON(t, r)
	if r.StatusCode != 200 || m["model"] != "r/a" || !strings.HasSuffix(route, "; alpha/up") {
		t.Fatalf("fallback: %d model=%v route=%q %v", r.StatusCode, m["model"], route, m)
	}
	u := m["usage"].(map[string]any)
	// r/a: 1M in × $1 + 1M out × $2 = $3; the failing r/down ($9/1M in) is not billed
	if u["cost"] != 3.0 || u["cost_micros"] != 3_000_000.0 || costHdr != "3000000" {
		t.Fatalf("usage: %v header %s", u, costHdr)
	}
	if d, _ := u["prompt_tokens_details"].(map[string]any); d["cached_tokens"] != 5.0 {
		t.Fatalf("cached tokens: %v", u)
	}
	up := f.alpha.last()
	for _, k := range []string{"models", "provider", "usage", "preset"} {
		if _, ok := up[k]; ok {
			t.Fatalf("%s reached the upstream: %v", k, up)
		}
	}
	if up["user"] != "u-42" || up["model"] != "up" {
		t.Fatalf("upstream body: %v", up)
	}
	f.g.Usage.Close()
	time.Sleep(10 * time.Millisecond)
	day := store.Day(time.Now())
	if a, _ := f.g.S.KV.GetAgg(ctx, "agg/"+f.org.ID+"/"+day+"/m/r/a"); a[store.AggRequests] != 1 || a[store.AggCostMicros] != 3_000_000 {
		t.Fatalf("billed on r/a: %v", a)
	}
	if a, _ := f.g.S.KV.GetAgg(ctx, "agg/"+f.org.ID+"/"+day+"/m/r/down"); a[store.AggRequests] != 0 {
		t.Fatalf("r/down must not be billed: %v", a)
	}
	if a, _ := f.g.S.KV.GetAgg(ctx, usage.EndUserPrefix(f.org.ID, day)+"u-42"); a[store.AggRequests] != 1 || a[store.AggCostMicros] != 3_000_000 {
		t.Fatalf("end-user rollup: %v", a)
	}
	logs, _ := f.g.S.ListLogs(ctx, f.org.ID, 1, 10)
	if len(logs) != 1 || logs[0].EndUser != "u-42" || logs[0].Model != "r/a" {
		t.Fatalf("logs: %+v", logs)
	}
}

func TestRouteModelsAllFailing(t *testing.T) {
	f := setupRoute(t)
	if st, m, _ := f.chat(t, "nope/x", `,"models":["nope/y"]`); st != 404 || errCode(m) != "model_not_found" {
		t.Fatalf("no model: %d %v", st, m)
	}
	if st, m, _ := f.chat(t, "", `,"models":["r/a"]`); st != 200 || m["model"] != "r/a" {
		t.Fatalf("models without model: %d %v", st, m)
	}
	f.keyRec.Models = []string{"r/multi"} // allowlists apply to fallbacks too
	if _, err := f.g.S.UpdateKey(context.Background(), f.org.ID, f.keyRec.ID, func(k *store.APIKey) error { k.Models = f.keyRec.Models; return nil }); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	if st, m, _ := f.chat(t, "r/down", `,"models":["r/a"]`); st != 403 || errCode(m) != "model_not_allowed" {
		t.Fatalf("allowlist: %d %v", st, m)
	}
}

func TestRouteProviderPreferences(t *testing.T) {
	f := setupRoute(t)
	for _, tc := range []struct {
		name, model, extra, want string
	}{
		{"strategy order", "r/multi", ``, "alpha"},
		{"order", "r/multi", `,"provider":{"order":["beta"]}`, "beta"},
		{"order by type keeps declared order", "r/multi", `,"provider":{"order":["openai"]}`, "alpha"},
		{"only", "r/multi", `,"provider":{"only":["BETA"]}`, "beta"},
		{"ignore", "r/multi", `,"provider":{"ignore":["alpha"]}`, "beta"},
		{"zdr beats order", "r/multi", `,"provider":{"zdr":true,"order":["beta"]}`, "alpha"},
		{"data_collection deny", "r/multi", `,"provider":{"data_collection":"deny","order":["beta"]}`, "alpha"},
		{"sort price (target override)", "r/multi", `,"provider":{"sort":"price"}`, "beta"},
		{"sort object", "r/multi", `,"provider":{"sort":{"by":"price","partition":"none"}}`, "beta"},
		{":floor", "r/multi:floor", ``, "beta"},
		{"max_price", "r/multi", `,"provider":{"max_price":{"prompt":0.7}}`, "beta"},
		{"quantizations ignored", "r/multi", `,"provider":{"quantizations":["fp8"],"require_parameters":true}`, "alpha"},
		{"fallbacks", "r/flaky", ``, "alpha"},
	} {
		st, m, from := f.chat(t, tc.model, tc.extra)
		if st != 200 || from != tc.want {
			t.Errorf("%s: %d from %q, want %q (%v)", tc.name, st, from, tc.want, m)
		}
	}
	if st, m, _ := f.chat(t, "r/multi", `,"provider":{"max_price":{"prompt":0.1}}`); st != 503 || errCode(m) != "no_provider" {
		t.Errorf("max_price below all: %d %v", st, m)
	}
	if st, m, _ := f.chat(t, "r/flaky", `,"provider":{"allow_fallbacks":false}`); st == 200 {
		t.Errorf("allow_fallbacks false must stop at the first target: %v", m)
	}

	// :nitro sorts by observed throughput (beta faster), then latency when throughput is unknown
	snap := f.g.Snapshot()
	ta, tb := snap.Models["_|r/multi"].Targets[0], snap.Models["_|r/multi"].Targets[1]
	f.g.rt.mu.Lock()
	f.g.rt.stats = map[string]*targetStats{} // forget the mock's instant answers above
	f.g.rt.mu.Unlock()
	f.g.rt.observeTPS(ta, 10, time.Second)
	f.g.rt.observeTPS(tb, 900, time.Second)
	if st, _, from := f.chat(t, "r/multi:nitro", ``); st != 200 || from != "beta" {
		t.Errorf(":nitro: %d %s", st, from)
	}
	// preferred_min_throughput deprioritises alpha even under the priority strategy
	if st, _, from := f.chat(t, "r/multi", `,"provider":{"preferred_min_throughput":{"p50":100}}`); st != 200 || from != "beta" {
		t.Errorf("preferred_min_throughput: %d %s", st, from)
	}
}

func TestRouteSortByLatencyAndUnknownLast(t *testing.T) {
	_, m := routeFixture()
	snap, _ := routeFixture()
	g := &Gateway{rt: newRouter()}
	a, b, c := m.Targets[0], m.Targets[1], m.Targets[2]
	g.rt.observe(b, true, 50*time.Millisecond)
	g.rt.observe(a, true, 300*time.Millisecond)
	by := &store.ProviderPrefs{Sort: &store.SortPref{By: "latency"}}
	if got := ids(g.preferTargets(snap, m, m.Targets, by, "")); got != "bac" {
		t.Fatalf("latency sort: %s", got)
	}
	g.rt.br.openFor(targetID(b), time.Minute) // open circuit: last whatever the sort says
	if got := ids(g.preferTargets(snap, m, []store.Target{a, b, c}, by, "")); got != "acb" {
		t.Fatalf("open breaker last: %s", got)
	}
	lat := store.Percentile(0.1) // 100 ms
	if got := ids(g.preferTargets(snap, m, []store.Target{a, c}, &store.ProviderPrefs{PreferredMaxLatency: &lat}, "")); got != "ca" {
		t.Fatalf("preferred_max_latency: %s", got)
	}
}

func TestRouteInvalidInput(t *testing.T) {
	f := setupRoute(t)
	for _, extra := range []string{
		`,"models":"r/a"`,
		`,"models":["a","b","c","d","e","f"]`,
		`,"models":["@preset/x"]`,
		`,"provider":[]`,
		`,"provider":{"sort":"fast"}`,
		`,"provider":{"bogus":1}`,
		`,"provider":{"data_collection":"maybe"}`,
		`,"provider":{"max_price":{"prompt":-1}}`,
		`,"provider":{"order":"alpha"}`,
		`,"usage":"yes"`,
		`,"user":5`,
		`,"preset":7`,
	} {
		if st, m, _ := f.chat(t, "r/multi", extra); st != 400 || errCode(m) != "invalid_routing" {
			t.Errorf("%s: %d %v", extra, st, m)
		}
	}
	if st, m, _ := f.chat(t, "@preset/none", ``); st != 404 || errCode(m) != "preset_not_found" {
		t.Errorf("unknown preset: %d %v", st, m)
	}
}

func TestRoutePresets(t *testing.T) {
	f := setupRoute(t)
	ctx := context.Background()
	asc := false
	ps := &store.Preset{OrgID: f.org.ID, Slug: "fast", Model: "r/multi", Models: []string{"r/a"}, Provider: &store.ProviderPrefs{Order: []string{"beta"}, AllowFallbacks: &asc},
		Params: map[string]any{"temperature": 0.1, "max_tokens": 7.0, "system": "be brief"}}
	if err := f.g.S.PutPreset(ctx, ps, true); err != nil {
		t.Fatal(err)
	}
	other := &store.Org{Name: "other"}
	if err := f.g.S.CreateOrg(ctx, other, ""); err != nil {
		t.Fatal(err)
	}
	otherKey := KeyPrefix + "othersecret"
	if err := f.g.S.CreateKey(ctx, &store.APIKey{OrgID: other.ID, Name: "o"}, HashKey(otherKey)); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	st, m, from := f.chat(t, "@preset/fast", `,"temperature":0.9`)
	if st != 200 || from != "beta" || m["model"] != "r/multi" {
		t.Fatalf("preset: %d %s %v", st, from, m)
	}
	up := f.beta.last()
	msgs := up["messages"].([]any)
	if up["temperature"] != 0.9 || fmt.Sprint(up["max_completion_tokens"], up["max_tokens"]) == "<nil> <nil>" ||
		msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "be brief" || len(msgs) != 2 {
		t.Fatalf("preset params (request wins): %v", up)
	}
	// request routing fields replace the preset's
	if st, _, from := f.chat(t, "r/multi", `,"preset":"fast","provider":{"order":["alpha"]}`); st != 200 || from != "alpha" {
		t.Fatalf("request provider over preset: %d %s", st, from)
	}
	// presets are org-scoped
	r := f.post(t, "/v1/chat/completions", otherKey, `{"model":"@preset/fast","messages":[{"role":"user","content":"hi"}]}`)
	if m := readJSON(t, r); r.StatusCode != 404 || errCode(m) != "preset_not_found" {
		t.Fatalf("other org: %d %v", r.StatusCode, m)
	}
}

func TestRouteUsageCostStreamAndOtherAPIs(t *testing.T) {
	f := setupRoute(t)
	r := f.post(t, "/v1/chat/completions", f.key, `{"model":"r/a","stream":true,"usage":{"include":true},"messages":[{"role":"user","content":"hi"}]}`)
	defer r.Body.Close()
	var lastUsage map[string]any
	sc := bufio.NewScanner(r.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var c map[string]any
		_ = json.Unmarshal([]byte(data), &c)
		if c["model"] != "r/a" {
			t.Fatalf("chunk model: %s", data)
		}
		if u, ok := c["usage"].(map[string]any); ok {
			lastUsage = u
		}
	}
	if lastUsage["cost"] != 3.0 || lastUsage["cost_micros"] != 3_000_000.0 {
		t.Fatalf("stream usage: %v", lastUsage)
	}

	r = f.post(t, "/v1/completions", f.key, `{"model":"r/down","models":["r/a"],"prompt":"hi","usage":{"include":true}}`)
	if m := readJSON(t, r); r.StatusCode != 200 || m["model"] != "r/a" || m["usage"].(map[string]any)["cost"] != 3.0 {
		t.Fatalf("completions: %d %v", r.StatusCode, m)
	}
	r = f.post(t, "/v1/responses", f.key, `{"model":"r/multi:floor","input":"hi","usage":{"include":true}}`)
	m := readJSON(t, r)
	if r.StatusCode != 200 || m["model"] != "r/multi" || m["usage"].(map[string]any)["cost_micros"] != 2_000_000.0 {
		t.Fatalf("responses: %d %v", r.StatusCode, m)
	}
	if _, ok := f.beta.last()["usage"]; ok {
		t.Fatal("usage reached the upstream")
	}
	// without usage.include the body is unchanged
	r = f.post(t, "/v1/completions", f.key, `{"model":"r/a","prompt":"hi"}`)
	if m := readJSON(t, r); m["usage"].(map[string]any)["cost"] != nil {
		t.Fatalf("cost without include: %v", m)
	}
}

func TestRouteUserStickyAffinity(t *testing.T) {
	f := setupRoute(t)
	seen := map[string]bool{}
	for i := range 20 {
		user := fmt.Sprintf(`,"user":"user-%d"`, i)
		_, _, first := f.chat(t, "r/sticky", user)
		for range 3 {
			if _, _, from := f.chat(t, "r/sticky", user); from != first {
				t.Fatalf("user-%d moved from %s to %s", i, first, from)
			}
		}
		seen[first] = true
	}
	if len(seen) != 2 {
		t.Fatalf("users should spread over both providers: %v", seen)
	}
}

func TestPrivateRouteHidesUpstreamErrors(t *testing.T) {
	pub, priv := store.Model{ID: "p"}, store.Model{ID: "o", OrgID: "org"}
	if privateRoute(pub, []store.Target{{ProviderID: "a"}}) || !privateRoute(pub, []store.Target{{ProviderID: "a"}, {ProviderID: "b", Via: &priv}}) {
		t.Fatal("a private fallback model must hide upstream error text")
	}
}

func TestPresetValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, p := range []store.Preset{
		{Slug: "Bad", Model: "m"},
		{Slug: "-x", Model: "m"},
		{Slug: "ok"},
		{Slug: "ok", Model: "@preset/y"},
		{Slug: "ok", Model: "m", Models: []string{"a", "b", "c", "d", "e", "f"}},
		{Slug: "ok", Model: "m", Params: map[string]any{"logit_bias": 1.0}},
		{Slug: "ok", Model: "m", Params: map[string]any{"max_tokens": 1.5}},
		{Slug: "ok", Model: "m", Provider: &store.ProviderPrefs{Sort: &store.SortPref{By: "fast"}}},
	} {
		p.OrgID = f.org.ID
		if err := f.g.S.PutPreset(ctx, &p, true); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	p := store.Preset{OrgID: f.org.ID, Slug: "a-1", Model: "m"}
	if err := f.g.S.PutPreset(ctx, &p, true); err != nil {
		t.Fatal(err)
	}
	if err := f.g.S.PutPreset(ctx, &store.Preset{OrgID: f.org.ID, Slug: "a-1", Model: "m"}, true); err == nil {
		t.Fatal("duplicate slug")
	}
	if err := f.g.S.PutPreset(ctx, &store.Preset{OrgID: f.org.ID, Slug: "zz", Model: "m"}, false); err == nil {
		t.Fatal("update of a missing preset")
	}
}
