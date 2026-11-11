package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// W1 integration: a management token (C) manages presets (B) and policies (A) of its org, and
// is no credential for the model API.
func TestMgmtTokenManagesPresetsAndPolicies(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	r := f.do(t, "admin", "POST", A+"/mgmt-tokens", `{"name":"ci","role":"admin"}`)
	expect(t, r, 201, "create token")
	var out struct {
		Secret string `json:"secret"`
	}
	r.json(t, &out)

	r = f.bearer(t, out.Secret, "POST", A+"/presets", `{"slug":"zdr-fast","model":"x/m","models":["x/n"],"provider":{"zdr":true,"order":["alpha"]}}`)
	expect(t, r, 201, "token creates preset")
	r = f.bearer(t, out.Secret, "POST", A+"/policies", `{"name":"zdr only","is_org_default":true,"require_zdr":true}`)
	expect(t, r, 201, "token creates policy")
	var pol store.Policy
	r.json(t, &pol)
	if !pol.RequireZDR || !pol.IsOrgDefault {
		t.Fatalf("policy: %s", r.body)
	}
	expect(t, f.bearer(t, out.Secret, "GET", A+"/presets/zdr-fast", ""), 200, "token reads preset")
	expect(t, f.bearer(t, out.Secret, "GET", A+"/policies", ""), 200, "token lists policies")
	expect(t, f.bearer(t, out.Secret, "POST", "/api/orgs/"+f.orgB.ID+"/policies", `{"name":"x","is_org_default":true}`), 404, "token on other org")

	// not an API key: /v1 refuses it
	expect(t, f.bearer(t, out.Secret, "GET", "/v1/models", ""), 401, "token on /v1/models")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"x/m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+out.Secret)
	rec := httptest.NewRecorder()
	f.g.ChatCompletions(rec, req)
	if rec.Code != 401 {
		t.Fatalf("token on /v1/chat/completions: %d %s", rec.Code, rec.Body)
	}
}
