package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

func TestPolicyAPI(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	base := "/api/orgs/" + f.orgA.ID + "/policies"
	mk := func(user string) *store.APIKey {
		k := &store.APIKey{OrgID: f.orgA.ID, Name: user, CreatedBy: f.users[user].ID}
		if err := f.s.CreateKey(ctx, k, gateway.HashKey("sk-gw-"+user+"-x")); err != nil {
			t.Fatal(err)
		}
		return k
	}
	memberKey, member2Key := mk("member"), mk("member2")
	otherKey := &store.APIKey{OrgID: f.orgB.ID, Name: "b"}
	if err := f.s.CreateKey(ctx, otherKey, gateway.HashKey("sk-gw-b")); err != nil {
		t.Fatal(err)
	}

	// role and validation
	body := fmt.Sprintf(`{"name":"eng","member_ids":[%q],"allowed_models":["openai/*"],"require_zdr":true,
		"budget":{"limit_usd":2.5,"interval":"weekly"},"filters":[{"name":"pin","pattern":"\\d{4}","action":"redact"}]}`, f.users["member"].ID)
	if r := f.do(t, "member", "POST", base, body); r.code != 403 {
		t.Fatalf("member create: %d", r.code)
	}
	if r := f.do(t, "outsider", "GET", base, ""); r.code != 404 {
		t.Fatalf("outsider list: %d", r.code)
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"name":"x","member_ids":[%q]}`, f.users["outsider"].ID), // not a member of A
		fmt.Sprintf(`{"name":"x","key_ids":[%q]}`, otherKey.ID),               // key of org B
		`{"name":"x","filters":[{"pattern":"(","action":"block"}]}`,
		`{"name":"x","budget":{"limit_usd":1,"interval":"yearly"}}`,
		`{"name":"x","budget":{"limit_usd":0,"interval":"daily"}}`,
	} {
		if r := f.do(t, "admin", "POST", base, bad); r.code != 400 {
			t.Fatalf("%s: %d %s", bad, r.code, r.body)
		}
	}
	r := f.do(t, "admin", "POST", base, body)
	if r.code != 201 {
		t.Fatalf("create: %d %s", r.code, r.body)
	}
	var p store.Policy
	r.json(t, &p)
	if p.Budget == nil || p.Budget.LimitMicros != 2_500_000 || p.OrgID != f.orgA.ID {
		t.Fatalf("created: %+v", p)
	}
	if r := f.do(t, "admin", "GET", base, ""); r.code != 200 || !strings.Contains(r.body, `"eng"`) {
		t.Fatalf("list: %d %s", r.code, r.body)
	}
	// org B's admin cannot touch org A's policy through org B's path
	if r := f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/policies/"+p.ID, ""); r.code != 404 {
		t.Fatalf("cross-org get: %d", r.code)
	}

	// effective policy: member sees own key, not another member's; admin sees all
	eff := "/api/orgs/" + f.orgA.ID + "/keys/" + memberKey.ID + "/effective-policy"
	r = f.do(t, "member", "GET", eff, "")
	if r.code != 200 {
		t.Fatalf("effective: %d %s", r.code, r.body)
	}
	var ep gateway.EffectivePolicy
	r.json(t, &ep)
	if len(ep.Policies) != 1 || ep.Policies[0].Name != "eng" || !ep.RequireZDR || len(ep.Budgets) != 1 || ep.Budgets[0].Scope != "member" || ep.Budgets[0].ResetsAt.IsZero() {
		t.Fatalf("effective: %s", r.body)
	}
	if r := f.do(t, "member", "GET", "/api/orgs/"+f.orgA.ID+"/keys/"+member2Key.ID+"/effective-policy", ""); r.code != 404 {
		t.Fatalf("member reads another member's key policy: %d", r.code)
	}
	r = f.do(t, "admin", "GET", "/api/orgs/"+f.orgA.ID+"/keys/"+member2Key.ID+"/effective-policy", "")
	if r.code != 200 || !strings.Contains(r.body, `"policies":[]`) {
		t.Fatalf("admin effective (no policy): %d %s", r.code, r.body)
	}

	// update (full replace) then delete; both audited
	r = f.do(t, "admin", "PUT", base+"/"+p.ID, `{"name":"eng2","is_org_default":true,"opted_out_key_ids":["`+member2Key.ID+`"]}`)
	if r.code != 200 || !strings.Contains(r.body, `"eng2"`) || strings.Contains(r.body, "require_zdr") {
		t.Fatalf("put: %d %s", r.code, r.body)
	}
	if r := f.do(t, "admin", "DELETE", base+"/"+p.ID, ""); r.code != 204 {
		t.Fatalf("delete: %d", r.code)
	}
	if r := f.do(t, "admin", "DELETE", base+"/"+p.ID, ""); r.code != 404 {
		t.Fatalf("delete again: %d", r.code)
	}
	audit, _ := f.s.ListAudit(ctx, f.orgA.ID, 20)
	var acts []string
	for _, a := range audit {
		acts = append(acts, a.Action)
	}
	if got := strings.Join(acts, ","); !strings.Contains(got, "policy.create") || !strings.Contains(got, "policy.update") || !strings.Contains(got, "policy.delete") {
		t.Fatalf("audit: %s", got)
	}
}

func TestProviderPrivacy(t *testing.T) {
	f := setup(t, Config{})
	for _, bad := range []string{
		`{"name":"p","type":"openai","keys":[],"privacy":{"zdr":true,"retention_days":30}}`,
		`{"name":"p","type":"openai","keys":[],"privacy":{"region":"EU West!"}}`,
	} {
		if r := f.do(t, "op", "POST", "/api/admin/providers", bad); r.code != 400 {
			t.Fatalf("%s: %d %s", bad, r.code, r.body)
		}
	}
	r := f.do(t, "op", "POST", "/api/admin/providers", `{"name":"p","type":"openai","enabled":true,"keys":[{"value":"k"}],"privacy":{"zdr":true,"trains_on_data":false,"region":"eu"}}`)
	if r.code != 200 {
		t.Fatalf("put: %d %s", r.code, r.body)
	}
	var p store.Provider
	r.json(t, &p)
	if r := f.do(t, "op", "PUT", "/api/admin/models/acme/m1", `{"name":"m","enabled":true,"targets":[{"provider_id":"`+p.ID+`","model":"up"}]}`); r.code != 200 {
		t.Fatalf("model: %d %s", r.code, r.body)
	}
	if err := f.g.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	r = f.do(t, "", "GET", "/api/public/models/acme/m1", "")
	if r.code != 200 || !strings.Contains(r.body, `"privacy":{"zdr":true,"trains_on_data":false,"region":"europe"}`) { // eu is stored as europe
		t.Fatalf("public model privacy: %d %s", r.code, r.body)
	}
}
