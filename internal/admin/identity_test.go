package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/polyrail/polyrail/internal/store"
	dsig "github.com/russellhaering/goxmldsig"
)

// stubTXT answers DNS TXT lookups from a map.
func stubTXT(records map[string][]string) func(context.Context, string) ([]string, error) {
	return func(_ context.Context, name string) ([]string, error) {
		if v, ok := records[name]; ok {
			return v, nil
		}
		return nil, errors.New("no such host")
	}
}

func TestDomainVerification(t *testing.T) {
	f := setup(t, Config{})
	txt := map[string][]string{}
	f.a.LookupTXT = stubTXT(txt)
	a, b := "/api/orgs/"+f.orgA.ID+"/domains", "/api/orgs/"+f.orgB.ID+"/domains"
	expect(t, f.do(t, "admin", "POST", a, `{"name":"acme.com"}`), 403, "admin adds a domain")
	r := f.do(t, "owner", "POST", a, `{"name":"Acme.com"}`)
	expect(t, r, 201, "owner adds a domain")
	var d struct {
		ID       string            `json:"id"`
		Name     string            `json:"name"`
		Verified bool              `json:"verified"`
		Record   map[string]string `json:"record"`
	}
	r.json(t, &d)
	if d.Name != "acme.com" || d.Record["name"] != "_polyrail-verify.acme.com" || !strings.HasPrefix(d.Record["value"], "polyrail-verification=") {
		t.Fatalf("domain view: %+v", d)
	}
	expect(t, f.do(t, "owner", "POST", a+"/"+d.ID+"/verify", ""), 422, "verify without the record")
	txt["_polyrail-verify.acme.com"] = []string{"v=spf1 -all", d.Record["value"]}
	r = f.do(t, "owner", "POST", a+"/"+d.ID+"/verify", "")
	expect(t, r, 200, "verify")
	r.json(t, &d)
	if !d.Verified {
		t.Fatal("not verified")
	}
	// another org cannot verify the same domain, even with its own record published
	r = f.do(t, "outsider", "POST", b, `{"name":"acme.com"}`)
	expect(t, r, 201, "org B claims the domain")
	var db struct {
		ID     string            `json:"id"`
		Record map[string]string `json:"record"`
	}
	r.json(t, &db)
	txt["_polyrail-verify.acme.com"] = append(txt["_polyrail-verify.acme.com"], db.Record["value"])
	expect(t, f.do(t, "outsider", "POST", b+"/"+db.ID+"/verify", ""), 409, "org B verifies a domain org A holds")
	// org ids cannot be probed; members of A cannot touch B's domains
	expect(t, f.do(t, "owner", "POST", b+"/"+db.ID+"/verify", ""), 404, "cross-org verify")
	expect(t, f.do(t, "owner", "DELETE", a+"/"+d.ID, ""), 204, "delete")
	expect(t, f.do(t, "outsider", "POST", b+"/"+db.ID+"/verify", ""), 200, "org B verifies after A released it")
	audit := f.do(t, "owner", "GET", "/api/orgs/"+f.orgA.ID+"/audit", "")
	for _, act := range []string{"domain.create", "domain.verify", "domain.delete"} {
		if !strings.Contains(audit.body, act) {
			t.Fatalf("audit lacks %s: %s", act, audit.body)
		}
	}
}

// testIdP is a SAML identity provider (crewjam's own IdP code) that signs assertions for tests.
type testIdP struct {
	idp *saml.IdentityProvider
	sp  *saml.EntityDescriptor
}

func (p *testIdP) GetServiceProvider(*http.Request, string) (*saml.EntityDescriptor, error) {
	return p.sp, nil
}

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	p := &testIdP{}
	meta, _ := url.Parse("https://idp.example.org/metadata")
	sso, _ := url.Parse("https://idp.example.org/sso")
	p.idp = &saml.IdentityProvider{Key: k, Certificate: cert, MetadataURL: *meta, SSOURL: *sso,
		ServiceProviderProvider: p, SignatureMethod: dsig.RSASHA256SignatureMethod}
	return p
}

func (p *testIdP) metadataXML(t *testing.T) string {
	t.Helper()
	b, err := xml.Marshal(p.idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertion signs a response for email; authnURL is the SP's redirect to the IdP ("" for an
// IdP-initiated response). It returns the base64 SAMLResponse.
func (p *testIdP) assertion(t *testing.T, f *fixture, org, authnURL, email string, groups ...string) string {
	t.Helper()
	res, err := http.Get(f.srv.URL + "/api/sso/saml/" + org + "/metadata")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("sp metadata: %v %v", err, res)
	}
	p.sp = &saml.EntityDescriptor{}
	if err := xml.NewDecoder(res.Body).Decode(p.sp); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var req *saml.IdpAuthnRequest
	if authnURL != "" {
		req, err = saml.NewIdpAuthnRequest(p.idp, httptest.NewRequest("GET", authnURL, nil))
		if err == nil {
			err = req.Validate()
		}
	} else {
		req, err = p.idp.NewIdpInitiatedRequest(httptest.NewRequest("GET", "https://idp.example.org/start", nil), p.sp.EntityID, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	vals := func(vs ...string) []saml.AttributeValue {
		var out []saml.AttributeValue
		for _, v := range vs {
			out = append(out, saml.AttributeValue{Type: "xs:string", Value: v})
		}
		return out
	}
	attrs := []saml.Attribute{{Name: "email", Values: vals(email)}, {Name: "name", Values: vals("Jit User")}}
	if len(groups) > 0 {
		attrs = append(attrs, saml.Attribute{Name: "groups", Values: vals(groups...)})
	}
	now := time.Now()
	sess := &saml.Session{ID: "s-" + email, CreateTime: now, ExpireTime: now.Add(time.Hour), Index: "1",
		NameID: email, NameIDFormat: string(saml.EmailAddressNameIDFormat), CustomAttributes: attrs}
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, sess); err != nil {
		t.Fatal(err)
	}
	form, err := req.PostBinding()
	if err != nil {
		t.Fatal(err)
	}
	return form.SAMLResponse
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// samlStart begins an SP-initiated login and returns the IdP URL and the request cookie.
func samlStart(t *testing.T, f *fixture, org string) (string, *http.Cookie) {
	t.Helper()
	res, err := noRedirect.Get(f.srv.URL + "/api/sso/saml/" + org + "/login")
	if err != nil || res.StatusCode != 302 {
		t.Fatalf("saml login: %v %v", err, res)
	}
	res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Name == samlCookie {
			return res.Header.Get("Location"), c
		}
	}
	t.Fatal("no saml cookie")
	return "", nil
}

func postACS(t *testing.T, f *fixture, org, samlResponse string, c *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/sso/saml/"+org+"/acs", strings.NewReader(url.Values{"SAMLResponse": {samlResponse}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(c)
	}
	res, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// samlOrg verifies example.com for org A and configures SAML with the test IdP.
func samlOrg(t *testing.T, f *fixture, idp *testIdP, extra string) {
	t.Helper()
	ctx := context.Background()
	d := &store.Domain{OrgID: f.orgA.ID, Name: "example.com"}
	if err := f.s.CreateDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.VerifyDomain(ctx, f.orgA.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	body := `{"enabled":true,"idp_metadata_xml":` + jsonString(idp.metadataXML(t)) + extra + `}`
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+f.orgA.ID+"/sso/saml", body), 200, "configure saml")
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestSAMLLogin(t *testing.T) {
	f := setup(t, Config{})
	f.a.cfg.PublicURL = f.srv.URL
	idp := newTestIdP(t)
	org := f.orgA.ID
	expect(t, f.do(t, "owner", "GET", "/api/orgs/"+org+"/sso", ""), 200, "sso settings")
	expect(t, f.do(t, "admin", "GET", "/api/orgs/"+org+"/sso", ""), 403, "admin reads sso settings")
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/saml", `{"enabled":true,"idp_metadata_xml":"<x/>"}`), 400, "bad metadata")
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/saml", `{"enabled":true,"idp_metadata_url":"http://127.0.0.1:1/m"}`), 400, "metadata URL on a private address")
	samlOrg(t, f, idp, `,"attribute_groups":"groups"`)
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/group-mappings", `[{"group":"Platform Admins","role":"admin","workspace_id":"ws1"}]`), 400, "unknown workspace")
	ws1 := &store.Workspace{OrgID: org, Name: "One", Slug: "one"}
	if err := f.s.CreateWorkspace(context.Background(), ws1); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/group-mappings",
		`[{"group":"Platform Admins","role":"admin"},{"group":"Platform Admins","role":"viewer","workspace_id":"`+ws1.ID+`"}]`), 200, "group mappings")
	sso := f.do(t, "owner", "GET", "/api/orgs/"+org+"/sso", "")
	if strings.Contains(sso.body, "PRIVATE KEY") || !strings.Contains(sso.body, "BEGIN CERTIFICATE") || !strings.Contains(sso.body, `"workspace_id":"`+ws1.ID+`"`) {
		t.Fatalf("sso view: %s", sso.body)
	}

	// SP-initiated: a new user on the verified domain is provisioned with the mapped role
	authn, c := samlStart(t, f, org)
	resp := idp.assertion(t, f, org, authn, "Newbie@Example.com", "Platform Admins")
	res := postACS(t, f, org, resp, c)
	if res.StatusCode != 302 || res.Header.Get("Location") != "/app/" {
		t.Fatalf("acs: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	var sess *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie {
			sess = ck
		}
	}
	if sess == nil {
		t.Fatal("no session cookie")
	}
	u, err := f.s.GetUserByEmail(context.Background(), "newbie@example.com")
	if err != nil || u.Name != "Jit User" {
		t.Fatalf("jit user: %+v %v", u, err)
	}
	if m, err := f.s.GetMember(context.Background(), org, u.ID); err != nil || m.Role != store.RoleAdmin {
		t.Fatalf("membership: %+v %v", m, err)
	}
	if wm, err := f.s.GetWorkspaceMember(context.Background(), org, ws1.ID, u.ID); err != nil || wm.Role != store.RoleViewer || !wm.IdP {
		t.Fatalf("workspace membership from the group mapping: %+v %v", wm, err)
	}
	if _, ok := f.a.sign.verify("sess", sess.Value); !ok {
		t.Fatal("session cookie does not verify")
	}
	// the same response again is a replay
	if res := postACS(t, f, org, resp, c); res.StatusCode != 401 {
		t.Fatalf("replay: %d", res.StatusCode)
	}
	// InResponseTo must match this browser's request: no cookie, no login (IdP-initiated is off)
	authn, _ = samlStart(t, f, org)
	if res := postACS(t, f, org, idp.assertion(t, f, org, authn, "member@example.com"), nil); res.StatusCode != 401 {
		t.Fatalf("response without the request cookie: %d", res.StatusCode)
	}
	if res := postACS(t, f, org, idp.assertion(t, f, org, "", "member@example.com"), nil); res.StatusCode != 401 {
		t.Fatalf("idp-initiated while disabled: %d", res.StatusCode)
	}
	// an email outside the org's verified domains, and an operator, are refused
	authn, c = samlStart(t, f, org)
	if res := postACS(t, f, org, idp.assertion(t, f, org, authn, "ceo@other.org"), c); res.StatusCode != 403 {
		t.Fatalf("unverified domain: %d", res.StatusCode)
	}
	authn, c = samlStart(t, f, org)
	if res := postACS(t, f, org, idp.assertion(t, f, org, authn, "op@example.com"), c); res.StatusCode != 403 {
		t.Fatalf("operator via tenant IdP: %d", res.StatusCode)
	}
	// a response signed by another key is refused
	evil := newTestIdP(t)
	authn, c = samlStart(t, f, org)
	if res := postACS(t, f, org, evil.assertion(t, f, org, authn, "member@example.com"), c); res.StatusCode != 401 {
		t.Fatalf("foreign signature: %d", res.StatusCode)
	}
	// IdP-initiated works once allowed; an existing member keeps their role without a mapping
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/saml", `{"enabled":true,"allow_idp_initiated":true}`), 200, "allow idp-initiated")
	if res := postACS(t, f, org, idp.assertion(t, f, org, "", "member@example.com"), nil); res.StatusCode != 302 {
		t.Fatalf("idp-initiated: %d", res.StatusCode)
	}
	if m, _ := f.s.GetMember(context.Background(), org, f.users["member"].ID); m.Role != store.RoleMember {
		t.Fatalf("member role changed: %s", m.Role)
	}
	// the metadata URL path, through an allowed private address
	g := setup(t, Config{AllowPrivateURLs: true})
	idpMeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(idp.metadataXML(t))) }))
	defer idpMeta.Close()
	r := g.do(t, "owner", "PUT", "/api/orgs/"+g.orgA.ID+"/sso/saml", `{"enabled":true,"idp_metadata_url":"`+idpMeta.URL+`"}`)
	expect(t, r, 200, "metadata from URL")
	if !strings.Contains(r.body, `"idp_entity_id":"https://idp.example.org/metadata"`) {
		t.Fatalf("entity id: %s", r.body)
	}
}

func TestSSOEnforcementAndDiscovery(t *testing.T) {
	f := setup(t, Config{AllowSignup: true})
	f.a.cfg.PublicURL = f.srv.URL
	idp := newTestIdP(t)
	org := f.orgA.ID
	disc := f.do(t, "", "GET", "/api/auth/sso?email=owner@example.com", "")
	expect(t, disc, 200, "discovery")
	if !strings.Contains(disc.body, `"method":"password"`) {
		t.Fatalf("discovery before SAML: %s", disc.body)
	}
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/saml", `{"enabled":true,"idp_metadata_xml":`+jsonString(idp.metadataXML(t))+`,"enforce":true}`), 400, "enforce without a verified domain")
	samlOrg(t, f, idp, `,"enforce":true`)
	disc = f.do(t, "", "GET", "/api/auth/sso?email=Someone@EXAMPLE.com", "")
	if !strings.Contains(disc.body, `"method":"saml"`) || !strings.Contains(disc.body, `"/api/sso/saml/`+org+`/login"`) || !strings.Contains(disc.body, `"password_allowed":false`) {
		t.Fatalf("discovery: %s", disc.body)
	}
	if d := f.do(t, "", "GET", "/api/auth/sso?email=x@elsewhere.org", ""); !strings.Contains(d.body, `"method":"password"`) {
		t.Fatalf("other domain: %s", d.body)
	}
	login := func(email, pw string) resp {
		return f.do(t, "", "POST", "/api/auth/login", `{"email":"`+email+`","password":"`+pw+`"}`)
	}
	for _, c := range []struct{ email, pw string }{{"owner@example.com", testPassword}, {"owner@example.com", "wrong-password-1"},
		{"nobody@example.com", testPassword}, {"op@example.com", "wrong-password-1"}} {
		r := login(c.email, c.pw)
		if r.code != 403 || !strings.Contains(r.body, "sso_required") {
			t.Fatalf("%s/%s: %d %s", c.email, c.pw, r.code, r.body)
		}
	}
	expect(t, login("op@example.com", testPassword), 200, "operator break-glass")
	expect(t, login("outsider@example.com", testPassword), 403, "user of another org on the enforced domain")
	expect(t, f.do(t, "", "POST", "/api/auth/signup", `{"email":"new@example.com","password":"long-enough-password"}`), 403, "signup on an enforced domain")
	// turning enforcement off restores password sign-in
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/saml", `{"enabled":true}`), 200, "enforce off")
	expect(t, login("owner@example.com", testPassword), 200, "password sign-in")
	if r := f.do(t, "", "GET", "/api/auth/config", ""); !strings.Contains(r.body, `"sso_discovery":"/api/auth/sso"`) {
		t.Fatalf("auth config: %s", r.body)
	}
}
