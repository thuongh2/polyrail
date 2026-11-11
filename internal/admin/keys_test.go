package admin

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// keyUpstream answers like OpenAI: 401 for keys starting with "bad", 200 otherwise.
func keyUpstream(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer bad") {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided: bad-key-*** see internal-host.example","type":"invalid_request_error"}}`)
			return
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"m1","object":"model","created":1,"owned_by":"x"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type keyRow struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Masked        string   `json:"masked"`
	Weight        float64  `json:"weight"`
	Models        []string `json:"models"`
	Enabled       bool     `json:"enabled"`
	Status        string   `json:"status"`
	StatusReason  string   `json:"status_reason"`
	CooldownUntil *string  `json:"cooldown_until"`
	LastUsedAt    *string  `json:"last_used_at"`
	Stats24h      struct {
		Requests, Errors, AvgLatencyMs int64
	} `json:"stats_24h"`
}

func (f *fixture) keys(t *testing.T, as, base string) []keyRow {
	t.Helper()
	r := f.do(t, as, "GET", base+"/keys", "")
	expect(t, r, 200, "list keys")
	for _, secret := range []string{"good-key-00000001", "bad-key-000000001", "extra-key-0000001", "sealed"} {
		if strings.Contains(r.body, secret) {
			t.Fatalf("key list leaks %q: %s", secret, r.body)
		}
	}
	var rows []keyRow
	r.json(t, &rows)
	return rows
}

func byMasked(rows []keyRow, m string) keyRow {
	for _, r := range rows {
		if r.Masked == m {
			return r
		}
	}
	return keyRow{}
}

func TestProviderKeysAPI(t *testing.T) {
	f := setup(t, Config{})
	up := keyUpstream(t)
	r := f.do(t, "op", "POST", "/api/admin/providers", `{"name":"P","type":"openai","base_url":"`+up.URL+`","enabled":true,"keys":[{"value":"good-key-00000001","weight":1}]}`)
	expect(t, r, 200, "create provider")
	var p struct{ ID string }
	r.json(t, &p)
	base := "/api/admin/providers/" + p.ID

	// bulk add: newline/comma string, dedupe against stored and within the call, invalid values
	r = f.do(t, "op", "POST", base+"/keys", `{"keys":"bad-key-000000001\ngood-key-00000001, extra-key-0000001\nbad-key-000000001\nhas space-in-it"}`)
	expect(t, r, 200, "bulk add")
	var add struct{ Added, Duplicates, Invalid int }
	r.json(t, &add)
	if add.Added != 2 || add.Duplicates != 2 || add.Invalid != 1 {
		t.Fatalf("bulk add: %+v", add)
	}
	r = f.do(t, "op", "POST", base+"/keys", `{"keys":["extra-key-0000001","new-key-00000001"],"weight":3,"models":["m1"]}`)
	r.json(t, &add)
	if add.Added != 1 || add.Duplicates != 1 {
		t.Fatalf("array add: %+v", add)
	}
	many := make([]string, 1001)
	for i := range many {
		many[i] = fmt.Sprintf("k-%06d-xxxxxxxx", i)
	}
	expect(t, f.do(t, "op", "POST", base+"/keys", `{"keys":"`+strings.Join(many, `\n`)+`"}`), 400, "over 1000 keys")
	expect(t, f.do(t, "op", "POST", base+"/keys", `{"keys":[]}`), 400, "no keys")

	rows := f.keys(t, "op", base)
	if len(rows) != 4 {
		t.Fatalf("rows: %+v", rows)
	}
	good, bad, added := byMasked(rows, "good****0001"), byMasked(rows, "bad-****0001"), byMasked(rows, "new-****0001")
	if good.ID == "" || bad.ID == "" || !good.Enabled || good.Status != "available" || added.Weight != 3 || len(added.Models) != 1 {
		t.Fatalf("rows: %+v", rows)
	}

	// test: pinned to the key; a bad key reports the upstream status (operators see the message)
	r = f.do(t, "op", "POST", base+"/keys/"+good.ID+"/test", "")
	expect(t, r, 200, "test good")
	if !strings.Contains(r.body, `"ok":true`) {
		t.Fatalf("good test: %s", r.body)
	}
	r = f.do(t, "op", "POST", base+"/keys/"+bad.ID+"/test", `{"model":"m1"}`)
	expect(t, r, 200, "test bad")
	var tr struct {
		OK     bool
		Status int
		Error  string
	}
	r.json(t, &tr)
	if tr.OK || tr.Status != 401 || tr.Error == "" {
		t.Fatalf("bad test: %s", r.body)
	}
	if st := byMasked(f.keys(t, "op", base), "bad-****0001"); st.Status != "cooldown" || st.CooldownUntil == nil {
		t.Fatalf("bad key after failed test: %+v", st)
	}

	// patch: disable, rename, weight; enabling again lifts status
	r = f.do(t, "op", "PATCH", base+"/keys/"+bad.ID, `{"enabled":false,"name":"backup","weight":2}`)
	expect(t, r, 200, "patch")
	var row keyRow
	r.json(t, &row)
	if row.Enabled || row.Status != "disabled" || row.Name != "backup" || row.Weight != 2 {
		t.Fatalf("patched: %+v", row)
	}
	expect(t, f.do(t, "op", "PATCH", base+"/keys/"+bad.ID, `{"weight":-1}`), 400, "negative weight")
	expect(t, f.do(t, "op", "PATCH", base+"/keys/nope", `{"enabled":true}`), 404, "unknown key")
	r = f.do(t, "op", "PATCH", base+"/keys/"+bad.ID, `{"enabled":true}`)
	r.json(t, &row)
	if !row.Enabled || row.Status != "available" {
		t.Fatalf("re-enabled: %+v", row)
	}

	// the provider dialog (PUT with redacted keys) keeps key status
	expect(t, f.do(t, "op", "PATCH", base+"/keys/"+added.ID, `{"enabled":false}`), 200, "disable")
	r = f.do(t, "op", "GET", "/api/admin/providers", "")
	var ps []struct {
		ID   string
		Keys []map[string]any
	}
	r.json(t, &ps)
	if !strings.Contains(r.body, `"disabled":true`) {
		t.Fatalf("provider list lacks key status: %s", r.body)
	}
	putKeys := `[{"id":"` + good.ID + `"},{"id":"` + bad.ID + `"},{"id":"` + added.ID + `"},{"id":"` + byMasked(rows, "extr****0001").ID + `"}]`
	expect(t, f.do(t, "op", "PUT", base, `{"name":"P","type":"openai","base_url":"`+up.URL+`","enabled":true,"keys":`+putKeys+`}`), 200, "provider put")
	if k := byMasked(f.keys(t, "op", base), "new-****0001"); k.Enabled {
		t.Fatalf("PUT re-enabled a disabled key: %+v", k)
	}

	// stats: the two tests above were recorded on their keys
	deadline := time.Now().Add(3 * time.Second)
	for {
		r = f.do(t, "op", "GET", base+"/keys/"+bad.ID+"/stats?hours=3", "")
		expect(t, r, 200, "stats")
		var hours []struct {
			Hour               string
			Requests, Errors   int64
			InTokens, OutToken int64
		}
		r.json(t, &hours)
		if len(hours) != 3 {
			t.Fatalf("hours: %s", r.body)
		}
		if last := hours[2]; last.Requests == 1 && last.Errors == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats: %s", r.body)
		}
		time.Sleep(30 * time.Millisecond)
	}
	expect(t, f.do(t, "op", "GET", base+"/keys/"+bad.ID+"/stats?hours=0", ""), 400, "hours range")
	if k := byMasked(f.keys(t, "op", base), "good****0001"); k.Stats24h.Requests != 1 || k.LastUsedAt == nil {
		t.Fatalf("good key 24h stats: %+v", k)
	}

	// bulk actions with per-id results
	r = f.do(t, "op", "POST", base+"/keys/bulk", `{"action":"disable","ids":["`+good.ID+`","nope"]}`)
	expect(t, r, 200, "bulk disable")
	if !strings.Contains(r.body, `{"id":"nope","ok":false,"error":"not found"}`) {
		t.Fatalf("bulk results: %s", r.body)
	}
	if k := byMasked(f.keys(t, "op", base), "good****0001"); k.Enabled {
		t.Fatal("bulk disable")
	}
	r = f.do(t, "op", "POST", base+"/keys/bulk", `{"action":"test","ids":["`+good.ID+`","`+bad.ID+`"]}`)
	expect(t, r, 200, "bulk test")
	if !strings.Contains(r.body, `"status":401`) || !strings.Contains(r.body, `"status":200`) {
		t.Fatalf("bulk test: %s", r.body)
	}
	expect(t, f.do(t, "op", "POST", base+"/keys/bulk", `{"action":"clear","ids":["`+bad.ID+`"]}`), 200, "bulk clear")
	if k := byMasked(f.keys(t, "op", base), "bad-****0001"); k.Status != "available" {
		t.Fatalf("clear: %+v", k)
	}
	expect(t, f.do(t, "op", "POST", base+"/keys/bulk", `{"action":"explode","ids":["`+bad.ID+`"]}`), 400, "bad action")
	expect(t, f.do(t, "op", "POST", base+"/keys/bulk", `{"action":"delete","ids":["`+bad.ID+`","`+added.ID+`"]}`), 200, "bulk delete")
	expect(t, f.do(t, "op", "DELETE", base+"/keys/"+good.ID, ""), 204, "delete")
	if rows := f.keys(t, "op", base); len(rows) != 1 {
		t.Fatalf("after deletes: %+v", rows)
	}
	audit := f.do(t, "op", "GET", "/api/admin/audit", "")
	for _, a := range []string{"provider.keys.add", "provider.key.update", "provider.keys.delete", "provider.key.delete"} {
		if !strings.Contains(audit.body, a) {
			t.Fatalf("audit lacks %s", a)
		}
	}
	if strings.Contains(audit.body, "good-key-00000001") {
		t.Fatal("audit leaks a key")
	}
}

func TestProviderKeysTenantScope(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true}) // the upstream is on loopback
	up := keyUpstream(t)
	orgA := "/api/orgs/" + f.orgA.ID
	r := f.do(t, "admin", "POST", orgA+"/providers", `{"name":"mine","type":"openai","base_url":"`+up.URL+`","enabled":true,"keys":[{"value":"bad-key-000000001"}]}`)
	expect(t, r, 200, "org provider")
	var p struct{ ID string }
	r.json(t, &p)
	base := orgA + "/providers/" + p.ID
	rows := f.keys(t, "admin", base)
	if len(rows) != 1 || rows[0].Masked != "bad-****0001" {
		t.Fatalf("rows: %+v", rows)
	}
	kid := rows[0].ID

	// tenants see a status, never the upstream's text
	r = f.do(t, "admin", "POST", base+"/keys/"+kid+"/test", "")
	expect(t, r, 200, "tenant test")
	if !strings.Contains(r.body, `"ok":false`) || strings.Contains(r.body, "internal-host") || !strings.Contains(r.body, "status 401") {
		t.Fatalf("tenant test not sanitized: %s", r.body)
	}

	// isolation: other orgs, lower roles and the platform scope cannot reach these keys
	orgB := "/api/orgs/" + f.orgB.ID + "/providers/" + p.ID
	for _, c := range []struct{ as, method, path, body string }{
		{"outsider", "GET", orgB + "/keys", ""},
		{"outsider", "POST", orgB + "/keys", `{"keys":"x-key-0000000001"}`},
		{"outsider", "POST", orgB + "/keys/" + kid + "/test", ""},
		{"outsider", "GET", orgB + "/keys/" + kid + "/stats", ""},
		{"outsider", "PATCH", orgB + "/keys/" + kid, `{"enabled":false}`},
		{"outsider", "DELETE", orgB + "/keys/" + kid, ""},
		{"outsider", "POST", orgB + "/keys/bulk", `{"action":"delete","ids":["` + kid + `"]}`},
		{"outsider", "GET", base + "/keys", ""},
		{"op", "GET", "/api/admin/providers/" + p.ID + "/keys", ""},
		{"op", "DELETE", "/api/admin/providers/" + p.ID + "/keys/" + kid, ""},
	} {
		expect(t, f.do(t, c.as, c.method, c.path, c.body), 404, c.as+" "+c.method+" "+c.path)
	}
	expect(t, f.do(t, "member", "GET", base+"/keys", ""), 403, "member")
	if rows := f.keys(t, "admin", base); len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("keys changed by outsiders: %+v", rows)
	}
}
