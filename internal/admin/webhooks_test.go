package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/webhook"
)

type delivery struct {
	event, sig string
	body       []byte
}

func receiver(t *testing.T) (*httptest.Server, chan delivery) {
	ch := make(chan delivery, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- delivery{r.Header.Get("X-GW-Event"), r.Header.Get("X-GW-Signature"), b}
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func signed(secret string, d delivery) bool {
	ts, sig, _ := strings.Cut(strings.TrimPrefix(d.sig, "t="), ",v1=")
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + string(d.body)))
	return hmac.Equal([]byte(sig), []byte(hex.EncodeToString(m.Sum(nil))))
}

func wait(t *testing.T, ch chan delivery) delivery {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	return delivery{}
}

func TestWebhookCRUDTestPingAndKeyEvents(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true}) // the receiver is on loopback
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.a.Hooks = webhook.New(f.s, true, 16)
	f.a.Hooks.Start(ctx, 1)
	srv, ch := receiver(t)
	base := "/api/orgs/" + f.orgA.ID + "/webhooks"

	expect(t, f.do(t, "member", "POST", base, `{"url":"`+srv.URL+`","events":["*"]}`), 403, "member create")
	expect(t, f.do(t, "admin", "POST", base, `{"url":"ftp://x","events":["*"]}`), 400, "bad scheme")
	expect(t, f.do(t, "admin", "POST", base, `{"url":"`+srv.URL+`","events":["nope"]}`), 400, "unknown event")
	expect(t, f.do(t, "admin", "POST", base, `{"url":"`+srv.URL+`"}`), 400, "missing events")

	r := f.do(t, "admin", "POST", base, `{"url":"`+srv.URL+`/hook?token=abc","events":["test.ping","key.created","key.revoked"]}`)
	expect(t, r, 201, "create")
	var created struct {
		Webhook store.Webhook `json:"webhook"`
		Secret  string        `json:"secret"`
	}
	r.json(t, &created)
	if !strings.HasPrefix(created.Secret, "whsec_") || !created.Webhook.Enabled {
		t.Fatalf("create: %s", r.body)
	}
	id := created.Webhook.ID
	r = f.do(t, "admin", "GET", base, "")
	expect(t, r, 200, "list")
	if strings.Contains(r.body, created.Secret) || strings.Contains(r.body, "sealed") {
		t.Fatalf("list leaks the secret: %s", r.body)
	}
	expect(t, f.do(t, "outsider", "GET", base, ""), 404, "other org")

	r = f.do(t, "admin", "POST", base+"/"+id+"/test", "")
	expect(t, r, 200, "test ping")
	if !strings.Contains(r.body, `"ok":true`) {
		t.Fatalf("test ping: %s", r.body)
	}
	if d := wait(t, ch); d.event != "test.ping" || !signed(created.Secret, d) {
		t.Fatalf("test ping delivery: %+v", d)
	}

	keyID, _ := f.createKey(t, "admin", f.orgA.ID)
	if d := wait(t, ch); d.event != "key.created" || !strings.Contains(string(d.body), keyID) || !signed(created.Secret, d) {
		t.Fatalf("key.created: %+v", d)
	}
	expect(t, f.do(t, "admin", "PATCH", "/api/orgs/"+f.orgA.ID+"/keys/"+keyID, `{"revoked":true}`), 200, "revoke")
	if d := wait(t, ch); d.event != "key.revoked" {
		t.Fatalf("key.revoked: %+v", d)
	}

	if _, err := f.s.UpdateWebhook(context.Background(), f.orgA.ID, id, func(h *store.Webhook) error { h.Enabled, h.Failures = false, 10; return nil }); err != nil {
		t.Fatal(err)
	}
	r = f.do(t, "admin", "PATCH", base+"/"+id, `{"enabled":true,"events":["*"]}`)
	expect(t, r, 200, "re-enable")
	if !strings.Contains(r.body, `"failures":0`) || !strings.Contains(r.body, `"enabled":true`) {
		t.Fatalf("re-enable must reset failures: %s", r.body)
	}
	expect(t, f.do(t, "admin", "PATCH", base+"/"+id, `{"url":"gopher://x"}`), 400, "patch bad url")
	expect(t, f.do(t, "admin", "DELETE", base+"/"+id, ""), 204, "delete")
	expect(t, f.do(t, "admin", "DELETE", base+"/"+id, ""), 404, "delete again")

	r = f.do(t, "admin", "GET", "/api/orgs/"+f.orgA.ID+"/audit", "")
	for _, a := range []string{"webhook.create", "webhook.update", "webhook.delete"} {
		if !strings.Contains(r.body, a) {
			t.Fatalf("audit missing %s: %s", a, r.body)
		}
	}
	if strings.Contains(r.body, "token=abc") {
		t.Fatalf("audit records the webhook URL query: %s", r.body)
	}
}

func TestWebhookURLNeedsPublicHTTPS(t *testing.T) {
	f := setup(t, Config{})
	srv, _ := receiver(t)
	base := "/api/orgs/" + f.orgA.ID + "/webhooks"
	for _, u := range []string{srv.URL, "https://127.0.0.1/x", "http://example.com/x", "https://169.254.169.254/latest"} {
		expect(t, f.do(t, "admin", "POST", base, `{"url":"`+u+`","events":["*"]}`), 400, "url "+u)
	}
}

func TestSemanticCacheSettings(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	p := &store.Provider{Name: "p", Type: "openai", Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.Model{
		{ID: "x/emb", Kind: "embedding", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "e"}}},
		{ID: "x/chat", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "c"}}},
	} {
		if err := f.s.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	path := "/api/orgs/" + f.orgA.ID
	expect(t, f.do(t, "member", "PATCH", path, `{"semantic_cache":{"model":"x/emb"}}`), 403, "member")
	for _, bad := range []string{`{"model":"x/chat"}`, `{"model":"nope"}`, `{"model":"x/emb","threshold":0.3}`,
		`{"model":"x/emb","threshold":1}`, `{"model":"x/emb","ttl_seconds":-1}`, `{"model":"x/emb","ttl_seconds":100000}`} {
		expect(t, f.do(t, "admin", "PATCH", path, `{"semantic_cache":`+bad+`}`), 400, bad)
	}
	r := f.do(t, "admin", "PATCH", path, `{"semantic_cache":{"model":"x/emb"}}`)
	expect(t, r, 200, "enable")
	if !strings.Contains(r.body, `"semantic_cache":{"model":"x/emb","threshold":0.92,"ttl_seconds":3600}`) {
		t.Fatalf("defaults: %s", r.body)
	}
	r = f.do(t, "admin", "PATCH", path, `{"semantic_cache":{"model":""}}`)
	expect(t, r, 200, "disable")
	if strings.Contains(r.body, "semantic_cache") {
		t.Fatalf("disable: %s", r.body)
	}
}
