package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// wsAdminFixture makes "member" the admin of a new workspace "eng" (not an org admin).
func wsAdminFixture(t *testing.T) (*fixture, string, string) {
	t.Helper()
	f := setup(t, Config{AllowPrivateURLs: true})
	eng := &store.Workspace{OrgID: f.orgA.ID, Name: "Eng", Slug: "eng"}
	ctx := context.Background()
	if err := f.s.CreateWorkspace(ctx, eng); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetWorkspaceMember(ctx, store.WorkspaceMember{OrgID: f.orgA.ID, WorkspaceID: eng.ID, UserID: f.users["member"].ID, Role: store.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	return f, "/api/orgs/" + f.orgA.ID, eng.ID
}

// M17: provider privacy (zdr, training, retention, region) is what org policies (require_zdr,
// deny_data_collection, allowed_regions) trust, so only org admins declare it.
func TestWorkspaceAdminCannotDeclareProviderPrivacy(t *testing.T) {
	f, A, eng := wsAdminFixture(t)
	prov := `{"name":"byok","type":"openai","base_url":"http://127.0.0.1:9/v1","enabled":true,"keys":[{"value":"sk-x"}],"workspace_id":"` + eng + `"`
	expect(t, f.do(t, "member", "POST", A+"/providers", prov+`,"privacy":{"zdr":true,"region":"eu"}}`), 403, "workspace admin declares zdr")
	r := f.do(t, "member", "POST", A+"/providers", prov+`}`)
	expect(t, r, 200, "workspace admin creates provider without privacy")
	var p store.Provider
	r.json(t, &p)
	expect(t, f.do(t, "member", "PUT", A+"/providers/"+p.ID, prov+`,"privacy":{"zdr":true}}`), 403, "workspace admin adds zdr")
	expect(t, f.do(t, "admin", "PUT", A+"/providers/"+p.ID, prov+`,"privacy":{"zdr":true,"region":"eu"}}`), 200, "org admin declares privacy")
	r = f.do(t, "member", "PUT", A+"/providers/"+p.ID, strings.Replace(prov, "byok", "byok2", 1)+`}`)
	expect(t, r, 200, "workspace admin edits other fields")
	r.json(t, &p)
	if p.Privacy == nil || !p.Privacy.ZDR || p.Privacy.Region != "europe" {
		t.Fatalf("privacy after a workspace admin's edit: %+v", p.Privacy)
	}
	expect(t, f.do(t, "member", "PUT", A+"/providers/"+p.ID, prov+`,"privacy":{"zdr":true,"region":"eu"}}`), 200, "same privacy resent")
}

// M18: content (prompts and completions) is readable by org admins only, so only they may
// export it; and a new endpoint drops the stored headers (L26) so secrets entered by one admin
// are not sent to an endpoint another chose.
func TestTraceExportContentAndHeaders(t *testing.T) {
	f, A, eng := wsAdminFixture(t)
	base := A + "/trace-exports"
	ep := "http://127.0.0.1:9/v1/traces"
	expect(t, f.do(t, "member", "POST", base, `{"name":"t","type":"otlp","endpoint":"`+ep+`","workspace_id":"`+eng+`","include_content":true}`), 403, "workspace admin exports content")
	r := f.do(t, "member", "POST", base, `{"name":"t","type":"otlp","endpoint":"`+ep+`","workspace_id":"`+eng+`"}`)
	expect(t, r, 201, "workspace admin creates export")
	var te store.TraceExport
	r.json(t, &te)
	expect(t, f.do(t, "member", "PATCH", base+"/"+te.ID, `{"include_content":true}`), 403, "workspace admin turns content on")
	r = f.do(t, "admin", "POST", base, `{"name":"c","type":"otlp","endpoint":"`+ep+`","workspace_id":"`+eng+`","include_content":true,"headers":{"Authorization":"Bearer s3cret"}}`)
	expect(t, r, 201, "org admin exports content")
	var ce store.TraceExport
	r.json(t, &ce)
	expect(t, f.do(t, "member", "PATCH", base+"/"+ce.ID, `{"endpoint":"http://127.0.0.1:8/x"}`), 403, "workspace admin redirects a content export")
	expect(t, f.do(t, "member", "PATCH", base+"/"+ce.ID, `{"enabled":false}`), 403, "workspace admin edits a content export")
	expect(t, f.do(t, "member", "DELETE", base+"/"+ce.ID, ""), 204, "workspace admin deletes a content export")

	r = f.do(t, "admin", "POST", base, `{"name":"h","type":"otlp","endpoint":"`+ep+`","headers":{"Authorization":"Bearer s3cret"}}`)
	expect(t, r, 201, "export with headers")
	r.json(t, &te)
	r = f.do(t, "admin", "PATCH", base+"/"+te.ID, `{"endpoint":"http://127.0.0.1:8/x"}`)
	expect(t, r, 200, "new endpoint")
	if !strings.Contains(r.body, `"header_names":null`) && !strings.Contains(r.body, `"header_names":[]`) {
		t.Fatalf("headers kept for a new endpoint: %s", r.body)
	}
	got, err := f.s.GetTraceExport(context.Background(), f.orgA.ID, te.ID)
	if err != nil || len(got.Headers) != 0 {
		t.Fatalf("stored headers after a new endpoint: %v %v", got.Headers, err)
	}
	r = f.do(t, "admin", "PATCH", base+"/"+te.ID, `{"endpoint":"http://127.0.0.1:7/x","headers":{"Authorization":"Bearer new"}}`)
	expect(t, r, 200, "new endpoint with headers")
	if !strings.Contains(r.body, `"header_names":["Authorization"]`) {
		t.Fatalf("headers sent with the new endpoint: %s", r.body)
	}
}
