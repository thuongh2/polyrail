package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

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

// verify is what a receiver does: recompute the HMAC over "<t>.<body>".
func verify(secret, header string, body []byte) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + string(body)))
	want := hex.EncodeToString(m.Sum(nil))
	return ts != "" && hmac.Equal([]byte(sig), []byte(want))
}

func TestSignedDelivery(t *testing.T) {
	s, o := newStore(t)
	got := make(chan *http.Request, 1)
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		got <- r
	}))
	defer srv.Close()
	h := &store.Webhook{OrgID: o.ID, URL: srv.URL, Events: []string{KeyCreated}, Secret: "whsec_x", Enabled: true}
	if err := s.CreateWebhook(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	d := New(s, true, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx, 1)
	d.Emit(o.ID, KeyRevoked, nil) // not subscribed
	d.Emit(o.ID, KeyCreated, map[string]any{"key_id": "k1"})
	select {
	case r := <-got:
		if r.Header.Get("X-GW-Event") != KeyCreated || !strings.HasPrefix(r.Header.Get("X-GW-Delivery"), "evt_") {
			t.Fatalf("headers: %v", r.Header)
		}
		if !verify("whsec_x", r.Header.Get("X-GW-Signature"), body) || verify("other", r.Header.Get("X-GW-Signature"), body) {
			t.Fatalf("signature does not verify: %s", r.Header.Get("X-GW-Signature"))
		}
		var ev Event
		if err := json.Unmarshal(body, &ev); err != nil || ev.Type != KeyCreated || ev.OrgID != o.ID || ev.Data["key_id"] != "k1" || ev.Created == 0 {
			t.Fatalf("body: %s %v", body, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	select {
	case r := <-got:
		t.Fatalf("unsubscribed event delivered: %s", r.Header.Get("X-GW-Event"))
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRetryThenAutoDisable(t *testing.T) {
	s, o := newStore(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	h := &store.Webhook{OrgID: o.ID, URL: srv.URL, Events: []string{"*"}, Secret: "s", Enabled: true}
	if err := s.CreateWebhook(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	d := New(s, true, 8)
	d.Backoff = time.Millisecond
	ctx := context.Background()
	ev := NewEvent(o.ID, TestPing, nil)
	d.dispatch(ctx, ev)
	if n := hits.Load(); n != 1+retries {
		t.Fatalf("attempts = %d, want %d", n, 1+retries)
	}
	for i := 1; i < disableAfter; i++ {
		d.dispatch(ctx, ev)
	}
	got, _ := s.GetWebhook(ctx, o.ID, h.ID)
	if got.Enabled || got.Failures != disableAfter {
		t.Fatalf("want disabled after %d failures: %+v", disableAfter, got)
	}
	before := hits.Load()
	d.dispatch(ctx, ev)
	if hits.Load() != before {
		t.Fatal("disabled webhook still receives events")
	}
}

func TestSuccessResetsFailuresAnd4xxNotRetried(t *testing.T) {
	s, o := newStore(t)
	var hits atomic.Int64
	status := atomic.Int64{}
	status.Store(400)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	h := &store.Webhook{OrgID: o.ID, URL: srv.URL, Events: []string{"*"}, Secret: "s", Enabled: true}
	_ = s.CreateWebhook(context.Background(), h)
	d := New(s, true, 8)
	ctx := context.Background()
	d.dispatch(ctx, NewEvent(o.ID, TestPing, nil))
	if hits.Load() != 1 {
		t.Fatalf("4xx retried: %d attempts", hits.Load())
	}
	status.Store(204)
	d.dispatch(ctx, NewEvent(o.ID, TestPing, nil))
	if got, _ := s.GetWebhook(ctx, o.ID, h.ID); got.Failures != 0 {
		t.Fatalf("success must reset failures: %+v", got)
	}
}

func TestSSRFGuard(t *testing.T) {
	s, o := newStore(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	for _, u := range []string{srv.URL, "https://127.0.0.1/x", "https://user:pw@example.com/", "ftp://example.com", "https://10.0.0.1/"} {
		if CheckURL(u, false) == nil {
			t.Errorf("CheckURL(%q) accepted", u)
		}
	}
	if CheckURL(srv.URL, true) != nil {
		t.Error("allow-private must accept http on loopback")
	}
	// A webhook saved under allow-private is still blocked at send time once the setting is off,
	// and the dialer itself refuses loopback even if validation were bypassed.
	d := New(s, false, 8)
	h := store.Webhook{OrgID: o.ID, URL: srv.URL, Secret: "s"}
	if _, err := d.Send(context.Background(), h, NewEvent(o.ID, TestPing, nil)); err == nil {
		t.Fatal("send to loopback allowed")
	}
	req, _ := http.NewRequest("POST", srv.URL, nil)
	if _, err := d.client.Do(req); err == nil {
		t.Fatal("dialer connected to loopback")
	}
	if hits.Load() != 0 {
		t.Fatal("loopback server was reached")
	}
}

func TestDedupeRequestFailed(t *testing.T) {
	s, o := newStore(t)
	d := New(s, true, 8)
	d.Emit(o.ID, RequestFailed, map[string]any{"model": "a"})
	d.Emit(o.ID, RequestFailed, map[string]any{"model": "a"})
	d.Emit(o.ID, RequestFailed, map[string]any{"model": "b"})
	d.Emit(o.ID, KeyCreated, nil)
	d.Emit(o.ID, KeyCreated, nil)
	if n := len(d.q); n != 4 {
		t.Fatalf("queued %d events, want 4 (one per model + both key events)", n)
	}
	small := New(s, true, 1)
	small.Emit(o.ID, KeyCreated, nil)
	small.Emit(o.ID, KeyCreated, nil) // full: dropped, not blocking
	if len(small.q) != 1 {
		t.Fatal("bounded queue")
	}
}

// One org flooding events must not fill the shared queue and drop other orgs' events.
func TestEmitPerOrgQueueShare(t *testing.T) {
	d := New(nil, false, 1000)
	for range 1000 {
		d.Emit("orgA", KeyCreated, nil)
	}
	d.Emit("orgB", KeyCreated, nil)
	var b int
	for len(d.q) > 0 {
		if ev := <-d.q; ev.OrgID == "orgB" {
			b++
		}
	}
	if b != 1 {
		t.Fatalf("orgB event dropped while orgA flooded the queue")
	}
}
