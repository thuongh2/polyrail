package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/limits"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

func TestKeyHealthTransitions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	k := newKeyHealth()
	k.now = func() time.Time { return now }

	// 429 with Retry-After: cooldown is the hint, capped at 5 minutes
	k.result("a", 429, 90*time.Second)
	if until, ok := k.cooling("a"); !ok || until != now.Add(90*time.Second) {
		t.Fatalf("retry-after cooldown: %v %v", until, ok)
	}
	k.result("a", 429, time.Hour)
	if until, _ := k.cooling("a"); until != now.Add(5*time.Minute) {
		t.Fatalf("retry-after cap: %v", until)
	}
	// without a hint: 30 s doubling to 5 minutes
	for i, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		k.result("b", 503, 0)
		if until, _ := k.cooling("b"); until != now.Add(want) {
			t.Fatalf("failure %d: cooldown %v, want %v", i+1, until.Sub(now), want)
		}
	}
	// request errors are not the key's fault
	k.result("c", 400, 0)
	if _, ok := k.cooling("c"); ok {
		t.Fatal("400 cooled the key")
	}
	// 3 consecutive auth failures quarantine; success clears everything
	if k.result("d", 401, 0) || k.result("d", 403, 0) {
		t.Fatal("quarantined too early")
	}
	if !k.result("d", 401, 0) {
		t.Fatal("third auth failure did not quarantine")
	}
	k.result("d", 200, 0)
	if _, ok := k.cooling("d"); ok {
		t.Fatal("success did not clear the cooldown")
	}
	if k.result("d", 401, 0) {
		t.Fatal("success did not reset the auth count")
	}
	// clear (re-enable, Clear status) lifts the cooldown but keeps the last use
	k.clear("d")
	if _, ok := k.cooling("d"); ok || k.lastUsed("d") != now || k.result("d", 401, 0) {
		t.Fatal("clear: cooldown, last use or auth count wrong")
	}
	if MaskKey("sk-abcdefghijklmnop") != "sk-a****mnop" || MaskKey("short-key") != "****" {
		t.Fatal("mask")
	}
}

type keyFixture struct {
	g    *Gateway
	srv  *httptest.Server
	prov *store.Provider
	key  string
	mu   sync.Mutex
	hits map[string]int // by upstream key value
}

// keySetup runs a gateway with one openai provider holding keys; the upstream answers 401 for
// values starting with "bad", 200 otherwise. A second provider "spare" (key "spare-key") is the
// fallback target of model test/k.
func keySetup(t *testing.T, keys ...store.ProviderKey) *keyFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := store.NewBox("test-master-key-123456")
	s := store.New(kv, box)
	f := &keyFixture{hits: map[string]int{}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.hits[v]++
		f.mu.Unlock()
		if strings.HasPrefix(v, "bad") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error":{"message":"invalid api key sk-secretvalue123 (sent %s)","type":"invalid_request_error"}}`, v)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"up","object":"model","created":1,"owned_by":"x"}]}`)
			return
		}
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	t.Cleanup(up.Close)
	f.prov = &store.Provider{Name: "main", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: keys}
	spare := &store.Provider{Name: "spare", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "spare-key"}}}
	for _, p := range []*store.Provider{f.prov, spare} {
		if err := s.PutProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	m := &store.Model{ID: "test/k", Name: "K", Enabled: true, Strategy: "priority",
		Targets: []store.Target{{ProviderID: f.prov.ID, Model: "up"}, {ProviderID: spare.ID, Model: "up"}}}
	if err := s.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	org := &store.Org{Name: "acme"}
	if err := s.CreateOrg(ctx, org, ""); err != nil {
		t.Fatal(err)
	}
	f.key = KeyPrefix + "keytest"
	if err := s.CreateKey(ctx, &store.APIKey{OrgID: org.ID, Name: "k"}, HashKey(f.key)); err != nil {
		t.Fatal(err)
	}
	pipe := usage.New(kv, 100, 20*time.Millisecond, usage.StoreSink{S: s})
	t.Cleanup(pipe.Close)
	f.g, err = New(ctx, s, limits.NewLocal(), pipe, Config{MaxBodyBytes: 1 << 20, Concurrency: 4, Buffer: 16, PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.g.BF.Shutdown)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", f.g.ChatCompletions)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *keyFixture) chat(t *testing.T) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"test/k","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("x-gateway-provider")
}

func (f *keyFixture) hit(v string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[v]
}

func (f *keyFixture) stored(t *testing.T) store.Provider {
	t.Helper()
	p, err := f.g.S.GetProvider(context.Background(), f.prov.ID)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// keyStats flushes usage and returns this hour's per-key counters of the main provider.
func (f *keyFixture) keyStats(t *testing.T) map[string]store.Agg {
	t.Helper()
	f.g.Usage.Close()
	prefix := usage.KeyStatPrefix(f.prov.ID, time.Now())
	all, err := f.g.S.KV.ScanAgg(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Agg{}
	for k, a := range all {
		out[strings.TrimPrefix(k, prefix)] = a
	}
	return out
}

type events struct {
	mu  sync.Mutex
	got []string
}

func (e *events) Emit(org, typ string, data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, typ+" "+fmt.Sprint(data["key_id"]))
}

func (e *events) has(s string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, g := range e.got {
		if g == s {
			return true
		}
	}
	return false
}

// Two keys, one rejected upstream: every request is served (Bifrost rotates to the other key),
// each attempt is attributed to its key through the attempt trail / RoutingInfo.Key, and three
// auth failures quarantine the bad key, which then gets no traffic.
func TestKeyAttributionAndQuarantine(t *testing.T) {
	f := keySetup(t, store.ProviderKey{Name: "good", Value: "good-key-0001"}, store.ProviderKey{Name: "bad", Value: "bad-key-00001"})
	ev := &events{}
	f.g.Hooks = ev
	goodID, badID := f.prov.Keys[0].ID, f.prov.Keys[1].ID
	// each auth failure also cools the key down; a clock that jumps 10 minutes per call lets the
	// bad key come back between requests
	clock := time.Now()
	f.g.keys.now = func() time.Time { clock = clock.Add(10 * time.Minute); return clock }
	for i := 0; i < 80 && f.hit("bad-key-00001") < 3; i++ {
		if st, prov := f.chat(t); st != 200 || prov != "main" {
			t.Fatalf("request %d: status %d provider %q", i, st, prov)
		}
	}
	if f.hit("bad-key-00001") < 3 {
		t.Fatal("bad key never tried 3 times")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !f.stored(t).Keys[1].Quarantined || !ev.has(EventKeyQuarantined+" "+badID) {
		if time.Now().After(deadline) {
			t.Fatal("bad key not quarantined")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p := f.stored(t); p.Keys[0].Quarantined || !strings.Contains(p.Keys[1].StatusReason, "authentication") {
		t.Fatalf("stored status: %+v", p.Keys)
	}
	if !ev.has(EventKeyQuarantined + " " + badID) {
		t.Fatalf("events: %v", ev.got)
	}
	if st, _ := f.g.KeyStatus(f.g.Snapshot().Providers[f.prov.ID].Keys[1]); st != KeyQuarantined {
		t.Fatalf("status %s", st)
	}
	before := f.hit("bad-key-00001")
	for range 10 {
		if st, _ := f.chat(t); st != 200 {
			t.Fatalf("status %d", st)
		}
	}
	if f.hit("bad-key-00001") != before {
		t.Fatal("quarantined key still used")
	}
	// Bifrost names the serving key by its Name, which the gateway sets to the stored key id
	resp, be := f.g.BF.ChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: BifrostKey(*f.prov), Model: "up",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}}})
	if be != nil || resp.ExtraFields.RoutingInfo.Key != goodID {
		t.Fatalf("routing key: %v %v", be, resp)
	}
	stats := f.keyStats(t)
	if g := stats[goodID]; g[store.AggRequests] < 10 || g[store.AggErrors] != 0 || g[store.AggInTokens] == 0 || g[store.AggOutTokens] == 0 {
		t.Fatalf("good key stats: %v", g)
	}
	if b := stats[badID]; b[store.AggErrors] < 3 || b[store.AggRequests] != b[store.AggErrors] {
		t.Fatalf("bad key stats: %v", b)
	}
	if f.g.KeyLastUsed(goodID).IsZero() {
		t.Fatal("last used not tracked")
	}
}

func TestDisabledKeyExcludedAndAllFilteredFallback(t *testing.T) {
	f := keySetup(t, store.ProviderKey{Value: "good-key-0001"}, store.ProviderKey{Value: "good-key-0002", Disabled: true})
	// a provider PUT keeps the stored status (the dialog does not send it)
	p := f.stored(t)
	p.Keys[1].Disabled = false
	if err := f.g.S.PutProvider(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	if !f.stored(t).Keys[1].Disabled {
		t.Fatal("PUT re-enabled the key")
	}
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 8 {
		if st, prov := f.chat(t); st != 200 || prov != "main" {
			t.Fatalf("status %d provider %q", st, prov)
		}
	}
	if f.hit("good-key-0002") != 0 || f.hit("good-key-0001") != 8 {
		t.Fatalf("hits %v", f.hits)
	}
	// every enabled key cooling down: they are still used (provider.unavailable is reported)
	ev := &events{}
	f.g.Hooks = ev
	f.g.keys.result(f.prov.Keys[0].ID, 503, time.Minute)
	if st, prov := f.chat(t); st != 200 || prov != "main" {
		t.Fatalf("all-filtered: status %d provider %q", st, prov)
	}
	if !ev.has(EventProviderUnavailable + " <nil>") {
		t.Fatalf("events: %v", ev.got)
	}
	// every key disabled: the provider fails and the request fails over to the spare provider
	_, err := f.g.S.UpdateProviderKeys(context.Background(), "", f.prov.ID, func(p *store.Provider) error {
		p.Keys[0].Disabled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, prov := f.chat(t); st != 200 || prov != "spare" {
		t.Fatalf("all disabled: status %d provider %q", st, prov)
	}
}

func TestTestKeyPinsAndSanitizes(t *testing.T) {
	f := keySetup(t, store.ProviderKey{Value: "good-key-0001"}, store.ProviderKey{Value: "bad-key-00001"})
	ctx := context.Background()
	good, bad := f.prov.Keys[0].ID, f.prov.Keys[1].ID
	// quarantine the good key by hand: a test still reaches it (pinned), and success lifts it
	if err := f.g.SetKeyQuarantine(ctx, "", f.prov.ID, good, true, "operator"); err != nil {
		t.Fatal(err)
	}
	res, err := f.g.TestKey(ctx, f.prov.ID, good, "")
	if err != nil || !res.OK || res.Status != 200 {
		t.Fatalf("good key: %+v %v", res, err)
	}
	if f.stored(t).Keys[0].Quarantined {
		t.Fatal("successful test did not lift the quarantine")
	}
	before := f.hit("good-key-0001")
	res, err = f.g.TestKey(ctx, f.prov.ID, bad, "up")
	if err != nil || res.OK || res.Status != 401 {
		t.Fatalf("bad key: %+v %v", res, err)
	}
	if f.hit("good-key-0001") != before || f.hit("bad-key-00001") == 0 {
		t.Fatal("test was not pinned to the key")
	}
	if strings.Contains(res.Error, "sk-secretvalue123") || strings.Contains(res.Error, "bad-key-00001") || !strings.Contains(res.Error, "bad-****0001") {
		t.Fatalf("secret-like text or the key not redacted: %q", res.Error)
	}
	if st, _ := f.g.KeyStatus(f.g.Snapshot().Providers[f.prov.ID].Keys[1]); st != KeyCooldown {
		t.Fatalf("failed test should cool the key: %s", st)
	}
}
