package alert

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

func TestCheckURLTenantServices(t *testing.T) {
	for _, c := range []struct {
		url          string
		tenant, priv bool
		ok           bool
		what         string
	}{
		{"telegram://123456:ABCdefGHIjkl@telegram?chats=@alerts", true, false, true, "telegram"},
		{"discord://token@123456789", true, false, true, "discord"},
		{"ntfy://ntfy.sh/polyrail-alerts", true, false, true, "ntfy default host"},
		{"ntfy://ntfy.example.com/topic", true, false, false, "ntfy self-hosted"},
		{"gotify://gotify.example.com/AbCdEfGhIjKlMnO", true, false, false, "gotify (custom host)"},
		{"gotify://gotify.example.com/AbCdEfGhIjKlMnO", false, false, true, "gotify on the platform"},
		{"ntfy://127.0.0.1:9/x", true, true, false, "custom host must be public"},
		{"ntfy://127.0.0.1:9/x?disabletls=yes", false, false, true, "platform any host"},
		{"xmpp://user@example.com", false, false, false, "unsupported service"},
		{"telegram://not-a-token@telegram", true, false, false, "invalid telegram URL"},
		{"", false, false, false, "empty"},
	} {
		err := CheckURL(c.url, c.tenant, c.priv)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.what, err)
		}
		if err != nil && c.url != "" && strings.Contains(err.Error(), c.url) {
			t.Errorf("%s: error quotes the URL", c.what)
		}
	}
	if Service("generic+https://x.example/y") != "generic" || Service("telegram://a@telegram") != "telegram" {
		t.Fatal("service name")
	}
}

// The real shoutrrr ntfy service, against a local receiver over plain HTTP (platform channels may
// use private hosts).
func TestSendNtfyHTTP(t *testing.T) {
	sendReal(t, func(srvURL string) string {
		return "ntfy://" + strings.TrimPrefix(srvURL, "http://") + "/polyrail?disabletls=yes"
	})
}

// sendReal sends a key.quarantined message through the URL mk builds for a local receiver.
func sendReal(t *testing.T, mk func(srvURL string) string) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b) + " " + r.Header.Get("Title") + r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m1","event":"message"}`) // ntfy answers with the message
	}))
	defer srv.Close()
	raw := mk(srv.URL)
	if err := CheckURL(raw, false, false); err != nil {
		t.Fatal(err)
	}
	title, body := Message(KeyQuarantined, map[string]any{"provider": "OpenAI", "key_name": "sk-a****wxyz", "key_id": "k1", "reason": "3 consecutive authentication failures (status 401)"})
	if err := Send(context.Background(), raw, false, title, body); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		if !strings.Contains(b, "quarantined") || !strings.Contains(b, "sk-a****wxyz") || !strings.Contains(b, "OpenAI") {
			t.Fatalf("payload: %s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing received")
	}
	// a tenant send never reaches a private address
	if err := Send(context.Background(), raw, true, title, body); err == nil {
		t.Fatal("public-only send reached loopback")
	}
}

type fakeSend struct {
	mu   sync.Mutex
	sent []string // url + " " + title
	err  error
}

func (f *fakeSend) send(_ context.Context, u string, public bool, title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, u+" "+title)
	return f.err
}

func (f *fakeSend) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func TestDispatcherDedupeScopeAndAutoDisable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := store.NewBox("test-master-key-123456")
	s := store.New(kv, box)
	org := &store.Org{Name: "o"}
	if err := s.CreateOrg(ctx, org, ""); err != nil {
		t.Fatal(err)
	}
	chans := []*store.AlertChannel{
		{OrgID: org.ID, Name: "all", URL: "telegram://1:a@telegram?chats=1", Events: []string{"*"}, Enabled: true},
		{OrgID: org.ID, Name: "keys", URL: "telegram://2:b@telegram?chats=2", Events: []string{KeyQuarantined}, Enabled: true},
		{OrgID: org.ID, Name: "off", URL: "telegram://3:c@telegram?chats=3", Events: []string{"*"}, Enabled: false},
		{OrgID: "", Name: "platform", URL: "generic+http://ops.internal/x", Events: []string{"*"}, Enabled: true},
	}
	for _, c := range chans {
		if err := s.CreateAlertChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	fs := &fakeSend{}
	d := New(s, false, 100)
	d.Send = fs.send
	d.Start(ctx, 1)
	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for fs.count() < n {
			if time.Now().After(deadline) {
				t.Fatalf("sent %d, want %d: %v", fs.count(), n, fs.sent)
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		if fs.count() != n {
			t.Fatalf("sent %d, want %d: %v", fs.count(), n, fs.sent)
		}
	}

	key := map[string]any{"provider_id": "p1", "key_id": "k1", "provider": "P"}
	d.Emit(org.ID, KeyQuarantined, key)
	wait(2) // "all" and "keys"; never the disabled channel or the platform one
	d.Emit(org.ID, KeyQuarantined, key)
	d.Emit(org.ID, "key.created", key) // not an alert event
	wait(2)                            // deduped for 10 minutes
	d.Emit(org.ID, KeyQuarantined, map[string]any{"provider_id": "p1", "key_id": "k2"})
	wait(4) // another subject
	d.Emit(org.ID, RequestFailed, map[string]any{"model": "m"})
	wait(5) // only "all"
	d.Emit("", ProviderUnavailable, map[string]any{"provider_id": "p9"})
	wait(6) // platform channel only
	if last := fs.sent[5]; !strings.HasPrefix(last, "generic+http://ops.internal/x Polyrail: provider unavailable") {
		t.Fatalf("platform send: %q", last)
	}

	// 10 consecutive failures disable a channel; a success resets the count
	fs.mu.Lock()
	fs.err = errors.New("boom")
	fs.mu.Unlock()
	for i := range 10 {
		d.Emit(org.ID, RequestFailed, map[string]any{"model": "m" + string(rune('a'+i))})
	}
	wait(16)
	cs, _ := s.ListAlertChannels(ctx, org.ID)
	for _, c := range cs {
		if c.Name == "all" && (c.Enabled || c.Failures != 10) {
			t.Fatalf("auto-disable: %+v", c)
		}
		if c.Name == "keys" && c.Failures != 0 {
			t.Fatalf("unsubscribed channel counted failures: %+v", c)
		}
	}
}

func TestMessagesAreReadable(t *testing.T) {
	for _, typ := range append(Types, TestPing) {
		title, body := Message(typ, map[string]any{"provider": "P", "key_id": "k", "model": "m", "status": 502, "code": "insufficient_credits"})
		if !strings.HasPrefix(title, "Polyrail: ") || body == "" || strings.Contains(body, "<nil>") || strings.Contains(body, "%!") {
			t.Errorf("%s: %q / %q", typ, title, body)
		}
	}
}
