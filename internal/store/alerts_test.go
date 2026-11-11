package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAlertURLSealedAndOrgDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "acme"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	c := &AlertChannel{OrgID: o.ID, Name: "tg", URL: "telegram://1:TOPSECRET@telegram?chats=1", Service: "telegram", Events: []string{"*"}, Enabled: true}
	if err := s.CreateAlertChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.KV.Get(ctx, alertKey(o.ID, c.ID))
	if strings.Contains(string(raw), "TOPSECRET") {
		t.Fatalf("url stored in plaintext: %s", raw)
	}
	if out, _ := json.Marshal(c); strings.Contains(string(out), "TOPSECRET") {
		t.Fatalf("url in API JSON: %s", out)
	}
	if _, err := s.UpdateAlertChannel(ctx, o.ID, c.ID, func(x *AlertChannel) error { x.Failures = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAlertChannel(ctx, o.ID, c.ID)
	if err != nil || got.URL != c.URL || got.Failures != 2 {
		t.Fatalf("url must survive updates: %+v %v", got, err)
	}
	p := &AlertChannel{Name: "platform", URL: "generic+https://x.example/y", Events: []string{"*"}}
	if err := s.CreateAlertChannel(ctx, p); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.ListAlertChannels(ctx, ""); len(cs) != 1 || cs[0].ID != p.ID {
		t.Fatalf("platform scope: %+v", cs)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAlertChannel(ctx, o.ID, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("channel survived org deletion: %v", err)
	}
}

func TestAddProviderKeysDedupeAndStatusDefaults(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := &Provider{Name: "p", Type: "openai", Enabled: true, Keys: []ProviderKey{{Value: "key-one-000000"}}}
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	// keys stored before status fields existed read as enabled
	var old Provider
	if err := json.Unmarshal([]byte(`{"id":"x","keys":[{"id":"k","sealed":"s","weight":1}]}`), &old); err != nil || old.Keys[0].Disabled || old.Keys[0].Quarantined {
		t.Fatalf("old key: %+v %v", old.Keys, err)
	}
	added, dups, err := s.AddProviderKeys(ctx, "", p.ID, []ProviderKey{{Value: "key-one-000000"}, {Value: "key-two-000000", Quarantined: true}, {Value: "key-two-000000"}})
	if err != nil || len(added) != 1 || dups != 2 || added[0].Value != "" || added[0].Sealed != "" || added[0].Quarantined {
		t.Fatalf("add: %+v %d %v", added, dups, err)
	}
	if _, _, err := s.AddProviderKeys(ctx, "other-org", p.ID, []ProviderKey{{Value: "x"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner add: %v", err)
	}
	got, err := s.GetProviderSecrets(ctx, p.ID)
	if err != nil || len(got.Keys) != 2 || got.Keys[1].Value != "key-two-000000" {
		t.Fatalf("secrets: %+v %v", got, err)
	}
	if _, err := s.UpdateProviderKeys(ctx, "", p.ID, func(p *Provider) error {
		p.Keys[1].Quarantined, p.Keys[1].Value = true, "ignored-plaintext"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.KV.Get(ctx, "prov/"+p.ID)
	if strings.Contains(string(raw), "key-two") || strings.Contains(string(raw), "ignored-plaintext") || !strings.Contains(string(raw), `"quarantined":true`) {
		t.Fatalf("stored provider: %s", raw)
	}
}
