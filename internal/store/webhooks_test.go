package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWebhookSecretSealedAndOrgDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "acme"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	w := &Webhook{OrgID: o.ID, URL: "https://example.com/h", Events: []string{"*"}, Secret: "whsec_topsecret", Enabled: true}
	if err := s.CreateWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.KV.Get(ctx, hookKey(o.ID, w.ID))
	if strings.Contains(string(raw), "whsec_topsecret") {
		t.Fatalf("secret stored in plaintext: %s", raw)
	}
	if out, _ := json.Marshal(w); strings.Contains(string(out), "whsec_") {
		t.Fatalf("secret in API JSON: %s", out)
	}
	got, err := s.UpdateWebhook(ctx, o.ID, w.ID, func(x *Webhook) error { x.Failures = 3; return nil })
	if err != nil || got.Failures != 3 {
		t.Fatalf("update: %+v %v", got, err)
	}
	hs, err := s.ListWebhooks(ctx, o.ID)
	if err != nil || len(hs) != 1 || hs[0].Secret != "whsec_topsecret" || hs[0].Failures != 3 {
		t.Fatalf("list must decrypt and keep the secret across updates: %+v %v", hs, err)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWebhook(ctx, o.ID, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("webhook survives org delete: %v", err)
	}
	if err := s.CreateWebhook(ctx, &Webhook{OrgID: "nope", URL: "https://x", Secret: "s"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("webhook for missing org: %v", err)
	}
}
