package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

func TestPresetCRUDRolesAndAudit(t *testing.T) {
	f := setup(t, Config{})
	base := "/api/orgs/" + f.orgA.ID + "/presets"
	body := `{"slug":"fast","model":"x/m","models":["x/n"],"provider":{"sort":"throughput","zdr":true},"params":{"temperature":0.2,"system":"be brief"}}`
	if r := f.do(t, "member", "POST", base, body); r.code != 403 {
		t.Fatalf("member create: %d", r.code)
	}
	if r := f.do(t, "outsider", "GET", base, ""); r.code != 404 { // non-members do not learn the org exists
		t.Fatalf("outsider list: %d", r.code)
	}
	for _, bad := range []string{
		`{"slug":"Fast","model":"x/m"}`,
		`{"slug":"ok","model":"x/m","provider":{"sort":"fast"}}`,
		`{"slug":"ok","model":"x/m","provider":{"nope":1}}`,
		`{"slug":"ok","model":"x/m","params":{"logit_bias":{}}}`,
	} {
		if r := f.do(t, "admin", "POST", base, bad); r.code != 400 {
			t.Errorf("%s: %d %s", bad, r.code, r.body)
		}
	}
	r := f.do(t, "admin", "POST", base, body)
	var p store.Preset
	r.json(t, &p)
	if r.code != 201 || p.Slug != "fast" || p.Provider.Sort.By != "throughput" || p.ID == "" {
		t.Fatalf("create: %d %s", r.code, r.body)
	}
	if r := f.do(t, "admin", "POST", base, body); r.code != 409 {
		t.Fatalf("duplicate: %d", r.code)
	}
	if r := f.do(t, "viewer", "GET", base, ""); r.code != 200 || !strings.Contains(r.body, `"slug":"fast"`) {
		t.Fatalf("list: %d %s", r.code, r.body)
	}
	if r := f.do(t, "admin", "PUT", base+"/fast", `{"model":"x/o"}`); r.code != 200 || !strings.Contains(r.body, `"model":"x/o"`) || !strings.Contains(r.body, p.ID) {
		t.Fatalf("update: %d %s", r.code, r.body)
	}
	if r := f.do(t, "admin", "PUT", base+"/none", `{"model":"x/o"}`); r.code != 404 {
		t.Fatalf("update missing: %d", r.code)
	}
	if r := f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/presets/fast", ""); r.code != 404 {
		t.Fatalf("other org sees preset: %d", r.code)
	}
	if r := f.do(t, "admin", "DELETE", base+"/fast", ""); r.code != 204 {
		t.Fatalf("delete: %d", r.code)
	}
	if r := f.do(t, "admin", "GET", base+"/fast", ""); r.code != 404 {
		t.Fatalf("deleted: %d", r.code)
	}
	r = f.do(t, "admin", "GET", "/api/orgs/"+f.orgA.ID+"/audit", "")
	for _, a := range []string{"preset.create", "preset.update", "preset.delete"} {
		if !strings.Contains(r.body, a) {
			t.Errorf("audit lacks %s: %s", a, r.body)
		}
	}
}

func TestUsageGroupByEndUser(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	now := time.Now()
	for _, l := range []store.RequestLog{
		{OrgID: f.orgA.ID, KeyID: "k", Model: "m", Status: 200, Cost: 300, At: now, EndUser: "alice"},
		{OrgID: f.orgA.ID, KeyID: "k", Model: "m", Status: 200, Cost: 100, At: now, EndUser: "bob"},
		{OrgID: f.orgA.ID, KeyID: "k", Model: "m", Status: 200, Cost: 200, At: now, EndUser: "alice"},
		{OrgID: f.orgB.ID, KeyID: "k2", Model: "m", Status: 200, Cost: 900, At: now, EndUser: "eve"},
	} {
		f.g.Usage.Record(l)
	}
	f.g.Usage.Close()
	var out struct {
		EndUsers []struct {
			Key      string  `json:"key"`
			Requests int64   `json:"requests"`
			CostUSD  float64 `json:"cost_usd"`
		} `json:"end_users"`
	}
	r := f.do(t, "viewer", "GET", "/api/orgs/"+f.orgA.ID+"/usage?days=2&group=end_user", "")
	r.json(t, &out)
	if r.code != 200 || len(out.EndUsers) != 2 || out.EndUsers[0].Key != "alice" || out.EndUsers[0].Requests != 2 || out.EndUsers[0].CostUSD != 0.0005 || out.EndUsers[1].Key != "bob" {
		t.Fatalf("end users: %d %s", r.code, r.body)
	}
	if r := f.do(t, "viewer", "GET", "/api/orgs/"+f.orgA.ID+"/usage", ""); strings.Contains(r.body, "end_users") {
		t.Fatalf("end users without group: %s", r.body)
	}
	if r := f.do(t, "viewer", "GET", "/api/orgs/"+f.orgA.ID+"/usage?group=model", ""); r.code != 400 {
		t.Fatalf("bad group: %d", r.code)
	}
	if r := f.do(t, "op", "GET", "/api/admin/usage?group=end_user", ""); r.code != 400 {
		t.Fatalf("platform end users: %d", r.code)
	}
	// end-user rollups follow log retention
	if err := f.s.PruneLogs(ctx, -1); err != nil {
		t.Fatal(err)
	}
	if a, _ := f.s.KV.GetAgg(ctx, usage.EndUserPrefix(f.orgA.ID, store.Day(now))+"alice"); a[store.AggRequests] != 0 {
		t.Fatalf("not pruned: %v", a)
	}
}
