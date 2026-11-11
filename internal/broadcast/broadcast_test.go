package broadcast

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

var update = flag.Bool("update", false, "rewrite testdata/span.golden.json")

func newStore(t *testing.T) (*store.Store, *store.Org) {
	t.Helper()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kv.Close() })
	box, _ := store.NewBox("test-master-key-123456")
	s := store.New(kv, box)
	o := &store.Org{Name: "acme"}
	if err := s.CreateOrg(context.Background(), o, ""); err != nil {
		t.Fatal(err)
	}
	return s, o
}

func sampleTrace() gateway.Trace {
	return gateway.Trace{Kind: "chat", System: "openai", Request: `{"messages":[{"role":"user","content":"hi"}]}`, Response: "hello",
		Log: store.RequestLog{ID: "0123456789abcdef", OrgID: "org1", KeyID: "key1", Model: "openai/gpt-4o", Provider: "openai-main",
			Upstream: "gpt-4o-2024-08-06", Status: 200, InTokens: 12, OutTokens: 34, Cost: 1234, LatencyMs: 850,
			Stream: true, EndUser: "user-42", At: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}}
}

func TestSpanGolden(t *testing.T) {
	var got bytes.Buffer
	if err := json.Indent(&got, payload([]json.RawMessage{spanOf(sampleTrace(), true)}), "", "  "); err != nil {
		t.Fatal(err)
	}
	got.WriteByte('\n')
	const path = "testdata/span.golden.json"
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("span JSON differs from %s (run with -update after reviewing):\n%s", path, got.String())
	}
	// errors carry status ERROR and error.type; content is left out unless asked for
	tr := sampleTrace()
	tr.Log.Status, tr.Log.Error = 502, "upstream error"
	s := string(spanOf(tr, false))
	if !strings.Contains(s, `"status":{"code":2,"message":"upstream error"}`) || !strings.Contains(s, `"error.type"`) || strings.Contains(s, "gen_ai.prompt") {
		t.Fatalf("error span: %s", s)
	}
	tr.Request = strings.Repeat("é", contentMax)
	if s := string(spanOf(tr, true)); !strings.Contains(s, "…[truncated]") || len(s) > 3*contentMax {
		t.Fatalf("content not truncated: %d bytes", len(s))
	}
}

func TestSampling(t *testing.T) {
	n := 0
	for i := range 10000 {
		if sampled(store.NewID()+fmt.Sprint(i), 0.25) {
			n++
		}
	}
	if n < 2200 || n > 2800 {
		t.Fatalf("rate 0.25 kept %d of 10000", n)
	}
	if sampled("x", 0) || !sampled("x", 1) {
		t.Fatal("rate 0 keeps none, rate 1 keeps all")
	}
	if sampled("same-id", 0.5) != sampled("same-id", 0.5) {
		t.Fatal("sampling must be deterministic per request")
	}
}

func put(t *testing.T, s *store.Store, te store.TraceExport) *store.TraceExport {
	t.Helper()
	if te.Type == "" {
		te.Type = "otlp"
	}
	if te.Name == "" {
		te.Name = "x"
	}
	if err := s.CreateTraceExport(context.Background(), &te); err != nil {
		t.Fatal(err)
	}
	return &te
}

func TestExportFiltersAndContentGating(t *testing.T) {
	s, o := newStore(t)
	other := &store.Org{Name: "other"}
	_ = s.CreateOrg(context.Background(), other, "")
	all := put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: "http://a/v1/traces", SampleRate: 1, Enabled: true, IncludeContent: true})
	k1 := put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: "http://b/v1/traces", SampleRate: 1, Enabled: true, KeyIDs: []string{"k1"}})
	off := put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: "http://c/v1/traces", SampleRate: 1})
	none := put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: "http://d/v1/traces", SampleRate: 0, Enabled: true})
	plat := put(t, s, store.TraceExport{Endpoint: "http://e/v1/traces", SampleRate: 1, Enabled: true, IncludeContent: true})
	e := New(s, true, 1000)
	if err := e.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.Wants(other.ID) {
		t.Fatal("a platform export wants every org's requests")
	}
	tr := sampleTrace()
	tr.Log.OrgID, tr.Log.KeyID = o.ID, "k2"
	e.Export(tr) // content captured (org logs content)
	tr.Request, tr.Response = "", ""
	tr.Log.KeyID, tr.Log.ID = "k1", "fedcba9876543210"
	e.Export(tr) // org without content logging: nothing captured
	n := func(te *store.TraceExport) int { return len(e.sinks[te.ID].buf) }
	if n(all) != 2 || n(k1) != 1 || n(off) != 0 || n(none) != 0 || n(plat) != 2 {
		t.Fatalf("buffered all=%d k1=%d off=%d none=%d plat=%d", n(all), n(k1), n(off), n(none), n(plat))
	}
	withContent := func(sp json.RawMessage) bool { return bytes.Contains(sp, []byte("gen_ai.prompt")) }
	if b := e.sinks[all.ID].buf; !withContent(b[0]) || withContent(b[1]) {
		t.Fatal("content only when the export includes it and the org captured it")
	}
	if withContent(e.sinks[k1.ID].buf[0]) {
		t.Fatal("export without include_content got content")
	}
	if e.total != 5 || e.pending[o.ID] != 3 || e.pending[""] != 2 {
		t.Fatalf("accounting total=%d pending=%v", e.total, e.pending)
	}
	// deleting an export drops its buffer
	_ = s.DeleteTraceExport(context.Background(), o.ID, all.ID)
	_ = e.Reload(context.Background())
	if e.total != 3 || e.pending[o.ID] != 1 {
		t.Fatalf("after delete total=%d pending=%v", e.total, e.pending)
	}
}

func TestFairShare(t *testing.T) {
	s, o := newStore(t)
	other := &store.Org{Name: "other"}
	_ = s.CreateOrg(context.Background(), other, "")
	put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: "http://a", SampleRate: 1, Enabled: true})
	quiet := put(t, s, store.TraceExport{OrgID: other.ID, Endpoint: "http://b", SampleRate: 1, Enabled: true})
	e := New(s, true, 2000) // share: 1000 per org
	_ = e.Reload(context.Background())
	tr := sampleTrace()
	tr.Log.OrgID = o.ID
	for range 1500 {
		e.Export(tr)
	}
	tr.Log.OrgID = other.ID
	e.Export(tr)
	if e.pending[o.ID] != 1000 || len(e.sinks[quiet.ID].buf) != 1 {
		t.Fatalf("noisy org must stop at its share: %v", e.pending)
	}
}

type collector struct {
	mu     sync.Mutex
	sizes  []int
	hdr    http.Header
	path   string
	status atomic.Int32
	calls  atomic.Int32
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.calls.Add(1)
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []json.RawMessage `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	c.mu.Lock()
	c.hdr, c.path = r.Header.Clone(), r.URL.Path
	if st := c.status.Load(); st != 0 {
		c.mu.Unlock()
		w.WriteHeader(int(st))
		return
	}
	c.sizes = append(c.sizes, len(body.ResourceSpans[0].ScopeSpans[0].Spans))
	c.mu.Unlock()
}

func (c *collector) got() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.sizes...)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out waiting for " + what)
}

func TestBatchingAndHeaders(t *testing.T) {
	s, o := newStore(t)
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: srv.URL + "/v1/traces", SampleRate: 1, Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer tok"}})
	e := New(s, true, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx, 1)
	tr := sampleTrace()
	tr.Log.OrgID = o.ID
	start := time.Now()
	for i := range 250 {
		tr.Log.ID = fmt.Sprintf("%016x", i)
		e.Export(tr)
	}
	eventually(t, "two full batches", func() bool { return len(c.got()) >= 2 })
	if g := c.got(); g[0] != 100 || g[1] != 100 || time.Since(start) > BatchWait {
		t.Fatalf("full batches go out at once: %v after %v", g, time.Since(start))
	}
	eventually(t, "the partial batch", func() bool { return len(c.got()) == 3 })
	if g := c.got(); g[2] != 50 || time.Since(start) < BatchWait-100*time.Millisecond {
		t.Fatalf("partial batch waits for BatchWait: %v after %v", g, time.Since(start))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hdr.Get("Authorization") != "Bearer tok" || c.hdr.Get("Content-Type") != "application/json" || c.path != "/v1/traces" {
		t.Fatalf("request: %s %v", c.path, c.hdr)
	}
}

func TestRetryThenDisable(t *testing.T) {
	s, o := newStore(t)
	c := &collector{}
	c.status.Store(503)
	srv := httptest.NewServer(c)
	defer srv.Close()
	te := put(t, s, store.TraceExport{OrgID: o.ID, Endpoint: srv.URL, SampleRate: 1, Enabled: true})
	e := New(s, true, 5000)
	e.Backoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx, 1)
	tr := sampleTrace()
	tr.Log.OrgID = o.ID
	e.Export(tr)
	e.flush(time.Now().Add(BatchWait))
	eventually(t, "retries", func() bool { return c.calls.Load() == 1+retries })
	eventually(t, "failure count", func() bool {
		got, _ := s.GetTraceExport(ctx, o.ID, te.ID)
		return got.Failures == 1
	})
	// a success resets the count
	c.status.Store(0)
	e.Export(tr)
	e.flush(time.Now().Add(BatchWait))
	eventually(t, "reset", func() bool {
		got, _ := s.GetTraceExport(ctx, o.ID, te.ID)
		return got.Failures == 0
	})
	// 10 consecutive failed batches disable the export (400: not retried)
	c.status.Store(400)
	before := c.calls.Load()
	for i := range disableAfter {
		e.Export(tr)
		e.flush(time.Now().Add(BatchWait))
		eventually(t, "failed batch", func() bool { return c.calls.Load() == before+int32(i)+1 })
		eventually(t, "batch settled", func() bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			return !e.sinks[te.ID].inflight
		})
	}
	got, _ := s.GetTraceExport(ctx, o.ID, te.ID)
	if got.Enabled || got.Failures != disableAfter || e.Wants(o.ID) {
		t.Fatalf("export must be disabled: %+v wants=%v", got, e.Wants(o.ID))
	}
}

func TestSSRFAndLangfuse(t *testing.T) {
	s, o := newStore(t)
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	strict := New(s, false, 100)
	for _, u := range []string{srv.URL, "http://example.com/v1/traces", "https://169.254.169.254/v1/traces", "https://user:pw@example.com/"} {
		if CheckURL(store.TraceExport{Type: "otlp", Endpoint: u}, false) == nil {
			t.Fatalf("%s must be refused", u)
		}
	}
	if _, err := strict.Send(context.Background(), store.TraceExport{Type: "otlp", Endpoint: srv.URL}, o.ID); err == nil || c.calls.Load() != 0 {
		t.Fatalf("loopback collector reached without ALLOW_PRIVATE_PROVIDER_URLS: %v", err)
	}
	lf := store.TraceExport{Type: "langfuse", Endpoint: srv.URL + "/", Headers: map[string]string{"Authorization": LangfuseAuth("pk-lf-1", "sk-lf-2")}}
	st, err := New(s, true, 100).Send(context.Background(), lf, o.ID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil || st != 200 || c.path != "/api/public/otel/v1/traces" || c.hdr.Get("Authorization") != "Basic cGstbGYtMTpzay1sZi0y" {
		t.Fatalf("langfuse: %d %v %s %v", st, err, c.path, c.hdr)
	}
}
