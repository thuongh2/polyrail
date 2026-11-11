package gateway

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

type fakeTracer struct {
	mu   sync.Mutex
	org  string
	got  []Trace
	asks int
}

func (f *fakeTracer) Wants(org string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks++
	return org == f.org
}

func (f *fakeTracer) Export(t Trace) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, t)
}

func TestTracerReceivesRequests(t *testing.T) {
	f := setup(t)
	tr := &fakeTracer{org: "someone-else"}
	f.g.Traces = tr
	chat := `{"model":"test/ok","messages":[{"role":"user","content":"trace me"}],"user":"u-1"}`
	resp := f.post(t, "/v1/chat/completions", f.key, chat)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if tr.asks != 1 || len(tr.got) != 0 {
		t.Fatalf("no export wanted: asks=%d got=%d", tr.asks, len(tr.got))
	}
	tr.org = f.org.ID
	resp = f.post(t, "/v1/chat/completions", f.key, chat)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if len(tr.got) != 1 {
		t.Fatalf("got %d traces", len(tr.got))
	}
	g := tr.got[0]
	if g.System != "openai" || g.Kind != "chat" || g.Log.Model != "test/ok" || g.Log.KeyID != f.keyRec.ID || g.Log.EndUser != "u-1" ||
		g.Log.Status != 200 || g.Request != "" || g.Response != "" {
		t.Fatalf("trace: %+v", g)
	}
	// with content logging on, the captured bodies come along
	if _, err := f.g.S.UpdateOrg(context.Background(), f.org.ID, func(o *store.Org) error {
		o.ContentLogging = &store.ContentLogging{Enabled: true, RetentionDays: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	resp = f.post(t, "/v1/chat/completions", f.key, chat)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if g := tr.got[1]; !strings.Contains(g.Request, "trace me") || !strings.Contains(g.Response, "echo: trace me") {
		t.Fatalf("content: %+v", g)
	}
}
