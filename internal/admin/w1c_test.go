package admin

import (
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// bearer sends a request with an Authorization bearer and no cookie and no CSRF header.
func (f *fixture) bearer(t *testing.T, tok, method, path, body string) resp {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, string(raw), res.Header}
}

func TestKeyVisibilityByRole(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	mine, _ := f.createKey(t, "member", f.orgA.ID)
	theirs, _ := f.createKey(t, "member2", f.orgA.ID)
	f.createKey(t, "admin", f.orgA.ID)
	for as, want := range map[string]int{"owner": 3, "admin": 3, "op": 3, "member": 1, "member2": 1, "viewer": 0} {
		var keys []store.APIKey
		r := f.do(t, as, "GET", A+"/keys", "")
		expect(t, r, 200, as+" list keys")
		r.json(t, &keys)
		if len(keys) != want {
			t.Errorf("%s sees %d keys, want %d", as, len(keys), want)
		}
		if as == "member" && keys[0].ID != mine {
			t.Errorf("member sees someone else's key")
		}
	}
	// members rotate and revoke only their own keys; viewers neither; admins any
	expect(t, f.do(t, "member", "POST", A+"/keys/"+theirs+"/rotate", ""), 403, "member rotates other key")
	expect(t, f.do(t, "member", "PATCH", A+"/keys/"+theirs, `{"revoked":true}`), 403, "member revokes other key")
	expect(t, f.do(t, "viewer", "POST", A+"/keys/"+mine+"/rotate", ""), 403, "viewer rotates")
	expect(t, f.do(t, "member", "POST", A+"/keys/"+mine+"/rotate", ""), 200, "member rotates own key")
	expect(t, f.do(t, "admin", "POST", A+"/keys/"+theirs+"/rotate", ""), 200, "admin rotates any key")
	expect(t, f.do(t, "outsider", "POST", A+"/keys/"+mine+"/rotate", ""), 404, "outsider rotates")
}

func TestKeyRotateGraceAndSettings(t *testing.T) {
	f := setup(t, Config{})
	A := "/api/orgs/" + f.orgA.ID
	r := f.do(t, "admin", "POST", A+"/keys", `{"name":"ci","rpm":9,"models":["x/y"]}`)
	expect(t, r, 201, "create")
	var created struct {
		Key    store.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	r.json(t, &created)
	id, old := created.Key.ID, created.Secret
	call := func(secret string) int {
		req, _ := http.NewRequest("GET", f.srv.URL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if call(old) != 200 { // warm the auth cache: rotation must still cut the old secret off
		t.Fatal("fresh key rejected")
	}
	expect(t, f.do(t, "admin", "POST", A+"/keys/"+id+"/rotate", `{"grace_seconds":86401}`), 400, "grace too long")
	expect(t, f.do(t, "admin", "POST", A+"/keys/"+id+"/rotate", `{"grace_seconds":-1}`), 400, "negative grace")

	r = f.do(t, "admin", "POST", A+"/keys/"+id+"/rotate", `{"grace_seconds":3600}`)
	expect(t, r, 200, "rotate with grace")
	var rot struct {
		Key    store.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	r.json(t, &rot)
	if rot.Secret == old || !strings.HasPrefix(rot.Secret, "sk-gw-") || rot.Key.ID != id || rot.Key.RPM != 9 || len(rot.Key.Models) != 1 ||
		rot.Key.Name != "ci" || rot.Key.PrevValidUntil == nil || rot.Key.Prefix != rot.Secret[:12] {
		t.Fatalf("rotate result: %+v", rot)
	}
	if strings.Contains(r.body, "hash") {
		t.Fatalf("hash in response: %s", r.body)
	}
	if call(old) != 200 || call(rot.Secret) != 200 {
		t.Fatal("old secret must work during grace, new one at once")
	}
	r = f.do(t, "admin", "POST", A+"/keys/"+id+"/rotate", "")
	expect(t, r, 200, "rotate without grace")
	var rot2 struct{ Secret string }
	r.json(t, &rot2)
	if call(old) != 401 || call(rot.Secret) != 401 || call(rot2.Secret) != 200 {
		t.Fatal("rotation without grace must end every older secret at once")
	}
	var audit []store.AuditEntry
	f.do(t, "admin", "GET", A+"/audit", "").json(t, &audit)
	n := 0
	for _, e := range audit {
		if e.Action == "key.rotate" && e.Target == id {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("key.rotate audit entries: %d", n)
	}
	expect(t, f.do(t, "admin", "PATCH", A+"/keys/"+id, `{"revoked":true}`), 200, "revoke")
	expect(t, f.do(t, "admin", "POST", A+"/keys/"+id+"/rotate", ""), 400, "rotate revoked key")
}

func TestMgmtTokens(t *testing.T) {
	f := setup(t, Config{})
	A, B := "/api/orgs/"+f.orgA.ID, "/api/orgs/"+f.orgB.ID
	expect(t, f.do(t, "member", "POST", A+"/mgmt-tokens", `{"name":"x","role":"viewer"}`), 403, "member creates token")
	expect(t, f.do(t, "admin", "POST", A+"/mgmt-tokens", `{"name":"x","role":"owner"}`), 400, "owner-role token")
	expect(t, f.do(t, "admin", "POST", A+"/mgmt-tokens", `{"name":"","role":"viewer"}`), 400, "nameless token")
	mint := func(as, role string) (store.MgmtToken, string) {
		r := f.do(t, as, "POST", A+"/mgmt-tokens", `{"name":"ci-`+role+`","role":"`+role+`"}`)
		expect(t, r, 201, "create token")
		var out struct {
			Token  store.MgmtToken `json:"token"`
			Secret string          `json:"secret"`
		}
		r.json(t, &out)
		if !strings.HasPrefix(out.Secret, MgmtPrefix) || strings.Contains(r.body, `"hash"`) {
			t.Fatalf("token create: %s", r.body)
		}
		return out.Token, out.Secret
	}
	adminTok, admin := mint("admin", "admin")
	_, viewer := mint("owner", "viewer")

	// acts as an org admin without cookie or CSRF header
	expect(t, f.bearer(t, admin, "GET", A+"/keys", ""), 200, "token lists keys")
	r := f.bearer(t, admin, "POST", A+"/keys", `{"name":"from-ci"}`)
	expect(t, r, 201, "token creates key without CSRF")
	expect(t, f.bearer(t, admin, "GET", A+"/audit", ""), 200, "admin token reads audit")
	expect(t, f.bearer(t, viewer, "GET", A+"/audit", ""), 403, "viewer token reads audit")
	expect(t, f.bearer(t, viewer, "POST", A+"/keys", `{"name":"v"}`), 403, "viewer token creates key")
	// scope confinement: other org, platform routes, org listing, /v1, token management
	expect(t, f.bearer(t, admin, "GET", B, ""), 404, "token on other org")
	expect(t, f.bearer(t, admin, "GET", B+"/keys", ""), 404, "token on other org keys")
	expect(t, f.bearer(t, admin, "GET", "/api/admin/providers", ""), 401, "token on platform route")
	expect(t, f.bearer(t, admin, "POST", "/api/admin/models", `{"id":"x/y"}`), 401, "token on platform mutation")
	expect(t, f.bearer(t, admin, "GET", "/api/orgs", ""), 401, "token lists orgs")
	expect(t, f.bearer(t, admin, "GET", "/api/auth/me", ""), 401, "token on /me")
	expect(t, f.bearer(t, admin, "POST", A+"/credits", `{"usd":5}`), 401, "token on operator route")
	expect(t, f.bearer(t, admin, "GET", "/v1/models", ""), 401, "token on /v1")
	expect(t, f.bearer(t, admin, "GET", A+"/mgmt-tokens", ""), 403, "token manages tokens")
	expect(t, f.bearer(t, MgmtPrefix+"nope", "GET", A+"/keys", ""), 401, "unknown token")
	// CSRF stays required for cookie sessions
	expect(t, f.req(t, "admin", "POST", A+"/keys", `{"name":"c"}`, false), 403, "cookie without CSRF")

	// audit actor is token:<name>
	var audit []store.AuditEntry
	f.do(t, "admin", "GET", A+"/audit", "").json(t, &audit)
	found := false
	for _, e := range audit {
		if e.Action == "key.create" && e.Actor == "token:ci-admin" {
			found = true
		}
	}
	if !found {
		t.Fatal("key.create by token not audited as token:ci-admin")
	}
	// the creator's current role caps the token: demote admin -> member
	expect(t, f.do(t, "owner", "PUT", A+"/members/"+f.users["admin"].ID, `{"role":"member"}`), 204, "demote creator")
	expect(t, f.bearer(t, admin, "GET", A+"/audit", ""), 403, "token of demoted creator")
	expect(t, f.do(t, "owner", "DELETE", A+"/members/"+f.users["admin"].ID, ""), 204, "remove creator")
	expect(t, f.bearer(t, admin, "GET", A+"/keys", ""), 401, "token of removed creator")
	f.s.SetMember(context.Background(), store.Member{OrgID: f.orgA.ID, UserID: f.users["admin"].ID, Role: store.RoleAdmin})
	expect(t, f.bearer(t, admin, "GET", A+"/audit", ""), 200, "creator back")

	// list shows last use and no secret material; revoke and expiry end the token
	var list []store.MgmtToken
	r = f.do(t, "owner", "GET", A+"/mgmt-tokens", "")
	r.json(t, &list)
	if len(list) != 2 || strings.Contains(r.body, "hash") {
		t.Fatalf("token list: %s", r.body)
	}
	for _, tk := range list {
		if tk.ID == adminTok.ID && tk.LastUsedAt == nil {
			t.Fatal("last_used_at not set")
		}
	}
	expect(t, f.do(t, "admin", "PATCH", A+"/mgmt-tokens/"+adminTok.ID, `{"revoked":true}`), 200, "revoke token")
	expect(t, f.bearer(t, admin, "GET", A+"/keys", ""), 401, "revoked token")
	_, exp := mint("owner", "member")
	expect(t, f.bearer(t, exp, "GET", A+"/keys", ""), 200, "fresh token")
	all, _ := f.s.ListMgmtTokens(context.Background(), f.orgA.ID)
	for _, tk := range all {
		if tk.Name == "ci-member" {
			past := time.Now().Add(-time.Minute)
			f.s.UpdateMgmtToken(context.Background(), f.orgA.ID, tk.ID, func(x *store.MgmtToken) { x.ExpiresAt = &past })
		}
	}
	expect(t, f.bearer(t, exp, "GET", A+"/keys", ""), 401, "expired token")
	expect(t, f.do(t, "admin", "DELETE", A+"/mgmt-tokens/"+adminTok.ID, ""), 204, "delete token")
	expect(t, f.do(t, "outsider", "GET", A+"/mgmt-tokens", ""), 404, "outsider lists tokens")
}

func readCSV(t *testing.T, r resp) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(r.body)).ReadAll()
	if err != nil {
		t.Fatalf("csv (%d): %v: %s", r.code, err, r.body)
	}
	return recs
}

func TestUsageAndLogExport(t *testing.T) {
	f := setup(t, Config{LogRetentionDays: 30})
	ctx := context.Background()
	A := "/api/orgs/" + f.orgA.ID
	kid, _ := f.createKey(t, "member", f.orgA.ID)
	now := time.Now().UTC()
	day := store.Day(now)
	var one store.Agg
	one[store.AggRequests], one[store.AggCostMicros], one[store.AggLatencyMs] = 2, 1_500_000, 100
	if err := f.s.KV.AddAgg(ctx, map[string]store.Agg{
		"agg/" + f.orgA.ID + "/" + day + "/t":            one,
		"agg/" + f.orgA.ID + "/" + day + "/m/=HYPERLINK": one,
		"agg/" + f.orgA.ID + "/" + day + "/k/" + kid:     one,
		"agg/_/" + day + "/m/=HYPERLINK":                 one,
	}); err != nil {
		t.Fatal(err)
	}
	r := f.do(t, "viewer", "GET", A+"/usage/export.csv?group=creator", "")
	expect(t, r, 200, "creator export")
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("content type %q", ct)
	}
	recs := readCSV(t, r)
	if len(recs) != 2 || recs[0][1] != "creator" || recs[1][1] != "member@example.com" || recs[1][2] != "2" || recs[1][6] != "1.500000" || recs[1][7] != "50" {
		t.Fatalf("creator export: %v", recs)
	}
	recs = readCSV(t, f.do(t, "viewer", "GET", A+"/usage/export.csv?group=model", ""))
	if len(recs) != 2 || recs[1][1] != "'=HYPERLINK" {
		t.Fatalf("model export must defuse formulas: %v", recs)
	}
	recs = readCSV(t, f.do(t, "viewer", "GET", A+"/usage/export.csv?group=day&from="+now.AddDate(0, 0, -1).Format("2006-01-02")+"&to="+now.Format("2006-01-02"), ""))
	if len(recs) != 3 || len(recs[0]) != 10 || recs[2][1] != "2" || recs[1][1] != "0" {
		t.Fatalf("day export: %v", recs)
	}
	expect(t, f.do(t, "viewer", "GET", A+"/usage/export.csv?group=nope", ""), 400, "bad group")
	expect(t, f.do(t, "viewer", "GET", A+"/usage/export.csv?from=2026-01-10&to=2026-01-01", ""), 400, "reversed range")
	expect(t, f.do(t, "outsider", "GET", A+"/usage/export.csv", ""), 404, "outsider export")
	// platform export: operators only, per-model rollups
	expect(t, f.do(t, "owner", "GET", "/api/admin/usage/export.csv", ""), 403, "non-operator platform export")
	expect(t, f.do(t, "op", "GET", "/api/admin/usage/export.csv?group=key", ""), 400, "platform key grouping")
	recs = readCSV(t, f.do(t, "op", "GET", "/api/admin/usage/export.csv?group=day", ""))
	if recs[len(recs)-1][1] != "2" {
		t.Fatalf("platform day export: %v", recs)
	}

	// logs export: range filter, other orgs excluded, formula-safe cells
	logs := []store.RequestLog{
		{ID: "l1", OrgID: f.orgA.ID, KeyID: kid, Model: "a/b", Status: 500, Error: "=1+1", At: now.Add(-time.Hour)},
		{ID: "l2", OrgID: f.orgA.ID, KeyID: kid, Model: "a/b", Status: 200, At: now.Add(-48 * time.Hour)},
		{ID: "l3", OrgID: f.orgB.ID, Model: "a/b", Status: 200, At: now.Add(-time.Hour)},
	}
	if err := f.s.PutLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	r = f.do(t, "viewer", "GET", A+"/logs/export.csv?from="+now.Add(-24*time.Hour).Format(time.RFC3339), "")
	expect(t, r, 200, "log export")
	recs = readCSV(t, r)
	if len(recs) != 2 || recs[1][0] != "l1" || recs[1][7] != "'=1+1" || r.hdr.Get("X-GW-Row-Cap") != "100000" {
		t.Fatalf("log export: %v", recs)
	}
	recs = readCSV(t, f.do(t, "viewer", "GET", A+"/logs/export.csv", ""))
	if len(recs) != 3 {
		t.Fatalf("default 7-day log export: %v", recs)
	}
	expect(t, f.do(t, "viewer", "GET", A+"/logs/export.csv?from=yesterday", ""), 400, "bad from")
}

func TestContentLoggingSettingAndRead(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	A := "/api/orgs/" + f.orgA.ID
	var o store.Org
	f.do(t, "admin", "GET", A, "").json(t, &o)
	if o.ContentLogging != nil {
		t.Fatal("content logging must be off by default")
	}
	expect(t, f.do(t, "member", "PATCH", A, `{"content_logging":{"enabled":true}}`), 403, "member enables content logging")
	expect(t, f.do(t, "admin", "PATCH", A, `{"content_logging":{"enabled":true,"retention_days":91}}`), 400, "retention too long")
	r := f.do(t, "admin", "PATCH", A, `{"content_logging":{"enabled":true}}`)
	expect(t, r, 200, "enable")
	r.json(t, &o)
	if o.ContentLogging == nil || !o.ContentLogging.Enabled || o.ContentLogging.RetentionDays != 30 {
		t.Fatalf("content logging: %+v", o.ContentLogging)
	}
	if err := f.s.PutContent(ctx, f.orgA.ID, store.LogContent{LogID: "log1", At: time.Now(), Request: `{"q":1}`, Response: `{"a":2}`}); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do(t, "member", "GET", A+"/logs/log1/content", ""), 403, "member reads content")
	expect(t, f.do(t, "outsider", "GET", A+"/logs/log1/content", ""), 404, "outsider reads content")
	expect(t, f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/logs/log1/content", ""), 404, "content via other org")
	r = f.do(t, "admin", "GET", A+"/logs/log1/content", "")
	expect(t, r, 200, "admin reads content")
	var c store.LogContent
	r.json(t, &c)
	if c.Request != `{"q":1}` || c.Response != `{"a":2}` {
		t.Fatalf("content: %+v", c)
	}
	var audit []store.AuditEntry
	f.do(t, "admin", "GET", A+"/audit", "").json(t, &audit)
	var read, update bool
	for _, e := range audit {
		read = read || (e.Action == "log.content.read" && e.Target == "log1")
		update = update || (e.Action == "org.update" && strings.Contains(e.Detail, "content_logging enabled=true"))
	}
	if !read || !update {
		t.Fatalf("audit: read=%v update=%v", read, update)
	}
}
