package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/polyrail/polyrail/internal/broadcast"
	"github.com/polyrail/polyrail/internal/store"
)

func TestTraceExportAPI(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true}) // the collector is on loopback
	var hits atomic.Int32
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		w.WriteHeader(200)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.a.Traces = broadcast.New(f.s, true, 100)
	f.a.Traces.Start(ctx, 1)
	base := "/api/orgs/" + f.orgA.ID + "/trace-exports"

	expect(t, f.do(t, "member", "POST", base, `{"name":"t","type":"otlp","endpoint":"`+srv.URL+`"}`), 403, "member create")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"t","type":"s3","endpoint":"`+srv.URL+`"}`), 400, "unknown type")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"t","type":"otlp","endpoint":"ftp://x"}`), 400, "bad scheme")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"t","type":"otlp","endpoint":"`+srv.URL+`","sample_rate":2}`), 400, "sample rate")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"t","type":"otlp","endpoint":"`+srv.URL+`","key_ids":["not-ours"]}`), 400, "foreign key")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"t","type":"langfuse","endpoint":"`+srv.URL+`"}`), 400, "langfuse without keys")

	r := f.do(t, "admin", "POST", base, `{"name":"tempo","type":"otlp","endpoint":"`+srv.URL+`/v1/traces?token=abc","headers":{"Authorization":"Bearer s3cret"},"sample_rate":0.5}`)
	expect(t, r, 201, "create")
	var te store.TraceExport
	r.json(t, &te)
	if strings.Contains(r.body, "s3cret") || !strings.Contains(r.body, `"header_names":["Authorization"]`) || !te.Enabled || te.SampleRate != 0.5 {
		t.Fatalf("create: %s", r.body)
	}
	if !f.a.Traces.Wants(f.orgA.ID) || f.a.Traces.Wants(f.orgB.ID) {
		t.Fatal("create must reload the exporter")
	}
	r = f.do(t, "admin", "GET", base, "")
	if r.code != 200 || strings.Contains(r.body, "s3cret") || strings.Contains(r.body, "sealed") {
		t.Fatalf("list leaks headers: %s", r.body)
	}
	expect(t, f.do(t, "outsider", "GET", base, ""), 404, "other org")

	r = f.do(t, "admin", "POST", base+"/"+te.ID+"/test", "")
	if r.code != 200 || !strings.Contains(r.body, `"ok":true`) || hits.Load() != 1 || auth.Load() != "Bearer s3cret" {
		t.Fatalf("test: %s hits=%d", r.body, hits.Load())
	}

	_, _ = f.s.UpdateTraceExport(context.Background(), f.orgA.ID, te.ID, func(x *store.TraceExport) error { x.Enabled, x.Failures = false, 10; return nil })
	r = f.do(t, "admin", "PATCH", base+"/"+te.ID, `{"enabled":true,"include_content":true}`)
	if r.code != 200 || !strings.Contains(r.body, `"failures":0`) || !strings.Contains(r.body, `"include_content":true`) {
		t.Fatalf("re-enable: %s", r.body)
	}
	if got, _ := f.s.GetTraceExport(context.Background(), f.orgA.ID, te.ID); got.Headers["Authorization"] != "Bearer s3cret" {
		t.Fatal("patch without headers must keep them")
	}
	expect(t, f.do(t, "admin", "PATCH", base+"/"+te.ID, `{"endpoint":"gopher://x"}`), 400, "patch bad endpoint")

	// langfuse: key pair becomes basic auth on the OTLP endpoint
	r = f.do(t, "admin", "POST", base, `{"name":"lf","type":"langfuse","endpoint":"`+srv.URL+`","langfuse":{"public_key":"pk","secret_key":"sk"}}`)
	expect(t, r, 201, "langfuse")
	var lf store.TraceExport
	r.json(t, &lf)
	expect(t, f.do(t, "admin", "POST", base+"/"+lf.ID+"/test", ""), 200, "langfuse test")
	if auth.Load() != broadcast.LangfuseAuth("pk", "sk") || strings.Contains(r.body, "Basic") {
		t.Fatalf("langfuse auth: %v", auth.Load())
	}

	// platform scope: operator only
	expect(t, f.do(t, "owner", "GET", "/api/admin/trace-exports", ""), 403, "owner on platform")
	r = f.do(t, "op", "POST", "/api/admin/trace-exports", `{"name":"ops","type":"otlp","endpoint":"`+srv.URL+`","key_ids":["any"]}`)
	expect(t, r, 201, "platform create")
	if !f.a.Traces.Wants(f.orgB.ID) {
		t.Fatal("platform export wants every org")
	}

	expect(t, f.do(t, "admin", "DELETE", base+"/"+te.ID, ""), 204, "delete")
	expect(t, f.do(t, "admin", "DELETE", base+"/"+te.ID, ""), 404, "delete again")
	r = f.do(t, "admin", "GET", "/api/orgs/"+f.orgA.ID+"/audit", "")
	for _, a := range []string{"trace_export.create", "trace_export.update", "trace_export.delete", "trace_export.test"} {
		if !strings.Contains(r.body, a) {
			t.Fatalf("audit missing %s: %s", a, r.body)
		}
	}
	if strings.Contains(r.body, "token=abc") || strings.Contains(r.body, "s3cret") {
		t.Fatalf("audit records secrets: %s", r.body)
	}
}

func TestTraceExportNeedsPublicHTTPS(t *testing.T) {
	f := setup(t, Config{})
	base := "/api/orgs/" + f.orgA.ID + "/trace-exports"
	for _, u := range []string{"http://127.0.0.1:4318/v1/traces", "https://127.0.0.1/v1/traces", "http://example.com/v1/traces", "https://169.254.169.254/latest"} {
		expect(t, f.do(t, "admin", "POST", base, `{"name":"t","type":"otlp","endpoint":"`+u+`"}`), 400, "endpoint "+u)
	}
}
