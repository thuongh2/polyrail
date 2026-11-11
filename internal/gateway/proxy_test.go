package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestProxyConfigPrecedence(t *testing.T) {
	global := &GlobalProxy{ProviderProxy: store.ProviderProxy{URL: "http://corp:3128", Username: "u", Password: "env.HOME"}, NoProxy: ".internal"}
	own := &store.ProviderProxy{URL: "socks5://own:1080", Username: "o", Password: "env.PATH", CACertPEM: "pem"}
	a := &account{proxy: global}
	cases := []struct {
		name string
		p    store.Provider
		want string // URL, "" for none
	}{
		{"global default", store.Provider{}, "http://corp:3128"},
		{"own proxy wins", store.Provider{Proxy: own}, "socks5://own:1080"},
		{"opt out", store.Provider{NoProxy: true}, ""},
		{"own proxy beats opt-out", store.Provider{Proxy: own, NoProxy: true}, "socks5://own:1080"},
		{"tenant proxy ignored unless allowed", store.Provider{OrgID: "o1", Proxy: own}, "http://corp:3128"},
	}
	for _, c := range cases {
		got, err := a.proxyConfig(c.p)
		if err != nil || (got == nil) != (c.want == "") || (got != nil && got.URL.GetValue() != c.want) {
			t.Fatalf("%s: %+v", c.name, got)
		}
	}
	if cfg, _ := (&account{}).proxyConfig(store.Provider{}); cfg != nil {
		t.Fatal("no global proxy and no own proxy must mean direct")
	}
	g, _ := a.proxyConfig(store.Provider{})
	if g.Type != schemas.HTTPProxy || g.NoProxy != ".internal" || g.Username.GetValue() != "u" || g.Password.GetValue() != "env.HOME" || g.CACertPEM != nil {
		t.Fatalf("global proxy config: %+v (secrets must stay literal, never resolve env.*)", g)
	}
	o, _ := (&account{proxy: global, tenantProxy: true, allowPrivate: true}).proxyConfig(store.Provider{OrgID: "o1", Proxy: own})
	if o.Type != schemas.Socks5Proxy || o.NoProxy != "" || o.Password.GetValue() != "env.PATH" || o.CACertPEM.GetValue() != "pem" {
		t.Fatalf("own proxy config: %+v", o)
	}
}

func TestCheckProxyAndParseGlobal(t *testing.T) {
	for _, u := range []string{"", "ftp://h:21", "http://", "http://h:3128/path", "http://h?q=1", "http://u:p@h:3128", "http://h:99999", "corp:3128"} {
		if CheckProxy(&store.ProviderProxy{URL: u}, false) == nil {
			t.Fatalf("accepted proxy url %q", u)
		}
	}
	for _, u := range []string{"http://h:3128", "https://h", "socks5://10.0.0.1:1080", "http://h:3128/"} {
		if err := CheckProxy(&store.ProviderProxy{URL: u}, false); err != nil {
			t.Fatalf("rejected %q: %v", u, err)
		}
	}
	if CheckProxy(&store.ProviderProxy{URL: "http://h:1", Password: "p"}, false) == nil ||
		CheckProxy(&store.ProviderProxy{URL: "http://h:1", Username: "u"}, false) == nil ||
		CheckProxy(&store.ProviderProxy{URL: "http://h:1", Username: "u"}, true) != nil {
		t.Fatal("username/password pairing")
	}
	if CheckProxy(&store.ProviderProxy{URL: "http://h:1", CACertPEM: "not a cert"}, false) == nil {
		t.Fatal("accepted a bad CA")
	}
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	ca := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(srv.Certificate().Raw) + "\n-----END CERTIFICATE-----\n"
	if err := CheckProxy(&store.ProviderProxy{URL: "http://h:1", CACertPEM: ca}, false); err != nil {
		t.Fatalf("rejected a valid CA: %v", err)
	}

	g, err := ParseGlobalProxy("http://alice:s3cret@corp:3128", "localhost,.lan", "")
	if err != nil || g.URL != "http://corp:3128" || g.Username != "alice" || g.Password != "s3cret" || g.NoProxy != "localhost,.lan" {
		t.Fatalf("global proxy: %+v %v", g, err)
	}
	if g, err := ParseGlobalProxy("", "", ""); g != nil || err != nil {
		t.Fatal("empty UPSTREAM_PROXY_URL must mean no proxy")
	}
	if _, err := ParseGlobalProxy("socks4://corp:1080", "", ""); err == nil {
		t.Fatal("accepted socks4")
	}
	if CheckPublicProxy("socks5://127.0.0.1:1080") == nil || CheckPublicProxy("http://10.1.2.3:3128") == nil || CheckPublicProxy("http://8.8.8.8:3128") != nil {
		t.Fatal("public proxy check")
	}
}

// connectProxy is a minimal HTTP CONNECT proxy that records the tunnels it opens and the
// Proxy-Authorization it was given.
type connectProxy struct {
	mu      sync.Mutex
	targets []string
	auth    []string
}

func (p *connectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	p.mu.Lock()
	p.targets = append(p.targets, r.Host)
	p.auth = append(p.auth, r.Header.Get("Proxy-Authorization"))
	p.mu.Unlock()
	up, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() { _, _ = io.Copy(up, buf); up.Close() }()
	go func() { _, _ = io.Copy(conn, up); conn.Close() }()
}

func (p *connectProxy) seen() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...), append([]string(nil), p.auth...)
}

func TestChatGoesThroughProviderProxy(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	px := &connectProxy{}
	psrv := httptest.NewServer(px)
	defer psrv.Close()

	var prov store.Provider
	for _, p := range f.g.Snapshot().Providers {
		if p.Name == "ok" {
			prov = p
		}
	}
	upstream := strings.TrimPrefix(prov.BaseURL, "http://")
	stored, err := f.g.S.GetProvider(ctx, prov.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Keys[0].Value = "k2"
	stored.Proxy = &store.ProviderProxy{URL: psrv.URL, Username: "alice", Password: "pw"}
	if err := f.g.S.PutProvider(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	okBody := `{"model":"test/ok","messages":[{"role":"user","content":"hi"}]}`
	resp := f.post(t, "/v1/chat/completions", f.key, okBody)
	if m := readJSON(t, resp); resp.StatusCode != 200 {
		t.Fatalf("chat through proxy: %d %v", resp.StatusCode, m)
	}
	targets, auth := px.seen()
	if len(targets) == 0 || targets[0] != upstream || auth[0] != "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:pw")) {
		t.Fatalf("proxy saw %v %v, want a tunnel to %s with credentials", targets, auth, upstream)
	}

	// removing the proxy restarts the provider: the next call goes direct
	stored.Proxy = nil
	stored.Keys[0].Value = "k2"
	if err := f.g.S.PutProvider(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(targets)
	hits := f.okHits.Load()
	resp = f.post(t, "/v1/chat/completions", f.key, okBody)
	if m := readJSON(t, resp); resp.StatusCode != 200 {
		t.Fatalf("direct chat: %d %v", resp.StatusCode, m)
	}
	if targets, _ = px.seen(); len(targets) != before || f.okHits.Load() != hits+1 {
		t.Fatalf("call still went through the proxy: %v", targets)
	}
}

// A tenant proxy hop is dialed by Bifrost without the private-address check, so the vetted
// address is pinned into the config: a proxy host that rebinds to 127.0.0.1 after the check
// cannot become the hop (M12).
func TestTenantProxyPinnedToPublicIP(t *testing.T) {
	answers := map[string][]net.IP{
		"px.example":     {net.ParseIP("8.8.8.8")},
		"rebind.example": {net.ParseIP("8.8.4.4"), net.ParseIP("127.0.0.1")},
		"v6.example":     {net.ParseIP("2001:4860:4860::8888")},
	}
	old := lookupProxyIP
	lookupProxyIP = func(host string) ([]net.IP, error) {
		if ips, ok := answers[host]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}
	defer func() { lookupProxyIP = old }()
	for raw, want := range map[string]string{
		"http://px.example:3128":   "http://8.8.8.8:3128",
		"socks5://px.example":      "socks5://8.8.8.8",
		"socks5://v6.example:1080": "socks5://[2001:4860:4860::8888]:1080",
		"https://px.example:443":   "https://px.example:443", // TLS to the proxy names its host: keep it
		"http://1.1.1.1:8080":      "http://1.1.1.1:8080",
	} {
		got, err := pinProxyURL(raw)
		if err != nil || got != want {
			t.Fatalf("pin %s = %q %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"http://rebind.example:3128", "socks5://127.0.0.1:1080", "http://nxdomain.example", "https://rebind.example"} {
		if got, err := pinProxyURL(raw); err == nil {
			t.Fatalf("pinned %s to %q", raw, got)
		}
	}
	a := &account{tenantProxy: true}
	if cfg, err := a.proxyConfig(store.Provider{OrgID: "o1", Proxy: &store.ProviderProxy{URL: "http://rebind.example:3128"}}); err == nil {
		t.Fatalf("tenant proxy on a rebinding host configured: %+v", cfg)
	}
	cfg, err := a.proxyConfig(store.Provider{OrgID: "o1", Proxy: &store.ProviderProxy{URL: "http://px.example:3128"}})
	if err != nil || cfg.URL.GetValue() != "http://8.8.8.8:3128" {
		t.Fatalf("tenant proxy not pinned: %+v %v", cfg, err)
	}
	// operators and AllowPrivateURLs keep the host name
	if cfg, err := a.proxyConfig(store.Provider{Proxy: &store.ProviderProxy{URL: "http://corp.internal:3128"}}); err != nil || cfg.URL.GetValue() != "http://corp.internal:3128" {
		t.Fatalf("operator proxy: %+v %v", cfg, err)
	}
	if cfg, err := (&account{tenantProxy: true, allowPrivate: true}).proxyConfig(store.Provider{OrgID: "o1", Proxy: &store.ProviderProxy{URL: "http://rebind.example:3128"}}); err != nil || cfg.URL.GetValue() != "http://rebind.example:3128" {
		t.Fatalf("private URLs allowed: %+v %v", cfg, err)
	}
}
