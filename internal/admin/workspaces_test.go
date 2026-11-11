package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

func TestWorkspacesRoleMatrixAndIsolation(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true})
	A := "/api/orgs/" + f.orgA.ID
	var list []workspaceView
	f.do(t, "member", "GET", A+"/workspaces", "").json(t, &list)
	if len(list) != 1 || list[0].ID != store.DefaultWorkspace || list[0].Role != store.RoleMember {
		t.Fatalf("member's workspaces: %+v", list)
	}
	expect(t, f.do(t, "member", "POST", A+"/workspaces", `{"name":"Eng","slug":"eng"}`), 403, "member creates workspace")
	r := f.do(t, "admin", "POST", A+"/workspaces", `{"name":"Eng","slug":"eng"}`)
	expect(t, r, 201, "admin creates workspace")
	var eng workspaceView
	r.json(t, &eng)
	E := A + "/workspaces/" + eng.ID
	expect(t, f.do(t, "admin", "POST", A+"/workspaces", `{"name":"Eng again","slug":"eng"}`), 409, "duplicate slug")
	expect(t, f.do(t, "outsider", "GET", E, ""), 404, "other org's member")
	expect(t, f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/workspaces/"+eng.ID, ""), 404, "workspace through another org")
	expect(t, f.do(t, "member", "GET", E, ""), 404, "member not in eng")

	// member becomes eng's admin; member2 stays outside
	expect(t, f.do(t, "member", "PUT", E+"/members/"+f.users["member2"].ID, `{"role":"member"}`), 404, "non-member manages eng")
	expect(t, f.do(t, "admin", "PUT", E+"/members/"+f.users["member"].ID, `{"role":"admin"}`), 200, "add eng admin")
	expect(t, f.do(t, "admin", "PUT", E+"/members/"+f.users["outsider"].ID, `{"role":"member"}`), 400, "non org member")
	f.do(t, "member", "GET", A+"/workspaces", "").json(t, &list)
	if len(list) != 2 || list[1].ID != eng.ID || list[1].Role != store.RoleAdmin {
		t.Fatalf("member's workspaces after joining: %+v", list)
	}
	var me struct {
		Orgs []struct {
			ID         string          `json:"id"`
			Workspaces []workspaceView `json:"workspaces"`
		} `json:"orgs"`
	}
	f.do(t, "member", "GET", "/api/auth/me", "").json(t, &me)
	if len(me.Orgs) != 1 || len(me.Orgs[0].Workspaces) != 2 {
		t.Fatalf("/me workspaces: %+v", me)
	}

	// the workspace admin manages eng's BYOK provider, model, key, policy and preset
	prov := `{"name":"byok","type":"openai","base_url":"http://127.0.0.1:9/v1","enabled":true,"keys":[{"value":"sk-x"}]`
	expect(t, f.do(t, "member", "POST", A+"/providers", prov+`}`), 403, "member: provider in default workspace")
	expect(t, f.do(t, "member", "POST", A+"/providers", prov+`,"workspace_id":"`+eng.ID+`","shared_across_workspaces":true}`), 403, "workspace admin shares a provider")
	expect(t, f.do(t, "member", "POST", A+"/providers", prov+`,"workspace_id":"nope"}`), 400, "unknown workspace")
	r = f.do(t, "member", "POST", A+"/providers", prov+`,"workspace_id":"`+eng.ID+`"}`)
	expect(t, r, 200, "workspace admin creates provider")
	var p store.Provider
	r.json(t, &p)
	if p.WorkspaceID != eng.ID {
		t.Fatalf("provider workspace: %q", p.WorkspaceID)
	}
	expect(t, f.do(t, "member2", "GET", A+"/providers", ""), 403, "member2 administers no workspace")
	expect(t, f.do(t, "member2", "GET", A+"/providers/"+p.ID, ""), 404, "member2 reads eng provider")
	expect(t, f.do(t, "member", "PUT", A+"/providers/"+p.ID, strings.Replace(prov, "byok", "byok2", 1)+`,"workspace_id":"default"}`), 400, "move provider")
	var ps []store.Provider
	f.do(t, "admin", "GET", A+"/providers?workspace="+eng.ID, "").json(t, &ps)
	if len(ps) != 1 {
		t.Fatalf("admin lists eng providers: %+v", ps)
	}
	expect(t, f.do(t, "member", "POST", A+"/models", `{"id":"eng/m","name":"M","enabled":true,"workspace_id":"`+eng.ID+`","targets":[{"provider_id":"`+p.ID+`","model":"x"}]}`), 200, "eng model")
	if r := f.do(t, "member2", "GET", A+"/models", ""); strings.Contains(r.body, "eng/m") {
		t.Fatalf("member2 sees eng's model: %s", r.body)
	}
	r = f.do(t, "member", "POST", A+"/keys", `{"name":"eng key","workspace_id":"`+eng.ID+`"}`)
	expect(t, r, 201, "eng key")
	var kOut struct {
		Key store.APIKey `json:"key"`
	}
	r.json(t, &kOut)
	if kOut.Key.WorkspaceID != eng.ID {
		t.Fatalf("key workspace: %+v", kOut.Key)
	}
	expect(t, f.do(t, "member2", "POST", A+"/keys", `{"name":"x","workspace_id":"`+eng.ID+`"}`), 404, "member2 key in eng")
	expect(t, f.do(t, "viewer", "POST", A+"/keys", `{"name":"x"}`), 403, "viewer key")
	expect(t, f.do(t, "member2", "PATCH", A+"/keys/"+kOut.Key.ID, `{"revoked":true}`), 404, "member2 revokes eng key")
	expect(t, f.do(t, "member", "POST", A+"/policies", `{"name":"eng cap","workspace_id":"`+eng.ID+`","is_workspace_default":true,"budget":{"limit_usd":5,"interval":"monthly"}}`), 201, "eng policy")
	expect(t, f.do(t, "member", "POST", A+"/policies", `{"name":"org","is_org_default":true}`), 403, "workspace admin sets an org default")
	r = f.do(t, "member", "GET", A+"/keys/"+kOut.Key.ID+"/effective-policy", "")
	if !strings.Contains(r.body, `"scope":"workspace"`) {
		t.Fatalf("effective policy of an eng key: %s", r.body)
	}
	expect(t, f.do(t, "member", "POST", A+"/presets", `{"slug":"eng-fast","model":"eng/m","workspace_id":"`+eng.ID+`"}`), 201, "eng preset")
	expect(t, f.do(t, "member2", "GET", A+"/presets/eng-fast", ""), 404, "member2 reads eng preset")

	// a non-empty workspace is not deleted: 409 with counts
	expect(t, f.do(t, "member", "DELETE", E, ""), 403, "workspace admin deletes workspace")
	r = f.do(t, "admin", "DELETE", E, "")
	expect(t, r, 409, "delete non-empty")
	for _, k := range []string{`"api_keys":1`, `"providers":1`, `"models":1`, `"policies":1`, `"presets":1`} {
		if !strings.Contains(r.body, k) {
			t.Fatalf("409 counts lack %s: %s", k, r.body)
		}
	}
	expect(t, f.do(t, "admin", "DELETE", A+"/workspaces/default", ""), 400, "delete default")
	r = f.do(t, "admin", "POST", A+"/workspaces", `{"name":"Tmp","slug":"tmp"}`)
	var tmp workspaceView
	r.json(t, &tmp)
	expect(t, f.do(t, "admin", "DELETE", A+"/workspaces/"+tmp.ID, ""), 204, "delete empty")

	// usage grouped and filtered by workspace
	r = f.do(t, "viewer", "GET", A+"/usage?group=workspace", "")
	expect(t, r, 200, "usage by workspace")
	if !strings.Contains(r.body, `"workspaces"`) {
		t.Fatalf("usage: %s", r.body)
	}
	expect(t, f.do(t, "viewer", "GET", A+"/usage?workspace="+eng.ID, ""), 200, "usage of one workspace")
	expect(t, f.do(t, "viewer", "GET", A+"/usage/export.csv?group=workspace&workspace="+eng.ID, ""), 200, "usage csv")
	expect(t, f.do(t, "viewer", "GET", A+"/usage/export.csv?group=model&workspace="+eng.ID, ""), 400, "per-model csv of one workspace")
}

func TestMgmtTokenLimitedToWorkspace(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true})
	A := "/api/orgs/" + f.orgA.ID
	eng := &store.Workspace{OrgID: f.orgA.ID, Name: "Eng", Slug: "eng"}
	if err := f.s.CreateWorkspace(context.Background(), eng); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do(t, "admin", "POST", A+"/mgmt-tokens", `{"name":"x","role":"admin","workspace_id":"nope"}`), 400, "unknown workspace")
	r := f.do(t, "admin", "POST", A+"/mgmt-tokens", `{"name":"eng ci","role":"admin","workspace_id":"`+eng.ID+`"}`)
	expect(t, r, 201, "limited token")
	var out struct {
		Secret string `json:"secret"`
	}
	r.json(t, &out)
	tok := out.Secret
	var list []workspaceView
	f.bearer(t, tok, "GET", A+"/workspaces", "").json(t, &list)
	if len(list) != 1 || list[0].ID != eng.ID {
		t.Fatalf("token's workspaces: %+v", list)
	}
	prov := `{"name":"byok","type":"openai","base_url":"http://127.0.0.1:9/v1","enabled":true,"keys":[{"value":"sk-x"}]`
	r = f.bearer(t, tok, "POST", A+"/providers", prov+`}`)
	expect(t, r, 200, "token creates provider (its workspace by default)")
	if !strings.Contains(r.body, `"workspace_id":"`+eng.ID+`"`) {
		t.Fatalf("provider: %s", r.body)
	}
	expect(t, f.bearer(t, tok, "POST", A+"/providers", prov+`,"workspace_id":"default"}`), 404, "token outside its workspace")
	expect(t, f.bearer(t, tok, "POST", A+"/keys", `{"name":"k"}`), 201, "token creates key")
	expect(t, f.bearer(t, tok, "POST", A+"/policies", `{"name":"org","is_org_default":true}`), 403, "token sets an org default")
	expect(t, f.bearer(t, tok, "GET", A+"/members", ""), 403, "org route")
	expect(t, f.bearer(t, tok, "GET", A+"/usage", ""), 403, "org usage")
	expect(t, f.bearer(t, tok, "POST", A+"/workspaces", `{"name":"x","slug":"x"}`), 403, "token creates workspace")
	var keys []store.APIKey
	f.do(t, "admin", "POST", A+"/keys", `{"name":"default key"}`)
	f.bearer(t, tok, "GET", A+"/keys", "").json(t, &keys)
	if len(keys) != 1 || keys[0].WorkspaceID != eng.ID {
		t.Fatalf("token lists keys: %+v", keys)
	}
	expect(t, f.bearer(t, tok, "GET", A+"/keys?workspace=default", ""), 404, "token lists default keys")
}

func TestSCIMGroupMappingAddsWorkspaceMember(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	org := f.orgA.ID
	eng := &store.Workspace{OrgID: org, Name: "Eng", Slug: "eng"}
	if err := f.s.CreateWorkspace(ctx, eng); err != nil {
		t.Fatal(err)
	}
	id := &store.Identity{OrgID: org, GroupMappings: []store.GroupMapping{{Group: "Engineers", Role: store.RoleAdmin, WorkspaceID: eng.ID}}}
	uid := f.users["member"].ID
	if err := f.a.syncWorkspaces(ctx, id, uid, []string{"engineers"}); err != nil {
		t.Fatal(err)
	}
	if wm, err := f.s.GetWorkspaceMember(ctx, org, eng.ID, uid); err != nil || wm.Role != store.RoleAdmin || !wm.IdP {
		t.Fatalf("mapped membership: %+v %v", wm, err)
	}
	if m, _ := f.s.GetMember(ctx, org, uid); m.Role != store.RoleMember {
		t.Fatalf("org role changed by a workspace mapping: %s", m.Role)
	}
	// leaving the group removes the IdP membership; a hand-added one stays
	if err := f.a.syncWorkspaces(ctx, id, uid, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetWorkspaceMember(ctx, org, eng.ID, uid); err == nil {
		t.Fatal("IdP membership kept after leaving the group")
	}
	u2 := f.users["member2"].ID
	if err := f.s.SetWorkspaceMember(ctx, store.WorkspaceMember{OrgID: org, WorkspaceID: eng.ID, UserID: u2, Role: store.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.syncWorkspaces(ctx, id, u2, nil); err != nil {
		t.Fatal(err)
	}
	if wm, err := f.s.GetWorkspaceMember(ctx, org, eng.ID, u2); err != nil || wm.Role != store.RoleViewer {
		t.Fatalf("hand-added membership: %+v %v", wm, err)
	}
}
