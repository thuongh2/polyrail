package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	kv, err := OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kv.Close() })
	box, err := NewBox("test-master-key-123456")
	if err != nil {
		t.Fatal(err)
	}
	return New(kv, box)
}

func TestUsersOrgsMembersKeys(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := &User{Email: " Alice@Example.com ", PasswordHash: "h1"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetUser(ctx, u.ID); got.PasswordHash != "h1" {
		t.Fatalf("password hash not persisted: %+v", got)
	}
	if raw, _ := json.Marshal(u); strings.Contains(string(raw), "h1") {
		t.Fatalf("password hash leaks into JSON: %s", raw)
	}
	if got, _ := s.UpdateUser(ctx, u.ID, func(x *User) error { x.Name = "A"; return nil }); got.PasswordHash != "h1" {
		t.Fatal("update dropped password hash")
	}
	if err := s.CreateUser(ctx, &User{Email: "alice@example.com"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate email: %v", err)
	}
	if got, err := s.GetUserByEmail(ctx, "ALICE@example.com"); err != nil || got.ID != u.ID {
		t.Fatalf("by email: %v %v", got, err)
	}
	o := &Org{Name: "acme"}
	if err := s.CreateOrg(ctx, o, u.ID); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := s.UserOrgs(ctx, u.ID); orgs[o.ID] != RoleOwner {
		t.Fatalf("owner membership: %v", orgs)
	}
	k := &APIKey{OrgID: o.ID, Name: "ci"}
	if err := s.CreateKey(ctx, k, "hash1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.KeyByHash(ctx, "hash1")
	if err != nil || got.ID != k.ID || got.Hash != "hash1" {
		t.Fatalf("key by hash: %+v %v", got, err)
	}
	keys, _ := s.ListKeys(ctx, o.ID)
	if len(keys) != 1 || keys[0].Hash != "" {
		t.Fatalf("listed keys must not carry hash: %+v", keys)
	}
	if _, err := s.UpdateKey(ctx, o.ID, k.ID, func(k *APIKey) error { k.Revoked = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.KeyByHash(ctx, "hash1"); !got.Revoked || got.Hash != "hash1" {
		t.Fatalf("revoke lost hash or flag: %+v", got)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KeyByHash(ctx, "hash1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key survives org delete: %v", err)
	}
	if orgs, _ := s.UserOrgs(ctx, u.ID); len(orgs) != 0 {
		t.Fatalf("membership survives org delete: %v", orgs)
	}
}

func TestProviderSecretsAndModelIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := &Provider{Name: "oa", Type: "openai", OrgID: "orgA", Keys: []ProviderKey{{Value: "sk-secret", Weight: 1}}}
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.KV.Get(ctx, "prov/"+p.ID)
	if strings.Contains(string(raw), "sk-secret") {
		t.Fatal("provider key stored in plaintext")
	}
	// update without resending the secret keeps it
	p.Keys[0].Value, p.Name = "", "renamed"
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	ps, err := s.ListProviders(ctx, true)
	if err != nil || ps[0].Keys[0].Value != "sk-secret" || ps[0].Name != "renamed" {
		t.Fatalf("decrypt after update: %+v %v", ps, err)
	}
	// moving a provider to another org is refused
	if err := s.PutProvider(ctx, &Provider{ID: p.ID, Name: "x", Type: "openai", OrgID: "orgB"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org move: %v", err)
	}
	// orgB cannot route to orgA's provider; orgA can
	m := &Model{ID: "x/y", OrgID: "orgB", Targets: []Target{{ProviderID: p.ID, Model: "gpt"}}}
	if err := s.PutModel(ctx, m); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-org target: %v", err)
	}
	m.OrgID = "orgA"
	if err := s.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if ms, _ := s.ListModels(ctx, ""); len(ms) != 0 {
		t.Fatalf("private model listed as public: %v", ms)
	}
	if ms, _ := s.ListModels(ctx, "*"); len(ms) != 1 {
		t.Fatalf("all models: %v", ms)
	}
}

func TestLogsNewestFirstAndPrune(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	logs := []RequestLog{
		{ID: "1", OrgID: "o", At: now.Add(-2 * time.Second)},
		{ID: "2", OrgID: "o", At: now},
		{ID: "3", OrgID: "other", At: now},
		{ID: "old", OrgID: "o", At: now.AddDate(0, 0, -40)},
	}
	if err := s.PutLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListLogs(ctx, "o", 2, 10)
	if len(got) != 2 || got[0].ID != "2" {
		t.Fatalf("logs: %+v", got)
	}
	if err := s.PruneLogs(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListLogs(ctx, "o", 60, 10); len(got) != 2 {
		t.Fatalf("prune: %+v", got)
	}
}
