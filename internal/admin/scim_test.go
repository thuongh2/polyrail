package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

// scimClient replays what Okta and Entra ID send to a SCIM 2.0 server.
type scimClient struct {
	t     *testing.T
	base  string
	token string
}

func (c scimClient) do(method, path, body string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/scim+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{"_raw": string(raw)}
	}
	return res.StatusCode, out
}

func (c scimClient) must(code int, method, path, body string) map[string]any {
	c.t.Helper()
	got, out := c.do(method, path, body)
	if got != code {
		c.t.Fatalf("%s %s: %d, want %d: %v", method, path, got, code, out)
	}
	return out
}

func TestSCIMProvisioning(t *testing.T) {
	f := setup(t, Config{})
	f.a.cfg.PublicURL = f.srv.URL
	ctx := context.Background()
	org := f.orgA.ID
	d := &store.Domain{OrgID: org, Name: "example.com"}
	if err := f.s.CreateDomain(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.VerifyDomain(ctx, org, d.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/scim", `{"enabled":true}`), 400, "enable without a token")
	expect(t, f.do(t, "admin", "POST", "/api/orgs/"+org+"/sso/scim/token", ""), 403, "admin makes a SCIM token")
	r := f.do(t, "owner", "POST", "/api/orgs/"+org+"/sso/scim/token", "")
	expect(t, r, 201, "scim token")
	var tok struct {
		Token string `json:"token"`
		Base  string `json:"scim_base_url"`
	}
	r.json(t, &tok)
	if !strings.HasPrefix(tok.Token, ScimPrefix) || tok.Base != f.srv.URL+"/scim/v2/"+org {
		t.Fatalf("token: %+v", tok)
	}
	if sso := f.do(t, "owner", "GET", "/api/orgs/"+org+"/sso", ""); strings.Contains(sso.body, tok.Token) {
		t.Fatal("SCIM token returned after creation")
	}
	expect(t, f.do(t, "owner", "PUT", "/api/orgs/"+org+"/sso/group-mappings", `[{"group":"Polyrail Admins","role":"admin"}]`), 200, "mappings")
	c := scimClient{t, tok.Base, tok.Token}

	// authentication: no token, a wrong token, org A's token on org B
	for _, x := range []scimClient{{t, tok.Base, ""}, {t, tok.Base, ScimPrefix + "wrong"}, {t, f.srv.URL + "/scim/v2/" + f.orgB.ID, tok.Token}} {
		if code, _ := x.do("GET", "/Users", ""); code != 401 {
			t.Fatalf("unauthenticated SCIM: %d", code)
		}
	}
	c.must(200, "GET", "/ServiceProviderConfig", "")
	if out := c.must(200, "GET", "/ResourceTypes", ""); out["totalResults"] != float64(2) {
		t.Fatalf("resource types: %v", out)
	}
	c.must(200, "GET", "/Schemas", "")

	// Okta: look the user up, then create
	if out := c.must(200, "GET", `/Users?filter=userName%20eq%20%22alice%40example.com%22&startIndex=1&count=100`, ""); out["totalResults"] != float64(0) {
		t.Fatalf("lookup before create: %v", out)
	}
	alice := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice@example.com",
		"name":{"givenName":"Alice","familyName":"Doe"},"emails":[{"primary":true,"value":"alice@example.com","type":"work"}],
		"displayName":"Alice Doe","locale":"en-US","externalId":"00u1","groups":[],"active":true}`
	out := c.must(201, "POST", "/Users", alice)
	aliceID, _ := out["id"].(string)
	if aliceID == "" || out["active"] != true {
		t.Fatalf("create: %v", out)
	}
	c.must(409, "POST", "/Users", alice)
	c.must(400, "POST", "/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"eve@gmail.com","active":true}`)
	if out := c.must(200, "GET", `/Users?filter=userName%20eq%20%22ALICE%40example.com%22`, ""); out["totalResults"] != float64(1) {
		t.Fatalf("lookup after create: %v", out)
	}
	u, _ := f.s.GetUser(ctx, aliceID)
	if u.Name != "Alice Doe" || u.Email != "alice@example.com" {
		t.Fatalf("user: %+v", u)
	}
	role := func(uid string) store.Role {
		m, err := f.s.GetMember(ctx, org, uid)
		if err != nil {
			return ""
		}
		return m.Role
	}
	if role(aliceID) != store.RoleMember {
		t.Fatalf("role after create: %s", role(aliceID))
	}

	// groups: Okta push with members, then remove by filter; Entra add with a value list
	out = c.must(201, "POST", "/Groups", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Polyrail Admins","members":[{"value":"`+aliceID+`"},{"value":"nobody"}]}`)
	gid, _ := out["id"].(string)
	if role(aliceID) != store.RoleAdmin {
		t.Fatalf("role after group push: %s", role(aliceID))
	}
	c.must(200, "PATCH", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"remove","path":"members[value eq \"`+aliceID+`\"]"}]}`)
	if role(aliceID) != store.RoleMember {
		t.Fatalf("role after group removal: %s", role(aliceID))
	}
	c.must(200, "PATCH", "/Groups/"+gid, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Add","path":"members","value":[{"value":"`+aliceID+`"}]}]}`)
	if role(aliceID) != store.RoleAdmin {
		t.Fatalf("role after Entra add: %s", role(aliceID))
	}
	if out := c.must(200, "GET", `/Groups?filter=displayName%20eq%20%22Polyrail%20Admins%22`, ""); out["totalResults"] != float64(1) {
		t.Fatalf("group lookup: %v", out)
	}

	// Entra: attribute updates with paths; the email cannot change
	c.must(200, "PATCH", "/Users/"+aliceID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[
		{"op":"Replace","path":"name.givenName","value":"Alicia"},
		{"op":"Replace","path":"emails[type eq \"work\"].value","value":"alice@example.com"},
		{"op":"Add","path":"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User:department","value":"R&D"}]}`)
	c.must(400, "PATCH", "/Users/"+aliceID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"emails[type eq \"work\"].value","value":"alice@evil.com"}]}`)

	// credentials alice made in the org
	keyHash := gateway.HashKey("sk-alice")
	if err := f.s.CreateKey(ctx, &store.APIKey{OrgID: org, Name: "alice", CreatedBy: aliceID}, keyHash); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CreateMgmtToken(ctx, &store.MgmtToken{OrgID: org, Name: "ci", Role: store.RoleViewer, CreatedBy: aliceID}, gateway.HashKey("pr-mgmt-alice")); err != nil {
		t.Fatal(err)
	}

	// Okta deprovision: active=false without a path
	out = c.must(200, "PATCH", "/Users/"+aliceID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":false}}]}`)
	if out["active"] != false || role(aliceID) != "" {
		t.Fatalf("deprovision: %v role=%s", out, role(aliceID))
	}
	k, _ := f.s.KeyByHash(ctx, keyHash)
	mt, _ := f.s.MgmtTokenByHash(ctx, gateway.HashKey("pr-mgmt-alice"))
	if !k.Revoked || !mt.Revoked {
		t.Fatalf("credentials not revoked: key=%v token=%v", k.Revoked, mt.Revoked)
	}
	id, _ := f.s.GetIdentity(ctx, org)
	if u, code, _ := f.a.samlUser(ctx, id, "alice@example.com", "", nil); u != nil || code != 403 {
		t.Fatalf("SAML sign-in of a deprovisioned user: %v %d", u, code)
	}
	// reactivation restores membership (with the group role) but not the revoked key
	c.must(200, "PATCH", "/Users/"+aliceID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`)
	if k, _ := f.s.KeyByHash(ctx, keyHash); role(aliceID) != store.RoleAdmin || !k.Revoked {
		t.Fatalf("reactivate: role=%s revoked=%v", role(aliceID), k.Revoked)
	}
	// PUT replaces attributes
	out = c.must(200, "PUT", "/Users/"+aliceID, `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice@example.com","name":{"givenName":"Al","familyName":"Doe"},"emails":[{"primary":true,"value":"alice@example.com"}],"active":true}`)
	if n, _ := out["name"].(map[string]any); n["givenName"] != "Al" {
		t.Fatalf("put: %v", out)
	}

	// an existing account on the domain is linked; the last owner cannot be deprovisioned
	out = c.must(201, "POST", "/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"owner@example.com","active":true}`)
	ownerID, _ := out["id"].(string)
	if ownerID != f.users["owner"].ID || role(ownerID) != store.RoleOwner {
		t.Fatalf("link owner: %v role=%s", out, role(ownerID))
	}
	c.must(400, "PATCH", "/Users/"+ownerID, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","value":{"active":false}}]}`)
	if role(ownerID) != store.RoleOwner {
		t.Fatal("last owner removed")
	}

	// DELETE deprovisions and hides the user; the group loses the member
	c.must(204, "DELETE", "/Users/"+aliceID, "")
	c.must(404, "GET", "/Users/"+aliceID, "")
	if role(aliceID) != "" {
		t.Fatal("membership kept after DELETE")
	}
	if g, _ := f.s.GetSCIMGroup(ctx, org, gid); len(g.Members) != 0 {
		t.Fatalf("group members after DELETE: %v", g.Members)
	}
	c.must(204, "DELETE", "/Groups/"+gid, "")
	audit := f.do(t, "owner", "GET", "/api/orgs/"+org+"/audit", "")
	for _, act := range []string{"scim.user.create", "scim.user.deprovision", "scim.member.role", "scim.group.create", "sso.scim.token"} {
		if !strings.Contains(audit.body, act) {
			t.Fatalf("audit lacks %s", act)
		}
	}
}
