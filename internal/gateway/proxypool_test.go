package gateway

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

func testPool(strategy string, ids ...string) store.ProxyPool {
	p := store.ProxyPool{ID: "p1", Name: "pool", Strategy: strategy, StickyMinutes: 30, FailClosed: true}
	for _, id := range ids {
		p.Members = append(p.Members, store.ProxyMember{ID: id, URL: "http://" + id + ":3128", Enabled: true})
	}
	return p
}

func memberIDs(ms []store.ProxyMember) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

// fakeClock returns a rotator on a settable clock.
func fakeClock() (*rotator, *time.Time) {
	now := time.Unix(1_700_000_000, 0)
	r := newRotator()
	r.now = func() time.Time { return now }
	return r, &now
}

func TestRotatorStrategies(t *testing.T) {
	r, now := fakeClock()
	rr := testPool("round-robin", "a", "b", "c")
	for _, want := range []string{"a,b,c", "b,c,a", "c,a,b", "a,b,c"} {
		if got := memberIDs(r.order(rr)); got != want {
			t.Fatalf("round-robin order %s, want %s", got, want)
		}
	}
	rr.Members[1].Enabled = false
	if got := memberIDs(r.order(rr)); got != "c,a" && got != "a,c" {
		t.Fatalf("disabled member offered: %s", got)
	}

	first := map[string]int{}
	for range 300 {
		first[r.order(testPool("random", "a", "b", "c"))[0].ID]++
	}
	if len(first) != 3 {
		t.Fatalf("random never picked some member first: %v", first)
	}

	st := testPool("sticky", "a", "b", "c")
	pick := r.order(st)[0]
	r.succeeded(st, pick, time.Millisecond)
	for range 5 {
		if got := r.order(st)[0]; got.ID != pick.ID {
			t.Fatalf("sticky moved from %s to %s inside the window", pick.ID, got.ID)
		}
	}
	*now = now.Add(31 * time.Minute) // window over: the next pick may differ, and sticks in turn
	next := r.order(st)[0]
	r.succeeded(st, next, time.Millisecond)
	if got := r.order(st)[0]; got.ID != next.ID {
		t.Fatalf("sticky did not keep the new pick %s: %s", next.ID, got.ID)
	}
	r.failed(st, next, fmt.Errorf("boom")) // a set-aside sticky member is skipped
	if got := r.order(st); slices.ContainsFunc(got, func(m store.ProxyMember) bool { return m.ID == next.ID }) {
		t.Fatalf("set-aside sticky member offered: %s", memberIDs(got))
	}

	lat := testPool("latency", "a", "b", "c")
	lat.ID = "p2" // fresh member state
	r.succeeded(lat, lat.Members[0], 90*time.Millisecond)
	r.succeeded(lat, lat.Members[1], 10*time.Millisecond)
	if got := memberIDs(r.order(lat)); got != "c,b,a" { // c unmeasured first, then fastest
		t.Fatalf("latency order %s", got)
	}
}

func TestSetAsideCooldownAndDown(t *testing.T) {
	r, now := fakeClock()
	p := testPool("round-robin", "a", "b")
	a := p.Members[0]
	var got []time.Duration
	for range 6 {
		r.failed(p, a, fmt.Errorf("refused"))
		got = append(got, r.state(p.ID, a.ID).cooldown)
	}
	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second, 600 * time.Second, 600 * time.Second}
	if !slices.Equal(got, want) {
		t.Fatalf("cooldowns %v, want %v", got, want)
	}
	if h := r.health(p)[a.ID]; h.State != "down" {
		t.Fatalf("3+ consecutive failures must mark the member down: %+v", h)
	}
	*now = now.Add(time.Hour) // cooldown over, but down until a good probe
	if got := memberIDs(r.order(p)); got != "b" {
		t.Fatalf("down member offered: %s", got)
	}
	r.probed(p, a, 5*time.Millisecond, nil)
	if got := memberIDs(r.order(p)); !strings.Contains(got, "a") || r.health(p)[a.ID].State != "up" {
		t.Fatalf("good probe did not bring the member back: %s %+v", got, r.health(p)[a.ID])
	}
	r.failed(p, a, fmt.Errorf("refused")) // one failure: set aside only, cooldown restarts at 60 s after success
	r.succeeded(p, a, time.Millisecond)
	r.failed(p, a, fmt.Errorf("refused"))
	if s := r.state(p.ID, a.ID); s.cooldown != setAsideFirst || s.down {
		t.Fatalf("success must reset the cooldown: %+v", s)
	}
	if h := r.health(p)[a.ID]; h.State != "set_aside" || h.SetAsideUntil == nil {
		t.Fatalf("health: %+v", h)
	}
}

// deadAddr is a TCP address nothing listens on.
func deadAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// putPool stores a pool and assigns it to the provider named prov (or none when prov is "").
func putPool(t *testing.T, f *fixture, pool *store.ProxyPool, prov string) {
	t.Helper()
	ctx := context.Background()
	if err := f.g.S.PutProxyPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.g.Snapshot().Providers {
		if p.Name != prov {
			continue
		}
		stored, err := f.g.S.GetProvider(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		stored.Keys[0].Value = p.Keys[0].Value
		stored.ProxyPool = pool.ID
		if err := f.g.S.PutProvider(ctx, stored); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
}

const okChat = `{"model":"test/ok","messages":[{"role":"user","content":"hi"}]}`

func TestPoolFailsOverToWorkingMember(t *testing.T) {
	f := setup(t)
	good := &connectProxy{}
	gsrv := httptest.NewServer(good)
	defer gsrv.Close()
	pool := &store.ProxyPool{Name: "egress", FailClosed: true, Members: []store.ProxyMember{
		{ID: "dead", URL: "http://" + deadAddr(t), Enabled: true},
		{ID: "live", URL: gsrv.URL, Username: "bob", Password: "pw", Enabled: true},
	}}
	putPool(t, f, pool, "ok")
	for range 3 {
		resp := f.post(t, "/v1/chat/completions", f.key, okChat)
		if m := readJSON(t, resp); resp.StatusCode != 200 {
			t.Fatalf("chat through pool: %d %v", resp.StatusCode, m)
		}
	}
	targets, auth := good.seen()
	if len(targets) == 0 || auth[0] != "Basic "+base64.StdEncoding.EncodeToString([]byte("bob:pw")) {
		t.Fatalf("working member saw %v %v", targets, auth)
	}
	h := f.g.ProxyHealth(*pool)
	if h["dead"].State != "set_aside" || h["live"].State != "up" {
		t.Fatalf("health after failover: %+v", h)
	}
}

func TestPoolFailClosedAndOpen(t *testing.T) {
	f := setup(t)
	pool := &store.ProxyPool{Name: "only-dead", FailClosed: true, Members: []store.ProxyMember{{ID: "dead", URL: "http://" + deadAddr(t), Enabled: true}}}
	putPool(t, f, pool, "ok")
	hits := f.okHits.Load()
	if resp := f.post(t, "/v1/chat/completions", f.key, okChat); resp.StatusCode == 200 || f.okHits.Load() != hits {
		t.Fatalf("fail-closed pool went direct: %d", resp.StatusCode)
	}
	pool.FailClosed = false
	if err := f.g.S.PutProxyPool(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp := f.post(t, "/v1/chat/completions", f.key, okChat)
	if m := readJSON(t, resp); resp.StatusCode != 200 || f.okHits.Load() != hits+1 {
		t.Fatalf("fail-open pool did not connect directly: %d %v", resp.StatusCode, m)
	}
}

// A provider's 429 travels inside the TLS/HTTP tunnel; the forwarder never sees it, so it must
// neither set a member aside nor move a sticky pick (no rate-limit evasion by rotation, D23).
func TestProvider429DoesNotRotate(t *testing.T) {
	var limited atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limited.Add(1)
		w.Header().Set("Connection", "close") // every retry needs a new tunnel
		http.Error(w, `{"error":{"message":"rate limited"}}`, http.StatusTooManyRequests)
	}))
	defer up.Close()
	f := setup(t)
	ctx := context.Background()
	var okID string
	for _, p := range f.g.Snapshot().Providers {
		if p.Name == "ok" {
			okID = p.ID
		}
	}
	stored, _ := f.g.S.GetProvider(ctx, okID)
	stored.BaseURL, stored.Keys[0].Value = up.URL, "k2"
	if err := f.g.S.PutProvider(ctx, stored); err != nil {
		t.Fatal(err)
	}
	pa, pb := &connectProxy{}, &connectProxy{}
	sa, sb := httptest.NewServer(pa), httptest.NewServer(pb)
	defer sa.Close()
	defer sb.Close()
	pool := &store.ProxyPool{Name: "sticky", Strategy: "sticky", FailClosed: true, Members: []store.ProxyMember{
		{ID: "a", URL: sa.URL, Enabled: true}, {ID: "b", URL: sb.URL, Enabled: true}}}
	putPool(t, f, pool, "ok")
	for range 4 {
		resp := f.post(t, "/v1/chat/completions", f.key, okChat)
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("want the upstream 429, got %d", resp.StatusCode)
		}
	}
	ta, _ := pa.seen()
	tb, _ := pb.seen()
	if limited.Load() < 4 || (len(ta) > 0 && len(tb) > 0) || len(ta)+len(tb) < 4 {
		t.Fatalf("429s moved the sticky member: a=%d b=%d upstream=%d", len(ta), len(tb), limited.Load())
	}
	for id, h := range f.g.ProxyHealth(*pool) {
		if h.State == "set_aside" || h.State == "down" {
			t.Fatalf("a 429 set member %s aside: %+v", id, h)
		}
	}
}

// connectVia sends a CONNECT for target to the forwarder with Basic user:pw and returns the status.
func connectVia(t *testing.T, addr, user, pw, target string) int {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pw))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestForwarderAuthAndTenantTargets(t *testing.T) {
	good := &connectProxy{}
	gsrv := httptest.NewServer(good)
	defer gsrv.Close()
	f := setupWith(t, func(c *Config) { c.ProxyPool = "global" })
	ctx := context.Background()
	pool := &store.ProxyPool{Name: "global", FailClosed: false, Members: []store.ProxyMember{{ID: "m", URL: gsrv.URL, Enabled: true}}}
	tenant := &store.Provider{OrgID: f.org.ID, Name: "t", Type: "openai", BaseURL: "https://8.8.8.8", Enabled: true}
	if err := f.g.S.PutProxyPool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := f.g.S.PutProvider(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	fw := f.g.pools
	cfg, err := (&account{pools: fw}).proxyConfig(store.Provider{ID: tenant.ID, OrgID: f.org.ID})
	if err != nil || cfg == nil || cfg.URL.GetValue() != "http://"+fw.addr || cfg.Username.GetValue() != tenant.ID {
		t.Fatalf("tenant provider not on the global pool: %+v %v", cfg, err)
	}
	if cfg, _ := (&account{pools: fw}).proxyConfig(store.Provider{NoProxy: true}); cfg != nil {
		t.Fatal("no_proxy must opt out of the global pool")
	}

	upstream := deadAddr(t) // loopback: private for tenants
	if code := connectVia(t, fw.addr, tenant.ID, "wrong-token", upstream); code != http.StatusProxyAuthRequired {
		t.Fatalf("wrong token: %d", code)
	}
	if resp, err := http.Get("http://" + fw.addr + "/"); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("non-CONNECT request: %v %v", resp, err)
	}
	old := lookupProxyIP
	lookupProxyIP = func(host string) ([]net.IP, error) {
		return map[string][]net.IP{"rebind.example": {net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}}[host], nil
	}
	defer func() { lookupProxyIP = old }()
	for _, target := range []string{upstream, "169.254.169.254:80", "rebind.example:443", "nxdomain.example:443"} {
		if code := connectVia(t, fw.addr, tenant.ID, fw.token, target); code != http.StatusForbidden {
			t.Fatalf("tenant CONNECT to %s: %d, want 403", target, code)
		}
	}
	if targets, _ := good.seen(); len(targets) != 0 {
		t.Fatalf("private targets reached a member: %v", targets)
	}
	// the operator's providers may reach private hosts through the pool
	var opID string
	for _, p := range f.g.Snapshot().Providers {
		if p.Name == "ok" {
			opID = p.ID
		}
	}
	okAddr := strings.TrimPrefix(f.g.Snapshot().Providers[opID].BaseURL, "http://")
	if code := connectVia(t, fw.addr, opID, fw.token, okAddr); code != http.StatusOK {
		t.Fatalf("operator CONNECT: %d", code)
	}
	if code := connectVia(t, fw.addr, "nope", fw.token, okAddr); code != http.StatusBadGateway {
		t.Fatalf("unknown provider: %d", code)
	}
}
