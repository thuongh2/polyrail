package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

func TestProviderProxy(t *testing.T) {
	f := setup(t, Config{})
	const pw = "proxy-TOPSECRET-pw"
	body := func(proxy string) string {
		return `{"name":"pub","type":"openai","base_url":"http://127.0.0.1:9","enabled":true,"proxy":` + proxy + `}`
	}
	for _, bad := range []string{`{"url":"ftp://h:21"}`, `{"url":"http://h:3128/x"}`, `{"url":"http://u:p@h:3128"}`,
		`{"url":"http://h:3128","password":"p"}`, `{"url":"http://h:3128","username":"u"}`, `{"url":"http://h:3128","ca_cert_pem":"junk"}`} {
		expect(t, f.do(t, "op", "POST", "/api/admin/providers", body(bad)), 400, "proxy "+bad)
	}
	r := f.do(t, "op", "POST", "/api/admin/providers", body(`{"url":"http://10.0.0.5:3128","username":"alice","password":"`+pw+`"}`))
	expect(t, r, 200, "operator private proxy")
	var p store.Provider
	r.json(t, &p)
	if p.Proxy == nil || p.Proxy.URL != "http://10.0.0.5:3128" || p.Proxy.Username != "alice" {
		t.Fatalf("proxy not returned: %s", r.body)
	}
	keep := f.do(t, "op", "PUT", "/api/admin/providers/"+p.ID, body(`{"url":"http://10.0.0.5:3128","username":"alice"}`))
	for _, x := range []resp{r, keep, f.do(t, "op", "GET", "/api/admin/providers", "")} {
		expect(t, x, 200, "provider call")
		if strings.Contains(x.body, pw) || strings.Contains(x.body, "sealed") || strings.Contains(x.body, "password") {
			t.Fatalf("proxy password leaked: %s", x.body)
		}
	}
	raw, _ := f.s.KV.Get(context.Background(), "prov/"+p.ID)
	if strings.Contains(string(raw), pw) || !strings.Contains(string(raw), "sealed_password") {
		t.Fatalf("proxy password not sealed at rest: %s", raw)
	}
	all, _ := f.s.ListProviders(context.Background(), true)
	for _, x := range all {
		if x.ID == p.ID && (x.Proxy == nil || x.Proxy.Password != pw) {
			t.Fatalf("kept proxy password lost: %+v", x.Proxy)
		}
	}
	expect(t, f.do(t, "op", "PUT", "/api/admin/providers/"+p.ID, body(`{"url":"http://10.9.9.9:3128","username":"alice"}`)), 400,
		"new proxy host without re-entering the password")

	// tenants: refused unless ALLOW_TENANT_PROXY, and then only on a public host
	A := "/api/orgs/" + f.orgA.ID + "/providers"
	tenant := func(proxy string) string {
		return `{"name":"t","type":"openai","base_url":"https://8.8.8.8","enabled":true,"proxy":` + proxy + `}`
	}
	expect(t, f.do(t, "admin", "POST", A, tenant(`{"url":"http://8.8.8.8:3128"}`)), 400, "tenant proxy while not allowed")
	expect(t, f.do(t, "admin", "POST", A, `{"name":"t","type":"openai","base_url":"https://8.8.8.8","enabled":true,"no_proxy":true}`), 200, "tenant opt-out of the global proxy")
	g := setup(t, Config{AllowTenantProxy: true})
	GA := "/api/orgs/" + g.orgA.ID + "/providers"
	for _, u := range []string{"http://127.0.0.1:3128", "socks5://10.0.0.5:1080", "http://169.254.169.254", "http://localhost:3128"} {
		expect(t, g.do(t, "admin", "POST", GA, tenant(`{"url":"`+u+`"}`)), 400, "tenant private proxy "+u)
	}
	expect(t, g.do(t, "admin", "POST", GA, tenant(`{"url":"socks5://8.8.8.8:1080"}`)), 200, "tenant public proxy")
	var cfg map[string]any
	g.do(t, "", "GET", "/api/auth/config", "").json(t, &cfg)
	if cfg["tenant_proxy"] != true {
		t.Fatalf("auth config: %v", cfg)
	}
}
