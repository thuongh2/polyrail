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
	for _, bad := range []Model{{Strategy: "fastest"}, {Targets: []Target{{ProviderID: p.ID, Model: "gpt", InputPrice: -1}}}} {
		b := *m
		b.Strategy = bad.Strategy
		if bad.Targets != nil {
			b.Targets = bad.Targets
		}
		if err := s.PutModel(ctx, &b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid routing config accepted: %+v %v", bad, err)
		}
	}
	m.Strategy = "cache-affinity"
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

func TestPruneHealth(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	hour := func(t time.Time) string { return "health/" + t.UTC().Format("2006010215") + "/v/m|p" }
	now := time.Now()
	old, fresh := hour(now.AddDate(0, 0, -31)), hour(now.AddDate(0, 0, -29))
	if err := s.KV.AddAgg(ctx, map[string]Agg{old: {1}, fresh: {1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneHealth(ctx, 30); err != nil {
		t.Fatal(err)
	}
	m, _ := s.KV.ScanAgg(ctx, "health/")
	if _, ok := m[old]; ok || len(m) != 1 {
		t.Fatalf("health after prune: %v", m)
	}
}

func TestProviderConfigSecretsSealedAndKept(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := &Provider{Name: "br", Type: "bedrock", Keys: []ProviderKey{
		{Bedrock: &BedrockConfig{AccessKey: "AKIA-SECRET", SecretKey: "aws-SECRET", SessionToken: "tok-SECRET", Region: "us-east-1",
			Deployments: map[string]string{"claude": "anthropic.claude-v2"}}},
		{Vertex: &VertexConfig{ProjectID: "proj", Region: "us-central1", AuthCredentials: `{"private_key":"gcp-SECRET"}`}},
	}}
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.KV.Get(ctx, "prov/"+p.ID)
	if strings.Contains(string(raw), "SECRET") || !strings.Contains(string(raw), "sealed_secret_key") || !strings.Contains(string(raw), "us-east-1") {
		t.Fatalf("config secrets not sealed at rest: %s", raw)
	}
	// redaction keeps editable config, drops every secret
	red, _ := json.Marshal(p.Keys[0].Redacted())
	red2, _ := json.Marshal(p.Keys[1].Redacted())
	if strings.Contains(string(red)+string(red2), "sealed") || !strings.Contains(string(red), "anthropic.claude-v2") || !strings.Contains(string(red2), "us-central1") {
		t.Fatalf("redacted: %s %s", red, red2)
	}
	// empty inputs keep the stored secrets; a new session token replaces only that one
	p.Keys[0].Bedrock.SessionToken = "tok2-SECRET"
	p.Keys[1].Vertex.Region = "europe-west4"
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	ps, err := s.ListProviders(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	b, v := ps[0].Keys[0].Bedrock, ps[0].Keys[1].Vertex
	if b.AccessKey != "AKIA-SECRET" || b.SecretKey != "aws-SECRET" || b.SessionToken != "tok2-SECRET" ||
		v.AuthCredentials != `{"private_key":"gcp-SECRET"}` || v.Region != "europe-west4" {
		t.Fatalf("decrypted config: %+v %+v", b, v)
	}
	// stored secrets never follow a type change
	p.Type = "openai"
	if err := s.PutProvider(ctx, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("type change kept config secrets: %v", err)
	}
}

func TestAzureEndpointChangeNeedsKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := &Provider{Name: "az", Type: "azure", Keys: []ProviderKey{{Value: "az-key", Azure: &AzureConfig{Endpoint: "https://a.openai.azure.com"}}}}
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Keys[0].Azure = &AzureConfig{Endpoint: "https://evil.example"}
	if err := s.PutProvider(ctx, p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("endpoint change kept the key: %v", err)
	}
	p.Keys[0].Value = "az-key-2"
	if err := s.PutProvider(ctx, p); err != nil {
		t.Fatalf("endpoint change with a new key: %v", err)
	}
}
