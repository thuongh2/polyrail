package gateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Proxy pools (D23). Bifrost takes one static proxy per provider, so a provider that uses a pool
// is pointed at a CONNECT forwarder on 127.0.0.1 inside this process. The forwarder knows the
// provider from the proxy username, checks the per-process password, picks a pool member for each
// tunnel and dials the target through it. Rotation is per new upstream connection: Bifrost keeps
// connections alive, so many requests share one member until the connection closes.
//
// The forwarder only ever sees CONNECT tunnels (TLS to the provider), never HTTP responses, so a
// provider's 429 or 5xx cannot move the selection: only failures to reach a proxy do.

const (
	setAsideFirst     = 60 * time.Second  // first cooldown of a member that failed a dial
	setAsideMax       = 600 * time.Second // cooldown doubles up to this
	memberDialTimeout = 5 * time.Second   // dial + CONNECT/SOCKS handshake per member
	maxMemberTries    = 3                 // members tried per tunnel (Bifrost allows 30 s for it)
	downAfterFails    = 3                 // consecutive failures that mark a member down
	probeTimeout      = 2 * time.Second   // TCP probe of the health checker
	exhaustedRebound  = 300 * time.Second // "pool exhausted" is logged at most this often
)

var (
	mProxyDials = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gw_proxy_dials_total", Help: "Proxy pool tunnel dials by pool and result (ok, fail, refused, direct).",
	}, []string{"pool", "result"})
	mProxyUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gw_proxy_member_up", Help: "1 when a proxy pool member is usable (not down, not set aside) on this node.",
	}, []string{"pool", "member"})
)

func init() { prometheus.MustRegister(mProxyDials, mProxyUp) }

// memberState is this node's view of one pool member.
type memberState struct {
	fails      int           // consecutive failures (dials and probes)
	down       bool          // fails reached downAfterFails; a good probe clears it
	asideUntil time.Time     // set aside after a failed dial until then
	cooldown   time.Duration // last set-aside length (doubles)
	ewmaMs     float64       // connect latency, 0 = not measured yet
}

type stickyPick struct {
	member string
	until  time.Time
}

// rotator holds pool selection state: per-member health, round-robin cursors and sticky picks.
// shortcut: in memory per node (no shared cursor); fine for egress spreading, share via Redis
// only if exact cluster-wide rotation is ever needed.
type rotator struct {
	mu        sync.Mutex
	members   map[string]*memberState // pool id + "/" + member id
	cursor    map[string]int
	sticky    map[string]stickyPick
	exhausted map[string]time.Time
	now       func() time.Time
	perm      func(n int) []int
}

func newRotator() *rotator {
	return &rotator{members: map[string]*memberState{}, cursor: map[string]int{}, sticky: map[string]stickyPick{},
		exhausted: map[string]time.Time{}, now: time.Now, perm: mrand.Perm}
}

func (r *rotator) state(pool, member string) *memberState {
	k := pool + "/" + member
	s := r.members[k]
	if s == nil {
		s = &memberState{}
		r.members[k] = s
	}
	return s
}

// order returns the usable members of p (enabled, not down, not set aside) in the order to try.
func (r *rotator) order(p store.ProxyPool) []store.ProxyMember {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var c []store.ProxyMember
	for _, m := range p.Members {
		if s := r.state(p.ID, m.ID); m.Enabled && !s.down && !now.Before(s.asideUntil) {
			c = append(c, m)
		}
	}
	if len(c) < 2 {
		return c
	}
	switch p.Strategy {
	case "random":
		out := make([]store.ProxyMember, len(c))
		for i, j := range r.perm(len(c)) {
			out[i] = c[j]
		}
		return out
	case "latency": // unmeasured members first, so each gets measured
		sort.SliceStable(c, func(i, j int) bool { return r.state(p.ID, c[i].ID).ewmaMs < r.state(p.ID, c[j].ID).ewmaMs })
		return c
	case "sticky":
		if s, ok := r.sticky[p.ID]; ok && now.Before(s.until) {
			for i, m := range c {
				if m.ID == s.member {
					return append(append([]store.ProxyMember{m}, c[:i]...), c[i+1:]...)
				}
			}
		}
	}
	n := r.cursor[p.ID] % len(c) // round-robin, and a sticky pool choosing its next member
	r.cursor[p.ID]++
	return append(c[n:len(c):len(c)], c[:n]...)
}

// succeeded records a tunnel opened through m in d.
func (r *rotator) succeeded(p store.ProxyPool, m store.ProxyMember, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state(p.ID, m.ID)
	s.fails, s.cooldown = 0, 0
	s.observe(d)
	if s.down {
		s.down = false
		slog.Info("proxy member up", "pool", p.Name, "member", memberName(m))
	}
	now := r.now()
	if p.Strategy == "sticky" {
		if cur, ok := r.sticky[p.ID]; !ok || cur.member != m.ID || !now.Before(cur.until) {
			r.sticky[p.ID] = stickyPick{m.ID, now.Add(time.Duration(p.StickyMinutes) * time.Minute)}
		}
	}
	mProxyUp.WithLabelValues(p.Name, memberName(m)).Set(1)
}

// failed sets m aside after a failed dial: 60 s, doubling per consecutive failure, at most 600 s.
func (r *rotator) failed(p store.ProxyPool, m store.ProxyMember, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state(p.ID, m.ID)
	s.cooldown = min(max(s.cooldown*2, setAsideFirst), setAsideMax)
	s.asideUntil = r.now().Add(s.cooldown)
	s.fails++
	if s.fails >= downAfterFails && !s.down {
		s.down = true
		slog.Warn("proxy member down", "pool", p.Name, "member", memberName(m), "fails", s.fails)
	}
	slog.Warn("proxy member set aside", "pool", p.Name, "member", memberName(m), "cooldown", s.cooldown.String(), "err", err)
	mProxyUp.WithLabelValues(p.Name, memberName(m)).Set(0)
}

// probed records a health probe: a failure counts toward down, a success brings a down member back.
func (r *rotator) probed(p store.ProxyPool, m store.ProxyMember, d time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.state(p.ID, m.ID)
	if err != nil {
		s.fails++
		if s.fails >= downAfterFails && !s.down {
			s.down = true
			slog.Warn("proxy member down", "pool", p.Name, "member", memberName(m), "fails", s.fails)
		}
	} else {
		s.fails = 0
		s.observe(d)
		if s.down {
			s.down = false
			slog.Info("proxy member up", "pool", p.Name, "member", memberName(m))
		}
	}
	up := 0.0
	if !s.down && !r.now().Before(s.asideUntil) {
		up = 1
	}
	mProxyUp.WithLabelValues(p.Name, memberName(m)).Set(up)
}

// noteExhausted logs that p had no usable member, at most once per exhaustedRebound.
func (r *rotator) noteExhausted(p store.ProxyPool, failClosed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.now(); now.After(r.exhausted[p.ID]) {
		r.exhausted[p.ID] = now.Add(exhaustedRebound)
		slog.Error("proxy pool exhausted", "pool", p.Name, "fail_closed", failClosed)
	}
}

func (s *memberState) observe(d time.Duration) {
	ms := float64(d) / float64(time.Millisecond)
	if s.ewmaMs == 0 {
		s.ewmaMs = math.Max(ms, 0.001)
	} else {
		s.ewmaMs = 0.7*s.ewmaMs + 0.3*ms
	}
}

func memberName(m store.ProxyMember) string {
	if m.Label != "" {
		return m.Label
	}
	return m.ID
}

// MemberHealth is this node's view of a pool member, for the admin API.
type MemberHealth struct {
	State         string     `json:"state"` // up, down, set_aside, unknown (never used or probed)
	LatencyMs     int64      `json:"latency_ms,omitempty"`
	SetAsideUntil *time.Time `json:"set_aside_until,omitempty"`
}

func (r *rotator) health(p store.ProxyPool) map[string]MemberHealth {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]MemberHealth{}
	now := r.now()
	for _, m := range p.Members {
		s, ok := r.members[p.ID+"/"+m.ID]
		h := MemberHealth{State: "unknown"}
		switch {
		case !ok:
		case s.down:
			h.State = "down"
		case now.Before(s.asideUntil):
			t := s.asideUntil
			h.State, h.SetAsideUntil = "set_aside", &t
		case s.ewmaMs > 0:
			h.State = "up"
		}
		if ok {
			h.LatencyMs = int64(math.Round(s.ewmaMs))
		}
		out[m.ID] = h
	}
	return out
}

// ---- forwarder ----

// poolForwarder is the loopback CONNECT proxy that providers using a pool are configured with.
type poolForwarder struct {
	*rotator
	addr         string // 127.0.0.1:port
	token        string // proxy password Bifrost sends; anything else is refused
	snap         func() *Snapshot
	global       string // UPSTREAM_PROXY_POOL: pool id or name every provider defaults to
	allowPrivate bool
}

func newPoolForwarder(ctx context.Context, snap func() *Snapshot, global string, allowPrivate bool, probeEvery time.Duration) (*poolForwarder, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("proxy pool forwarder: %w", err)
	}
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	f := &poolForwarder{rotator: newRotator(), addr: ln.Addr().String(), token: hex.EncodeToString(b), snap: snap,
		global: global, allowPrivate: allowPrivate}
	srv := &http.Server{Handler: f, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	if probeEvery > 0 {
		go f.probeLoop(ctx, probeEvery)
	}
	return f, nil
}

// poolRef is the pool provider p uses: its own (platform providers), else the global default
// unless p opts out with no_proxy. own reports which.
func poolRef(p store.Provider, global string) (ref string, own bool) {
	if p.OrgID == "" && p.ProxyPool != "" {
		return p.ProxyPool, true
	}
	if !p.NoProxy && global != "" {
		return global, false
	}
	return "", false
}

// pool finds a pool by id, or by name (UPSTREAM_PROXY_POOL may name it).
func (s *Snapshot) pool(ref string) (store.ProxyPool, bool) {
	for _, p := range s.Pools {
		if p.ID == ref {
			return p, true
		}
	}
	for _, p := range s.Pools {
		if p.Name == ref {
			return p, true
		}
	}
	return store.ProxyPool{}, false
}

// config is the Bifrost proxy setting that sends provider id's tunnels to the forwarder.
func (f *poolForwarder) config(id, noProxy string) *schemas.ProxyConfig {
	return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: secretPtr("http://" + f.addr), Username: secretPtr(id),
		Password: secretPtr(f.token), NoProxy: noProxy}
}

func (f *poolForwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		// Bifrost's fasthttp clients tunnel http:// targets too; only net/http clients would send
		// absolute-form requests, and those reach https endpoints (docs/DEPLOY.md).
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	id, ok := f.authorized(r.Header.Get("Proxy-Authorization"))
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="gateway"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	snap := f.snap()
	prov, ok := snap.Providers[id]
	ref, _ := poolRef(prov, f.global)
	pool, found := snap.pool(ref)
	if !ok || !found {
		slog.Error("proxy pool not found", "provider", id, "pool", ref)
		http.Error(w, "proxy pool not found", http.StatusBadGateway)
		return
	}
	target := r.Host
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || port == "" {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}
	public := prov.OrgID != "" && !f.allowPrivate
	if public {
		// the SSRF guard cannot see through a proxy: check the target here and tunnel to the
		// checked address, so neither the member nor DNS can move it to an internal host
		if target, err = pinTarget(host, port); err != nil {
			http.Error(w, "target is not publicly routable", http.StatusForbidden)
			return
		}
	}
	up, err := f.dial(r.Context(), pool, target, public)
	if err != nil {
		http.Error(w, "no proxy available", http.StatusBadGateway)
		return
	}
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		up.Close()
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		conn.Close()
		up.Close()
		return
	}
	go func() { _, _ = io.Copy(up, buf); closeWrite(up) }()
	go func() { _, _ = io.Copy(conn, up); conn.Close(); up.Close() }()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// authorized checks Basic credentials "<provider id>:<token>" and returns the provider id.
func (f *poolForwarder) authorized(h string) (string, bool) {
	raw, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	id, pw, ok := strings.Cut(string(b), ":")
	return id, ok && id != "" && subtle.ConstantTimeCompare([]byte(pw), []byte(f.token)) == 1
}

// pinTarget resolves a tenant's CONNECT target and returns ip:port when every address is public.
func pinTarget(host, port string) (string, error) {
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		var err error
		if ips, err = lookupProxyIP(host); err != nil || len(ips) == 0 {
			return "", ErrPrivateAddress
		}
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return "", ErrPrivateAddress
		}
	}
	return net.JoinHostPort(ips[0].String(), port), nil
}

var errPoolExhausted = errors.New("proxy pool exhausted")

// dial opens a tunnel to target through pool members in strategy order, setting aside members
// that fail. With every member unusable it refuses (fail closed) or connects directly; a direct
// connection for a tenant (public) still refuses non-public addresses.
func (f *poolForwarder) dial(ctx context.Context, pool store.ProxyPool, target string, public bool) (net.Conn, error) {
	for i, m := range f.order(pool) {
		if i == maxMemberTries {
			break
		}
		start := time.Now()
		conn, err := dialMember(ctx, m, target)
		if err == nil {
			f.succeeded(pool, m, time.Since(start))
			mProxyDials.WithLabelValues(pool.Name, "ok").Inc()
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err() // the caller went away; not the member's fault
		}
		f.failed(pool, m, err)
		mProxyDials.WithLabelValues(pool.Name, "fail").Inc()
	}
	f.noteExhausted(pool, pool.FailClosed)
	if pool.FailClosed {
		mProxyDials.WithLabelValues(pool.Name, "refused").Inc()
		return nil, errPoolExhausted
	}
	mProxyDials.WithLabelValues(pool.Name, "direct").Inc()
	d := &net.Dialer{Timeout: 10 * time.Second}
	if public {
		d = PublicDialer(10 * time.Second)
	}
	return d.DialContext(ctx, "tcp", target)
}

// dialMember opens a tunnel to target through one member (http/https CONNECT or SOCKS5), with
// Bifrost's own proxy dialer: bounded handshake, Basic auth, CA for an https proxy hop.
func dialMember(ctx context.Context, m store.ProxyMember, target string) (net.Conn, error) {
	u, err := url.Parse(m.URL)
	if err != nil {
		return nil, err
	}
	if m.Username != "" {
		u.User = url.UserPassword(m.Username, m.Password)
	}
	var hop *tls.Config
	if m.CACertPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(m.CACertPEM))
		hop = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	ctx, cancel := context.WithTimeout(ctx, memberDialTimeout)
	defer cancel()
	return network.DialViaProxyTLS(ctx, u, target, hop)
}

// memberAddr is the host:port a member listens on (scheme default port when none is given).
func memberAddr(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Port() != "" {
		return u.Host
	}
	port := map[string]string{"http": "80", "https": "443", "socks5": "1080"}[u.Scheme]
	return net.JoinHostPort(u.Hostname(), port)
}

// probeLoop checks every enabled member of every pool with a TCP connect each interval.
func (f *poolForwarder) probeLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		f.probeAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (f *poolForwarder) probeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range f.snap().Pools {
		for _, m := range p.Members {
			if !m.Enabled {
				continue
			}
			wg.Go(func() {
				start := time.Now()
				c, err := (&net.Dialer{Timeout: probeTimeout}).DialContext(ctx, "tcp", memberAddr(m.URL))
				if err == nil {
					c.Close()
				}
				if ctx.Err() == nil {
					f.probed(p, m, time.Since(start), err)
				}
			})
		}
	}
	wg.Wait()
}

// ProxyTestResult is one member's answer to a pool test (admin API); no response bodies.
type ProxyTestResult struct {
	MemberID  string `json:"member_id"`
	Label     string `json:"label,omitempty"`
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// TestProxyPool opens a tunnel to target through each member of p (decrypted) and closes it,
// recording the outcome as a health probe.
func (g *Gateway) TestProxyPool(ctx context.Context, p store.ProxyPool, target string) []ProxyTestResult {
	out := make([]ProxyTestResult, len(p.Members))
	var wg sync.WaitGroup
	for i, m := range p.Members {
		wg.Go(func() {
			start := time.Now()
			conn, err := dialMember(ctx, m, target)
			d := time.Since(start)
			r := ProxyTestResult{MemberID: m.ID, Label: m.Label, OK: err == nil, LatencyMs: d.Milliseconds()}
			if err != nil {
				r.Error = err.Error()
				if len(r.Error) > 200 {
					r.Error = r.Error[:200]
				}
			} else {
				conn.Close()
			}
			g.pools.probed(p, m, d, err)
			out[i] = r
		})
	}
	wg.Wait()
	return out
}

// ProxyHealth is this node's view of each member of pool p, by member id.
func (g *Gateway) ProxyHealth(p store.ProxyPool) map[string]MemberHealth { return g.pools.health(p) }
