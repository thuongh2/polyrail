package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	return &http.Cookie{Name: sessionCookie, Value: f.a.sign.sign(expiry(time.Hour), u.ID, sessionEpoch(u))}
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
		res, err := http.Post(f.srv.URL+"/api/auth/login", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		return res
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
	expect(t, f.do(t, "admin", "POST", A, `{"name":"x","type":"bedrock"}`), 400, "unsupported type")

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

	// org A may build a private model on a public provider; it stays private
	expect(t, f.do(t, "admin", "POST", A+"/models", `{"id":"acme/private","name":"P","enabled":true,"targets":[{"provider_id":"`+pubProv.ID+`","model":"m"}]}`), 200, "private model on public provider")
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
