package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/gateway"
	"github.com/agent-gateway/agent-gateway/internal/limits"
	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
	"golang.org/x/crypto/bcrypt"
)

const testPassword = "correct-horse-battery"

// fixture: org A has one user per role, org B is owned by "outsider", "op" is the operator.
type fixture struct {
	a     *API
	srv   *httptest.Server
	s     *store.Store
	g     *gateway.Gateway
	orgA  *store.Org
	orgB  *store.Org
	users map[string]*store.User
}

func setup(t *testing.T, cfg Config) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	box, err := store.NewBox("test-master-key-123456")
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(kv, box)
	up := usage.New(kv, 100, 20*time.Millisecond, usage.StoreSink{S: s})
	t.Cleanup(up.Close)
	g, err := gateway.New(ctx, s, limits.NewLocal(), up, gateway.Config{MaxBodyBytes: 1 << 20, Concurrency: 2, Buffer: 8, CacheMB: 1, PollInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.BF.Shutdown)
	cfg.MasterKey = "test-master-key-123456"
	a, err := New(ctx, s, g, limits.NewLocal(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.Register(mux)
	mux.HandleFunc("GET /v1/models", g.Models)
	f := &fixture{a: a, s: s, g: g, srv: httptest.NewServer(mux), users: map[string]*store.User{}}
	t.Cleanup(f.srv.Close)

	// MinCost keeps the suite fast; login compares any bcrypt cost.
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	for _, n := range []string{"op", "owner", "admin", "member", "member2", "viewer", "outsider"} {
		u := &store.User{Email: n + "@example.com", Name: n, PasswordHash: string(hash), Operator: n == "op"}
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		f.users[n] = u
	}
	f.orgA = &store.Org{Name: "A"}
	f.orgB = &store.Org{Name: "B"}
	if err := s.CreateOrg(ctx, f.orgA, f.users["owner"].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrg(ctx, f.orgB, f.users["outsider"].ID); err != nil {
		t.Fatal(err)
	}
	for n, r := range map[string]store.Role{"admin": store.RoleAdmin, "member": store.RoleMember, "member2": store.RoleMember, "viewer": store.RoleViewer} {
		if err := s.SetMember(ctx, store.Member{OrgID: f.orgA.ID, UserID: f.users[n].ID, Role: r}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// cookie mints a session cookie for a fixture user (login itself is covered by TestLogin).
func (f *fixture) cookie(name string) *http.Cookie {
	u := f.users[name]
	return &http.Cookie{Name: sessionCookie, Value: f.a.sign.sign("sess", time.Hour, u.ID, sessionEpoch(u))}
}

type resp struct {
	code int
	body string
	hdr  http.Header
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("not json (%d): %s", r.code, r.body)
	}
}

// do sends a request as user `as` ("" = anonymous) with the CSRF header set.
func (f *fixture) do(t *testing.T, as, method, path, body string) resp {
	t.Helper()
	return f.req(t, as, method, path, body, true)
}

func (f *fixture) req(t *testing.T, as, method, path, body string, csrf bool) resp {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		req.Header.Set("X-GW-CSRF", "1")
	}
	if as != "" {
		req.AddCookie(f.cookie(as))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, string(raw), res.Header}
}

func expect(t *testing.T, r resp, code int, what string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: status %d, want %d: %s", what, r.code, code, r.body)
	}
}

func (f *fixture) createKey(t *testing.T, as, org string) (id, secret string) {
	t.Helper()
	r := f.do(t, as, "POST", "/api/orgs/"+org+"/keys", `{"name":"k-`+as+`"}`)
	expect(t, r, 201, as+" create key")
	var out struct {
		Key    store.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	r.json(t, &out)
	return out.Key.ID, out.Secret
}

func TestLogin(t *testing.T) {
	f := setup(t, Config{})
	post := func(body string) *http.Response {
		req, _ := http.NewRequest("POST", f.srv.URL+"/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GW-CSRF", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// login CSRF: a cross-site form cannot set the header
	if res, _ := http.Post(f.srv.URL+"/api/auth/login", "text/plain", strings.NewReader(`{"email":"owner@example.com","password":"`+testPassword+`"}`)); res.StatusCode != 403 {
		t.Fatalf("login without CSRF header: %d", res.StatusCode)
	}
	for _, body := range []string{
		`{"email":"owner@example.com","password":"wrong-password-123"}`,
		`{"email":"nobody@example.com","password":"` + testPassword + `"}`,
	} {
		res := post(body)
		res.Body.Close()
		if res.StatusCode != 401 || len(res.Cookies()) != 0 {
			t.Fatalf("bad login %s: status %d cookies %v", body, res.StatusCode, res.Cookies())
		}
	}

	res := post(`{"email":"Owner@Example.com","password":"` + testPassword + `"}`)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("login: %d %s", res.StatusCode, raw)
	}
	var sess *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode || sess.Path != "/" {
		t.Fatalf("session cookie: %+v", sess)
	}
	if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "$2a$") {
		t.Fatalf("login response leaks hash: %s", raw)
	}

	// the issued cookie works for /api/auth/me, which never exposes the hash
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/auth/me", nil)
	req.AddCookie(sess)
	me, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(me.Body)
	me.Body.Close()
	if me.StatusCode != 200 || !strings.Contains(string(raw), `"owner@example.com"`) || !strings.Contains(string(raw), f.orgA.ID) {
		t.Fatalf("me: %d %s", me.StatusCode, raw)
	}
	if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "$2a$") {
		t.Fatalf("/api/auth/me leaks hash: %s", raw)
	}

	expect(t, f.do(t, "", "GET", "/api/auth/me", ""), 401, "anonymous me")
	bad := &http.Cookie{Name: sessionCookie, Value: sess.Value + "x"}
	req, _ = http.NewRequest("GET", f.srv.URL+"/api/auth/me", nil)
	req.AddCookie(bad)
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 401 {
		t.Fatalf("tampered cookie: %d", r.StatusCode)
	}

	// logout expires the cookie
	lo := f.do(t, "owner", "POST", "/api/auth/logout", "")
	expect(t, lo, 204, "logout")
	if !strings.Contains(lo.hdr.Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout cookie: %q", lo.hdr.Get("Set-Cookie"))
	}
}

func TestDisabledUserAndPasswordChangeEndSessions(t *testing.T) {
	f := setup(t, Config{})
	old := f.cookie("member")
	// password change by another session of the same user invalidates the old cookie
	r := f.do(t, "member", "POST", "/api/auth/password", `{"current":"`+testPassword+`","new":"another-password-456"}`)
	expect(t, r, 204, "change password")
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/auth/me", nil)
	req.AddCookie(old)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 401 {
		t.Fatalf("old session after password change: %d", res.StatusCode)
	}

	expect(t, f.do(t, "op", "PATCH", "/api/admin/users/"+f.users["viewer"].ID, `{"disabled":true}`), 200, "disable user")
	expect(t, f.do(t, "viewer", "GET", "/api/auth/me", ""), 401, "disabled user session")
	users := f.do(t, "op", "GET", "/api/admin/users", "")
	expect(t, users, 200, "list users")
	if strings.Contains(users.body, "password_hash") || strings.Contains(users.body, "$2a$") {
		t.Fatalf("/api/admin/users leaks hash: %s", users.body)
	}
}

func TestCSRFHeaderRequiredOnMutations(t *testing.T) {
	f := setup(t, Config{})
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/orgs/" + f.orgA.ID + "/keys", `{"name":"x"}`},
		{"PATCH", "/api/orgs/" + f.orgA.ID, `{"name":"y"}`},
		{"POST", "/api/admin/providers", `{"name":"p","type":"openai"}`},
	} {
		as := "owner"
		if strings.HasPrefix(c.path, "/api/admin") {
			as = "op"
		}
		r := f.req(t, as, c.method, c.path, c.body, false)
		expect(t, r, 403, c.method+" "+c.path+" without X-GW-CSRF")
	}
	keys := f.do(t, "owner", "GET", "/api/orgs/"+f.orgA.ID+"/keys", "")
	if keys.body != "[]\n" {
		t.Fatalf("a key was created without CSRF header: %s", keys.body)
	}
	f.createKey(t, "owner", f.orgA.ID) // with the header it works
}

func TestRBACMatrix(t *testing.T) {
	f := setup(t, Config{})
	A, B := "/api/orgs/"+f.orgA.ID, "/api/orgs/"+f.orgB.ID
	memberKey, _ := f.createKey(t, "member", f.orgA.ID)
	cases := []struct {
		as, method, path, body string
		want                   int
	}{
		// viewer: read-only
		{"viewer", "GET", A, "", 200},
		{"viewer", "GET", A + "/usage", "", 200},
		{"viewer", "GET", A + "/logs", "", 200},
		{"viewer", "GET", A + "/models", "", 200},
		{"viewer", "POST", A + "/keys", `{"name":"v"}`, 403},
		{"viewer", "GET", A + "/audit", "", 403},
		// member: own keys only
		{"member", "POST", A + "/keys", `{"name":"m"}`, 201},
		{"member2", "PATCH", A + "/keys/" + memberKey, `{"revoked":true}`, 403},
		{"member", "GET", A + "/providers", "", 403},
		{"member", "POST", A + "/models", `{"id":"x/y"}`, 403},
		{"member", "GET", A + "/audit", "", 403},
		{"member", "POST", A + "/members", `{"email":"new@example.com","role":"viewer"}`, 403},
		{"member", "PATCH", A, `{"max_tokens_cap":10}`, 403},
		// admin: cannot grant or touch owners, cannot delete org, cannot set prepaid
		{"admin", "POST", A + "/members", `{"email":"x@example.com","role":"owner"}`, 403},
		{"admin", "PUT", A + "/members/" + f.users["member"].ID, `{"role":"owner"}`, 403},
		{"admin", "PUT", A + "/members/" + f.users["owner"].ID, `{"role":"viewer"}`, 403},
		{"admin", "DELETE", A + "/members/" + f.users["owner"].ID, "", 403},
		{"admin", "DELETE", A, "", 403},
		{"admin", "PATCH", A, `{"prepaid":true}`, 403},
		{"admin", "GET", A + "/audit", "", 200},
		{"admin", "PUT", A + "/members/" + f.users["viewer"].ID, `{"role":"member"}`, 204},
		// non-member: 404 so org ids cannot be probed
		{"outsider", "GET", A, "", 404},
		{"outsider", "GET", A + "/keys", "", 404},
		{"outsider", "POST", A + "/keys", `{"name":"o"}`, 404},
		{"outsider", "GET", A + "/usage", "", 404},
		{"member", "GET", B, "", 404},
		// platform scope: operator only
		{"owner", "GET", "/api/admin/providers", "", 403},
		{"owner", "POST", "/api/admin/models", `{"id":"x/y"}`, 403},
		{"owner", "GET", "/api/admin/users", "", 403},
		{"owner", "GET", "/api/admin/audit", "", 403},
		{"owner", "GET", "/api/admin/mcp", "", 403},
		{"owner", "POST", "/api/orgs", `{"name":"C"}`, 403},
		{"op", "GET", "/api/admin/providers", "", 200},
		{"op", "GET", A + "/audit", "", 200}, // operator acts as owner everywhere
		{"", "GET", A, "", 401},
	}
	for _, c := range cases {
		r := f.do(t, c.as, c.method, c.path, c.body)
		if r.code != c.want {
			t.Errorf("%s %s %s: status %d, want %d: %s", c.as, c.method, c.path, r.code, c.want, r.body)
		}
	}
	// the member's key is still active after member2's attempt; the owner (and admin) can revoke it
	k, _ := f.s.GetKey(context.Background(), f.orgA.ID, memberKey)
	if k.Revoked {
		t.Fatal("member2 revoked another member's key")
	}
	expect(t, f.do(t, "admin", "PATCH", A+"/keys/"+memberKey, `{"revoked":true}`), 200, "admin revokes member key")
	// revocation is one-way
	expect(t, f.do(t, "member", "PATCH", A+"/keys/"+memberKey, `{"revoked":false}`), 200, "member un-revoke attempt")
	if k, _ := f.s.GetKey(context.Background(), f.orgA.ID, memberKey); !k.Revoked {
		t.Fatal("revoked key was re-enabled")
	}
	// member can revoke own key
	own, _ := f.createKey(t, "member2", f.orgA.ID)
	expect(t, f.do(t, "member2", "PATCH", A+"/keys/"+own, `{"revoked":true}`), 200, "member revokes own key")
}

func TestLastOwnerCannotBeDemotedOrRemoved(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	ownerID := f.users["owner"].ID
	expect(t, f.do(t, "owner", "PUT", A+"/members/"+ownerID, `{"role":"admin"}`), 400, "demote last owner")
	expect(t, f.do(t, "owner", "DELETE", A+"/members/"+ownerID, ""), 400, "remove last owner")
	expect(t, f.do(t, "op", "PUT", A+"/members/"+ownerID, `{"role":"viewer"}`), 400, "operator demotes last owner")
	if m, _ := f.s.GetMember(context.Background(), f.orgA.ID, ownerID); m == nil || m.Role != store.RoleOwner {
		t.Fatalf("owner role changed: %+v", m)
	}
	// with a second owner, demotion is allowed
	expect(t, f.do(t, "owner", "PUT", A+"/members/"+f.users["admin"].ID, `{"role":"owner"}`), 204, "owner grants owner")
	expect(t, f.do(t, "owner", "PUT", A+"/members/"+ownerID, `{"role":"admin"}`), 204, "demote one of two owners")
	expect(t, f.do(t, "admin", "DELETE", A+"/members/"+f.users["admin"].ID, ""), 400, "remove the remaining owner")
}

func TestProviderSecretsNeverReturned(t *testing.T) {
	f := setup(t, Config{})
	const secret = "sk-upstream-TOPSECRET-123"
	pub := f.do(t, "op", "POST", "/api/admin/providers",
		`{"name":"pub","type":"openai","base_url":"http://127.0.0.1:9","enabled":true,"keys":[{"name":"main","value":"`+secret+`","weight":1}]}`)
	expect(t, pub, 200, "operator public provider")
	priv := f.do(t, "admin", "POST", "/api/orgs/"+f.orgA.ID+"/providers",
		`{"name":"priv","type":"openai","base_url":"https://8.8.8.8","enabled":true,"keys":[{"value":"`+secret+`"}]}`)
	expect(t, priv, 200, "tenant private provider")
	var p store.Provider
	pub.json(t, &p)

	for _, r := range []resp{pub, priv,
		f.do(t, "op", "GET", "/api/admin/providers", ""),
		f.do(t, "admin", "GET", "/api/orgs/"+f.orgA.ID+"/providers", ""),
		// update without a value keeps the key and still returns nothing secret
		f.do(t, "op", "PUT", "/api/admin/providers/"+p.ID, `{"name":"pub2","type":"openai","base_url":"http://127.0.0.1:9","enabled":true,"keys":[{"id":"`+p.Keys[0].ID+`"}]}`),
	} {
		expect(t, r, 200, "provider call")
		if strings.Contains(r.body, secret) || strings.Contains(r.body, "sealed") || strings.Contains(r.body, `"value"`) {
			t.Fatalf("provider API leaks key material: %s", r.body)
		}
	}
	// encrypted at rest, and the kept key still decrypts
	raw, err := f.s.KV.Get(context.Background(), "prov/"+p.ID)
	if err != nil || strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "sealed") {
		t.Fatalf("stored provider not sealed: %s %v", raw, err)
	}
	all, err := f.s.ListProviders(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		if x.ID == p.ID && (len(x.Keys) != 1 || x.Keys[0].Value != secret) {
			t.Fatalf("kept key lost after update: %+v", x.Keys)
		}
	}
	// tenants see only their own providers
	other := f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/providers", "")
	if other.body != "[]\n" {
		t.Fatalf("org B sees foreign providers: %s", other.body)
	}
}

func TestProviderSSRF(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID + "/providers"
	for _, u := range []string{"http://127.0.0.1:11434", "http://localhost:8080", "http://169.254.169.254", "http://10.0.0.5", "http://[::1]:80", "http://0.0.0.0", "file:///etc/passwd", "gopher://8.8.8.8"} {
		r := f.do(t, "admin", "POST", A, `{"name":"x","type":"openai","base_url":"`+u+`","enabled":true}`)
		expect(t, r, 400, "tenant provider "+u)
	}
	ok := f.do(t, "admin", "POST", A, `{"name":"ok","type":"openai","base_url":"https://8.8.8.8","enabled":true}`)
	expect(t, ok, 200, "tenant public-address provider")
	var p store.Provider
	ok.json(t, &p)
	expect(t, f.do(t, "admin", "PUT", A+"/"+p.ID, `{"name":"ok","type":"openai","base_url":"http://127.0.0.1:1","enabled":true}`), 400, "tenant update to loopback")
	expect(t, f.do(t, "op", "POST", "/api/admin/providers", `{"name":"local","type":"openai","base_url":"http://127.0.0.1:11434","enabled":true}`), 200, "operator loopback provider")
	expect(t, f.do(t, "admin", "POST", A, `{"name":"x","type":"azure"}`), 400, "platform-only type")

	// replicate/huggingface adapters fetch URLs from upstream replies: tenants get the vendor host only
	for _, typ := range []string{"replicate", "huggingface"} {
		expect(t, f.do(t, "admin", "POST", A, `{"name":"u-`+typ+`","type":"`+typ+`","base_url":"https://8.8.8.8","enabled":true,"keys":[{"value":"k"}]}`), 400, "tenant "+typ+" with custom base_url")
		expect(t, f.do(t, "admin", "POST", A, `{"name":"v-`+typ+`","type":"`+typ+`","enabled":true,"keys":[{"value":"k"}]}`), 200, "tenant "+typ+" on the vendor host")
	}

	g := setup(t, Config{AllowPrivateURLs: true})
	expect(t, g.do(t, "admin", "POST", "/api/orgs/"+g.orgA.ID+"/providers", `{"name":"x","type":"openai","base_url":"http://127.0.0.1:11434","enabled":true}`), 200, "ALLOW_PRIVATE_PROVIDER_URLS")
}

func TestCrossTenantProvidersAndModels(t *testing.T) {
	f := setup(t, Config{})
	pb := f.do(t, "outsider", "POST", "/api/orgs/"+f.orgB.ID+"/providers", `{"name":"b","type":"openai","base_url":"https://8.8.8.8","enabled":true}`)
	expect(t, pb, 200, "org B provider")
	var bProv store.Provider
	pb.json(t, &bProv)
	pub := f.do(t, "op", "POST", "/api/admin/providers", `{"name":"pub","type":"openai","base_url":"http://127.0.0.1:9","enabled":true}`)
	var pubProv store.Provider
	pub.json(t, &pubProv)

	A := "/api/orgs/" + f.orgA.ID
	expect(t, f.do(t, "admin", "POST", A+"/models", `{"id":"acme/steal","enabled":true,"targets":[{"provider_id":"`+bProv.ID+`","model":"m"}]}`), 400, "model targeting another org's provider")
	expect(t, f.do(t, "admin", "PUT", A+"/providers/"+bProv.ID, `{"name":"hijack","type":"openai","base_url":"https://8.8.4.4","enabled":true}`), 404, "update another org's provider")
	expect(t, f.do(t, "admin", "DELETE", A+"/providers/"+bProv.ID, ""), 404, "delete another org's provider")
	expect(t, f.do(t, "admin", "PUT", A+"/providers/"+pubProv.ID, `{"name":"hijack","type":"openai","base_url":"https://8.8.4.4","enabled":true}`), 404, "tenant updates public provider")
	expect(t, f.do(t, "admin", "DELETE", A+"/providers/"+pubProv.ID, ""), 404, "tenant deletes public provider")
	expect(t, f.do(t, "op", "POST", "/api/admin/models", `{"id":"pub/steal","enabled":true,"targets":[{"provider_id":"`+bProv.ID+`","model":"m"}]}`), 400, "public model targeting a private provider")

	// org models may not route to public providers at their own price (billing bypass, SECURITY H4)
	expect(t, f.do(t, "admin", "POST", A+"/models", `{"id":"acme/cheap","enabled":true,"targets":[{"provider_id":"`+pubProv.ID+`","model":"m"}]}`), 400, "private model on public provider")
	pa := f.do(t, "admin", "POST", A+"/providers", `{"name":"a","type":"openai","base_url":"https://8.8.8.8","enabled":true}`)
	var aProv store.Provider
	pa.json(t, &aProv)
	expect(t, f.do(t, "admin", "POST", A+"/models", `{"id":"acme/private","name":"P","enabled":true,"targets":[{"provider_id":"`+aProv.ID+`","model":"m"}]}`), 200, "private model on own provider")
	expect(t, f.do(t, "op", "POST", "/api/admin/models", `{"id":"vendor/public","name":"Pub","enabled":true,"input_price_micros":1500000,"output_price_micros":3000000,"context_length":8192,"targets":[{"provider_id":"`+pubProv.ID+`","model":"m"}]}`), 200, "public model")
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	cat := f.do(t, "", "GET", "/api/public/models", "")
	expect(t, cat, 200, "public catalog")
	if strings.Contains(cat.body, "acme/private") || !strings.Contains(cat.body, `"vendor/public"`) || !strings.Contains(cat.body, `"1.5"`) {
		t.Fatalf("public catalog: %s", cat.body)
	}
	bModels := f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/models", "")
	if strings.Contains(bModels.body, "acme/private") || strings.Contains(bModels.body, pubProv.ID) {
		t.Fatalf("org B sees org A's private model or public targets: %s", bModels.body)
	}
	expect(t, f.do(t, "outsider", "DELETE", "/api/orgs/"+f.orgB.ID+"/models/acme/private", ""), 404, "org B deletes org A's model")
}

// A tenant must not be able to affect platform (public) providers. Non-custom provider types
// (groq, mistral, ...) share one Bifrost slot per type, and the oldest provider of the type wins.
func TestTenantProviderDoesNotShadowPublicProvider(t *testing.T) {
	f := setup(t, Config{})
	r := f.do(t, "admin", "POST", "/api/orgs/"+f.orgA.ID+"/providers", `{"name":"mine","type":"groq","base_url":"https://8.8.8.8","enabled":true,"keys":[{"value":"tenant-key"}]}`)
	if r.code != 200 {
		t.Skipf("tenant cannot create non-custom provider types (status %d); nothing to shadow", r.code)
	}
	pub := f.do(t, "op", "POST", "/api/admin/providers", `{"name":"platform-groq","type":"groq","enabled":true,"keys":[{"value":"platform-key"}]}`)
	expect(t, pub, 200, "public groq provider")
	var p store.Provider
	pub.json(t, &p)
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.g.Snapshot().Providers[p.ID]; !ok {
		t.Fatalf("public provider %s is missing from the routing snapshot: a tenant's private %q provider shadows it, so every public model on it fails", p.ID, "groq")
	}
}

func TestKeySecretShownOnceAndAudited(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	r := f.do(t, "member", "POST", A+"/keys", `{"name":"ci","rpm":60,"monthly_budget_usd":5,"models":["vendor/x"],"expires_in_days":30}`)
	expect(t, r, 201, "create key")
	var out struct {
		Key    store.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	r.json(t, &out)
	if !strings.HasPrefix(out.Secret, gateway.KeyPrefix) || len(out.Secret) < 30 || out.Key.Prefix != out.Secret[:12] {
		t.Fatalf("secret %q prefix %q", out.Secret, out.Key.Prefix)
	}
	if out.Key.RPM != 60 || out.Key.MonthlyBudget != 5_000_000 || out.Key.ExpiresAt == nil || out.Key.CreatedBy != f.users["member"].ID {
		t.Fatalf("key fields: %+v", out.Key)
	}
	if strings.Contains(r.body, gateway.HashKey(out.Secret)) || strings.Contains(r.body, `"hash"`) {
		t.Fatalf("create response leaks hash: %s", r.body)
	}
	list := f.do(t, "viewer", "GET", A+"/keys", "")
	expect(t, list, 200, "list keys")
	if strings.Contains(list.body, out.Secret) || strings.Contains(list.body, gateway.HashKey(out.Secret)) || !strings.Contains(list.body, out.Key.ID) {
		t.Fatalf("key list leaks secret/hash: %s", list.body)
	}
	// only the hash is stored
	if k, err := f.s.KeyByHash(context.Background(), gateway.HashKey(out.Secret)); err != nil || k.ID != out.Key.ID {
		t.Fatalf("key by hash: %v %v", k, err)
	}

	audit := f.do(t, "admin", "GET", A+"/audit", "")
	expect(t, audit, 200, "audit")
	var entries []store.AuditEntry
	audit.json(t, &entries)
	found := false
	for _, e := range entries {
		if e.Action == "key.create" && e.Target == out.Key.ID && e.ActorID == f.users["member"].ID && e.Actor == "member@example.com" && !e.At.IsZero() {
			found = true
		}
	}
	if !found {
		t.Fatalf("no key.create audit entry: %s", audit.body)
	}
	// other mutations are audited too
	expect(t, f.do(t, "admin", "POST", A+"/members", `{"email":"newbie@example.com","role":"viewer"}`), 201, "add member")
	expect(t, f.do(t, "admin", "PATCH", A, `{"max_tokens_cap":256}`), 200, "patch org")
	audit = f.do(t, "admin", "GET", A+"/audit", "")
	for _, act := range []string{"member.add", "org.update", "key.create"} {
		if !strings.Contains(audit.body, `"action":"`+act+`"`) {
			t.Errorf("audit missing %s: %s", act, audit.body)
		}
	}
	// org B's audit does not include org A's actions
	if b := f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/audit", ""); strings.Contains(b.body, "key.create") {
		t.Fatalf("org B sees org A audit: %s", b.body)
	}
}

func TestCreditsOperatorOnly(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	for _, who := range []string{"owner", "admin", "member", "outsider"} {
		expect(t, f.do(t, who, "POST", A+"/credits", `{"usd":100}`), 403, who+" grants credits")
	}
	expect(t, f.do(t, "op", "POST", A+"/credits", `{"usd":0}`), 400, "zero credits")
	r := f.do(t, "op", "POST", A+"/credits", `{"usd":12.5,"note":"invoice 7"}`)
	expect(t, r, 200, "operator grants credits")
	var v struct {
		Credits int64 `json:"credits_micros"`
		Balance int64 `json:"balance_micros"`
	}
	r.json(t, &v)
	if v.Credits != 12_500_000 || v.Balance != 12_500_000 {
		t.Fatalf("credits: %+v", v)
	}
	audit := f.do(t, "owner", "GET", A+"/audit", "")
	if !strings.Contains(audit.body, `"action":"org.credits"`) {
		t.Fatalf("credits not audited: %s", audit.body)
	}
}

// F7-AC4: every admin mutation leaves an audit entry someone can view. After an org is deleted
// its own audit log is unreachable, so the deletion must be visible to the operator.
func TestOrgDeletionAuditIsViewable(t *testing.T) {
	f := setup(t, Config{})
	expect(t, f.do(t, "op", "DELETE", "/api/orgs/"+f.orgB.ID, ""), 204, "delete org")
	expect(t, f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID, ""), 404, "deleted org")
	r := f.do(t, "op", "GET", "/api/admin/audit", "")
	expect(t, r, 200, "platform audit")
	if !strings.Contains(r.body, `"org.delete"`) {
		t.Fatalf("org.delete audit entry is not viewable by anyone: /api/admin/audit = %s", r.body)
	}
}

func TestMalformedJSON(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	expect(t, f.do(t, "owner", "POST", A+"/keys", `{"name":`), 400, "truncated JSON")
	expect(t, f.do(t, "owner", "POST", A+"/keys", `{"nmae":"typo"}`), 400, "unknown field")
	expect(t, f.do(t, "owner", "POST", A+"/keys", `{"name":"`+strings.Repeat("a", 2<<20)+`"}`), 400, "oversized body")
}

func TestOrgGuardrailEnums(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	expect(t, f.do(t, "admin", "PATCH", A, `{"guardrails":{"prompt_injection":"loud"}}`), 400, "bad prompt_injection")
	expect(t, f.do(t, "admin", "PATCH", A, `{"guardrails":{"compression":"lossy"}}`), 400, "bad compression")
	r := f.do(t, "admin", "PATCH", A, `{"guardrails":{"mask_secrets":true,"prompt_injection":"block","compression":"lossless"}}`)
	expect(t, r, 200, "valid guardrails")
	var o store.Org
	r.json(t, &o)
	if g := o.Guardrails; !g.MaskSecrets || g.PromptInjection != "block" || g.Compression != "lossless" {
		t.Fatalf("guardrails not saved: %+v", g)
	}
}

// F6-AC1: revocation is immediate on the node that served the admin call (the key cache must not
// keep serving a revoked key until the next catalog poll).
func TestKeyRevocationImmediateOnSameNode(t *testing.T) {
	f := setup(t, Config{})
	id, secret := f.createKey(t, "member", f.orgA.ID)
	call := func() int {
		req, _ := http.NewRequest("GET", f.srv.URL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := call(); c != 200 {
		t.Fatalf("fresh key: %d", c)
	}
	expect(t, f.do(t, "member", "PATCH", "/api/orgs/"+f.orgA.ID+"/keys/"+id, `{"revoked":true}`), 200, "revoke")
	if c := call(); c != 401 {
		t.Fatalf("revoked key still accepted right after revocation: status %d (served from the 30 s auth cache until the next catalog poll)", c)
	}
}

func TestMemberCannotLiftAdminLimits(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID + "/keys/"
	id, _ := f.createKey(t, "member", f.orgA.ID)
	expect(t, f.do(t, "admin", "PATCH", A+id, `{"rpm":10}`), 200, "admin sets limit")
	expect(t, f.do(t, "member", "PATCH", A+id, `{"rpm":0}`), 403, "member lifts limit")
	r := f.do(t, "admin", "PATCH", A+id, `{"mcp_servers":["search"]}`)
	expect(t, r, 200, "admin limits MCP servers")
	if !strings.Contains(r.body, `"mcp_servers":["search"]`) {
		t.Fatalf("mcp_servers not stored: %s", r.body)
	}
	expect(t, f.do(t, "member", "PATCH", A+id, `{"mcp_servers":[]}`), 403, "member lifts MCP limit")
	expect(t, f.do(t, "member", "PATCH", A+id, `{"name":"renamed"}`), 200, "member renames own key")
	expect(t, f.do(t, "member", "PATCH", A+id, `{"revoked":true}`), 200, "member revokes own key")
}

func TestModelIDTraversalRejected(t *testing.T) {
	f := setup(t, Config{})
	for _, id := range []string{"x/../..", "a/b/../c", "noslash", "a/b c"} {
		expect(t, f.do(t, "op", "POST", "/api/admin/models", `{"id":"`+id+`","targets":[{"provider_id":"x","model":"m"}]}`), 400, "model id "+id)
	}
}

func TestMCPGrantOperatorOnly(t *testing.T) {
	f := setup(t, Config{})
	expect(t, f.do(t, "owner", "PATCH", "/api/orgs/"+f.orgA.ID, `{"mcp_servers":["*"]}`), 403, "owner grants MCP")
	expect(t, f.do(t, "op", "PATCH", "/api/orgs/"+f.orgA.ID, `{"mcp_servers":["search"]}`), 200, "operator grants MCP")
	expect(t, f.do(t, "op", "POST", "/api/admin/mcp", `{"name":"web-search","type":"http","url":"http://127.0.0.1:9/mcp"}`), 400, "MCP name Bifrost rejects")
}

func TestPublicModelHealthAndStatus(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	prov := func(name string) store.Provider {
		var p store.Provider
		r := f.do(t, "op", "POST", "/api/admin/providers", `{"name":"`+name+`","type":"openai","base_url":"http://127.0.0.1:9","enabled":true,"keys":[{"value":"sk-up-secret-1"}]}`)
		expect(t, r, 200, "public provider "+name)
		r.json(t, &p)
		return p
	}
	alpha, beta := prov("alpha"), prov("beta")
	prov("gamma") // public provider without traffic
	expect(t, f.do(t, "op", "POST", "/api/admin/models", `{"id":"vendor/m","name":"M","enabled":true,"max_output_tokens":4096,"targets":[{"provider_id":"`+alpha.ID+`","model":"up-a","weight":2},{"provider_id":"`+beta.ID+`","model":"up-b"}]}`), 200, "public model")
	A := "/api/orgs/" + f.orgA.ID
	pa := f.do(t, "admin", "POST", A+"/providers", `{"name":"privprov","type":"openai","base_url":"https://8.8.8.8","enabled":true}`)
	var aProv store.Provider
	pa.json(t, &aProv)
	expect(t, f.do(t, "admin", "POST", A+"/models", `{"id":"acme/private","name":"P","enabled":true,"targets":[{"provider_id":"`+aProv.ID+`","model":"m"}]}`), 200, "private model")
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	err := f.s.KV.AddAgg(ctx, map[string]store.Agg{
		// alpha: 100 ok requests, 500 ms each, 100 output tokens each -> 200 tok/s
		usage.HealthKey("vendor/m", alpha.ID, now):                    {store.AggRequests: 100, store.AggLatencyMs: 50_000, store.AggOutTokens: 10_000},
		usage.HealthKey("vendor/m", alpha.ID, now.Add(-30*time.Hour)): {store.AggRequests: 10, store.AggLatencyMs: 5_000},
		// beta: half the attempts failed -> down
		usage.HealthKey("vendor/m", beta.ID, now):      {store.AggRequests: 10, store.AggErrors: 5, store.AggLatencyMs: 5_000},
		usage.HealthKey("acme/private", aProv.ID, now): {store.AggRequests: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	r := f.do(t, "", "GET", "/api/public/models/vendor/m", "")
	expect(t, r, 200, "public model health")
	if r.hdr.Get("Cache-Control") != "public, max-age=30" {
		t.Fatalf("cache-control: %q", r.hdr.Get("Cache-Control"))
	}
	for _, leak := range []string{alpha.ID, beta.ID, "base_url", "127.0.0.1", "sk-up-secret", "provider_id"} {
		if strings.Contains(r.body, leak) {
			t.Fatalf("public model page leaks %q: %s", leak, r.body)
		}
	}
	var page struct {
		Model     gateway.ModelInfo `json:"model"`
		Stats     healthStats       `json:"stats_24h"`
		Providers []struct {
			Name, Type, UpstreamModel, Status string
			Weight                            float64
			Stats                             healthStats `json:"stats_24h"`
			Hourly                            []hourStats
		} `json:"providers"`
	}
	r.json(t, &page)
	if page.Model.MaxOutput != 4096 || strings.Join(page.Model.Providers, ",") != "alpha,beta" || page.Stats.Requests != 110 || page.Stats.Errors != 5 {
		t.Fatalf("model: %+v stats %+v", page.Model, page.Stats)
	}
	if len(page.Providers) != 2 {
		t.Fatalf("providers: %s", r.body)
	}
	a, b := page.Providers[0], page.Providers[1]
	if a.Name != "alpha" || a.Status != "operational" || a.Weight != 2 || a.Stats.Requests != 100 || *a.Stats.Uptime != 1 ||
		a.Stats.AvgLatencyMs != 500 || *a.Stats.ThroughputTPS != 200 || len(a.Hourly) != 72 || a.Hourly[71].Requests != 100 || a.Hourly[41].Requests != 10 {
		t.Fatalf("alpha: %+v", a)
	}
	if b.Status != "down" || *b.Stats.Uptime != 0.5 || b.Stats.AvgLatencyMs != 1000 {
		t.Fatalf("beta: %+v", b)
	}
	expect(t, f.do(t, "", "GET", "/api/public/models/acme/private", ""), 404, "private model is not public")
	expect(t, f.do(t, "", "GET", "/api/public/models/nope/x", ""), 404, "unknown model")

	st := f.do(t, "", "GET", "/api/public/status", "")
	expect(t, st, 200, "public status")
	if strings.Contains(st.body, "privprov") || strings.Contains(st.body, alpha.ID) || strings.Contains(st.body, "base_url") {
		t.Fatalf("status leaks: %s", st.body)
	}
	var status struct {
		Providers []struct {
			Name, Status string
			Models       int
			Stats        healthStats `json:"stats_24h"`
		}
		UpdatedAt time.Time `json:"updated_at"`
	}
	st.json(t, &status)
	got := map[string]string{}
	for _, p := range status.Providers {
		got[p.Name] = fmt.Sprintf("%s/%d/%d", p.Status, p.Models, p.Stats.Requests)
	}
	if len(got) != 3 || got["alpha"] != "operational/1/100" || got["beta"] != "down/1/10" || got["gamma"] != "no_data/0/0" || status.UpdatedAt.IsZero() {
		t.Fatalf("status: %s", st.body)
	}
}

func TestSessionEndpoint(t *testing.T) {
	f := setup(t, Config{})
	r := f.do(t, "", "GET", "/api/auth/session", "")
	expect(t, r, 200, "anonymous session")
	if !strings.Contains(r.body, `"user":null`) {
		t.Fatalf("anonymous: %s", r.body)
	}
	r = f.do(t, "owner", "GET", "/api/auth/session", "")
	if !strings.Contains(r.body, "owner@example.com") || strings.Contains(r.body, "password_hash") {
		t.Fatalf("signed in: %s", r.body)
	}
}

func TestCloudProviderValidationAndRedaction(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID + "/providers"
	P := "/api/admin/providers"
	for _, c := range []struct{ as, path, body, what string }{
		{"op", P, `{"name":"a","type":"azure","keys":[{"value":"k"}]}`, "azure without endpoint"},
		{"op", P, `{"name":"a","type":"azure","base_url":"https://x.example","keys":[{"value":"k","azure":{"endpoint":"https://r.openai.azure.com"}}]}`, "azure with base_url"},
		{"op", P, `{"name":"b","type":"bedrock","keys":[{"bedrock":{}}]}`, "bedrock without region"},
		{"op", P, `{"name":"b","type":"bedrock","keys":[{"bedrock":{"region":"evil.com/x#"}}]}`, "bedrock region injection"},
		{"op", P, `{"name":"b","type":"bedrock","keys":[{"bedrock":{"region":"us-east-1","access_key":"AK"}}]}`, "bedrock access key without secret"},
		{"op", P, `{"name":"b","type":"bedrock"}`, "bedrock without keys"},
		{"op", P, `{"name":"v","type":"vertex","keys":[{"vertex":{"region":"us-central1"}}]}`, "vertex without project"},
		{"op", P, `{"name":"v","type":"vertex","keys":[{"vertex":{"project_id":"p","region":"us-central1","auth_credentials":"not json"}}]}`, "vertex bad credentials"},
		{"op", P, `{"name":"o","type":"openai","keys":[{"value":"k","vertex":{"project_id":"p","region":"r"}}]}`, "config for another type"},
		{"admin", A, `{"name":"a","type":"azure","keys":[{"value":"k","azure":{"endpoint":"https://8.8.8.8"}}]}`, "tenant azure (platform-only)"},
		{"admin", A, `{"name":"v","type":"vertex","keys":[{"vertex":{"project_id":"p","region":"us-central1"}}]}`, "tenant vertex (platform-only)"},
		{"admin", A, `{"name":"b","type":"bedrock","keys":[{"bedrock":{"region":"us-east-1"}}]}`, "tenant bedrock on host IAM"},
		{"admin", A, `{"name":"b","type":"bedrock","keys":[{"id":"made-up","bedrock":{"region":"us-east-1"}}]}`, "tenant bedrock IAM via unknown key id"},
	} {
		expect(t, f.do(t, c.as, "POST", c.path, c.body), 400, c.what)
	}

	const secret = "TOPSECRET-cloud"
	for _, c := range []struct{ as, path, body string }{
		{"op", P, `{"name":"az","type":"azure","enabled":true,"keys":[{"value":"` + secret + `","azure":{"endpoint":"https://r.openai.azure.com","api_version":"2024-10-21","deployments":{"gpt-4o":"prod"}}}]}`},
		{"op", P, `{"name":"br","type":"bedrock","enabled":true,"keys":[{"bedrock":{"region":"us-east-1"}}]}`},
		{"op", P, `{"name":"vx","type":"vertex","enabled":true,"keys":[{"vertex":{"project_id":"proj-1","region":"us-central1","auth_credentials":"{\"k\":\"` + secret + `\"}"}}]}`},
		{"op", P, `{"name":"el","type":"elevenlabs","enabled":true,"keys":[{"value":"` + secret + `"}]}`},
		{"admin", A, `{"name":"tb","type":"bedrock","enabled":true,"keys":[{"bedrock":{"region":"us-east-1","access_key":"AK` + secret + `","secret_key":"` + secret + `","session_token":"` + secret + `"}}]}`},
	} {
		r := f.do(t, c.as, "POST", c.path, c.body)
		expect(t, r, 200, c.body)
		if strings.Contains(r.body, secret) || strings.Contains(r.body, "sealed") {
			t.Fatalf("provider API leaks config secrets: %s", r.body)
		}
	}
	list := f.do(t, "op", "GET", P, "")
	if strings.Contains(list.body, secret) || !strings.Contains(list.body, `"prod"`) || !strings.Contains(list.body, "proj-1") {
		t.Fatalf("provider list: %s", list.body)
	}
	// tenant bedrock keeps its stored AWS keys on an update with empty secrets
	var tb []store.Provider
	f.do(t, "admin", "GET", A, "").json(t, &tb)
	upd := `{"name":"tb2","type":"bedrock","enabled":true,"keys":[{"id":"` + tb[0].Keys[0].ID + `","bedrock":{"region":"eu-west-1"}}]}`
	expect(t, f.do(t, "admin", "PUT", A+"/"+tb[0].ID, upd), 200, "tenant bedrock update keeping secrets")
	all, _ := f.s.ListProviders(context.Background(), true)
	for _, p := range all {
		if p.ID == tb[0].ID && (p.Keys[0].Bedrock.SecretKey != secret || p.Keys[0].Bedrock.Region != "eu-west-1") {
			t.Fatalf("kept bedrock secrets: %+v", p.Keys[0].Bedrock)
		}
	}
	var cfg struct {
		ProviderTypes map[string][]string `json:"provider_types"`
	}
	f.do(t, "", "GET", "/api/auth/config", "").json(t, &cfg)
	if !slices.Contains(cfg.ProviderTypes["org"], "bedrock") || slices.Contains(cfg.ProviderTypes["org"], "azure") || !slices.Contains(cfg.ProviderTypes["platform"], "vertex") {
		t.Fatalf("provider types: %+v", cfg.ProviderTypes)
	}
}
