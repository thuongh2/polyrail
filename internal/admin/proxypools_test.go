package admin

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

func TestProxyPoolsAdmin(t *testing.T) {
	f := setup(t, Config{AllowTenantProxy: true})
	const pw = "member-TOPSECRET-pw"
	P := "/api/admin/proxy-pools"
	body := `{"name":"egress","strategy":"sticky","sticky_minutes":15,"members":[` +
		`{"url":"http://10.0.0.5:3128","username":"alice","password":"` + pw + `","label":"dc-1","enabled":true},` +
		`{"url":"socks5://10.0.0.6:1080","enabled":true}]}`

	for _, who := range []string{"", "owner", "admin", "member"} {
		if r := f.do(t, who, "GET", P, ""); r.code != 401 && r.code != 403 {
			t.Fatalf("%q listed pools: %d", who, r.code)
		}
		if r := f.do(t, who, "POST", P, body); r.code != 401 && r.code != 403 {
			t.Fatalf("%q created a pool: %d", who, r.code)
		}
	}
	expect(t, f.req(t, "op", "POST", P, body, false), 403, "missing CSRF header")
	for _, bad := range []string{
		`{"name":"x","members":[]}`,
		`{"name":"x","strategy":"fastest","members":[{"url":"http://h:1","enabled":true}]}`,
		`{"name":"x","sticky_minutes":5000,"members":[{"url":"http://h:1","enabled":true}]}`,
		`{"name":"x","members":[{"url":"ftp://h:1","enabled":true}]}`,
		`{"name":"x","members":[{"url":"http://u:p@h:1","enabled":true}]}`,
		`{"name":"x","members":[{"url":"http://h:1","username":"u","enabled":true}]}`,
		`{"name":"x","members":[{"url":"http://h:1","enabled":true}],"extra":1}`,
	} {
		expect(t, f.do(t, "op", "POST", P, bad), 400, "pool "+bad)
	}
	r := f.do(t, "op", "POST", P, body)
	expect(t, r, 200, "create pool")
	var pool struct {
		store.ProxyPool
		Members []struct {
			store.ProxyMember
			Health struct{ State string } `json:"health"`
		} `json:"members"`
	}
	r.json(t, &pool)
	if !pool.FailClosed || pool.Strategy != "sticky" || pool.StickyMinutes != 15 || len(pool.Members) != 2 || pool.Members[0].Health.State != "unknown" {
		t.Fatalf("pool: %s", r.body)
	}
	expect(t, f.do(t, "op", "POST", P, strings.Replace(body, "egress", "EGRESS", 1)), 400, "duplicate name")

	// keep the password by sending the member id without one
	m0 := pool.Members[0].ID
	keep := `{"name":"egress","fail_closed":false,"members":[{"id":"` + m0 + `","url":"http://10.0.0.5:3128","username":"alice","enabled":true}]}`
	rk := f.do(t, "op", "PUT", P+"/"+pool.ID, keep)
	expect(t, rk, 200, "update keeping password")
	expect(t, f.do(t, "op", "PUT", P+"/"+pool.ID, strings.Replace(keep, "10.0.0.5", "10.0.0.9", 1)), 400, "new URL without password")
	list := f.do(t, "op", "GET", P, "")
	for _, x := range []resp{r, rk, list} {
		if strings.Contains(x.body, pw) || strings.Contains(x.body, "sealed") || strings.Contains(x.body, "password") {
			t.Fatalf("member password leaked: %s", x.body)
		}
	}
	stored, _ := f.s.GetProxyPool(context.Background(), pool.ID, true)
	if stored.FailClosed || len(stored.Members) != 1 || stored.Members[0].Password != pw {
		t.Fatalf("stored pool: %+v", stored)
	}
	raw, _ := f.s.KV.Get(context.Background(), "proxypool/"+pool.ID)
	if strings.Contains(string(raw), pw) {
		t.Fatal("member password stored in plaintext")
	}

	// providers: platform only, exclusive with proxy, pool must exist; delete blocked while used
	prov := func(extra string) string {
		return `{"name":"pub","type":"openai","base_url":"http://127.0.0.1:9","enabled":true` + extra + `}`
	}
	expect(t, f.do(t, "op", "POST", "/api/admin/providers", prov(`,"proxy_pool":"nope"`)), 400, "unknown pool")
	expect(t, f.do(t, "op", "POST", "/api/admin/providers", prov(`,"proxy_pool":"`+pool.ID+`","proxy":{"url":"http://h:1"}`)), 400, "pool and proxy")
	expect(t, f.do(t, "admin", "POST", "/api/orgs/"+f.orgA.ID+"/providers",
		`{"name":"t","type":"openai","base_url":"https://8.8.8.8","enabled":true,"proxy_pool":"`+pool.ID+`"}`), 400, "tenant pool")
	rp := f.do(t, "op", "POST", "/api/admin/providers", prov(`,"proxy_pool":"`+pool.ID+`"`))
	expect(t, rp, 200, "provider with pool")
	var p store.Provider
	rp.json(t, &p)
	if p.ProxyPool != pool.ID {
		t.Fatalf("proxy_pool not returned: %s", rp.body)
	}
	expect(t, f.do(t, "op", "DELETE", P+"/"+pool.ID, ""), 400, "delete a pool in use")

	// test: one dead member; results carry no target bodies
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close()
	expect(t, f.do(t, "op", "PUT", P+"/"+pool.ID, `{"name":"egress","members":[{"url":"http://`+dead+`","label":"gone","enabled":true}]}`), 200, "point at a dead member")
	expect(t, f.do(t, "op", "POST", P+"/"+pool.ID+"/test", `{"target":"nope"}`), 400, "bad test target")
	rt := f.do(t, "op", "POST", P+"/"+pool.ID+"/test", `{"target":"example.com:443"}`)
	expect(t, rt, 200, "test pool")
	var tr struct {
		Results []struct {
			Label string
			OK    bool
			Error string
		}
	}
	rt.json(t, &tr)
	if len(tr.Results) != 1 || tr.Results[0].OK || tr.Results[0].Label != "gone" || tr.Results[0].Error == "" {
		t.Fatalf("test results: %s", rt.body)
	}
	expect(t, f.do(t, "admin", "POST", P+"/"+pool.ID+"/test", ""), 403, "org admin tests a pool")

	p.ProxyPool = ""
	expect(t, f.do(t, "op", "PUT", "/api/admin/providers/"+p.ID, prov("")), 200, "drop the pool")
	expect(t, f.do(t, "op", "DELETE", P+"/"+pool.ID, ""), 204, "delete unused pool")
	audit := f.do(t, "op", "GET", "/api/admin/audit", "")
	for _, act := range []string{"proxy_pool.put", "proxy_pool.test", "proxy_pool.delete"} {
		if !strings.Contains(audit.body, act) {
			t.Fatalf("audit lacks %s", act)
		}
	}
}
