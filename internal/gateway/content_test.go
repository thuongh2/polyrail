package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// waitContent polls for the content of the org's newest request log.
func waitContent(t *testing.T, f *fixture) *store.LogContent {
	t.Helper()
	ctx := context.Background()
	for range 200 {
		f.g.Usage.Close() // flush logs (safe to call more than once)
		logs, _ := f.g.S.ListLogs(ctx, f.org.ID, 1, 1)
		if len(logs) == 1 {
			if c, err := f.g.S.GetContent(ctx, f.org.ID, logs[0].ID); err == nil {
				return c
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no content stored")
	return nil
}

func TestContentLoggingOptIn(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// off by default: nothing stored
	f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"quiet"}]}`).Body.Close()
	time.Sleep(50 * time.Millisecond)
	if items, _ := f.g.S.KV.Scan(ctx, "logc/", "", 0); len(items) != 0 {
		t.Fatal("content stored while content logging is off")
	}
	if _, err := f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error {
		o.ContentLogging = &store.ContentLogging{Enabled: true, RetentionDays: 7}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.g.InvalidateAuth()
	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"remember me"}]}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	c := waitContent(t, f)
	if !strings.Contains(c.Request, "remember me") || !strings.Contains(c.Response, "echo: remember me") {
		t.Fatalf("non-stream content: %+v", c)
	}
}

func TestContentLoggingStream(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error {
		o.ContentLogging = &store.ContentLogging{Enabled: true, RetentionDays: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","stream":true,"messages":[{"role":"user","content":"s"}]}`)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	c := waitContent(t, f)
	if c.Response != "hello" || !strings.Contains(c.Request, `"stream":true`) {
		t.Fatalf("stream content: %+v", c)
	}
	if _, err := f.g.S.GetContent(ctx, "other-org", c.LogID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("content visible to another org")
	}
}
