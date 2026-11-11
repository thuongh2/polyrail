package catalog

import (
	"context"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

func TestEmbeddedCatalog(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) < 10 || len(c.Models) < 100 {
		t.Fatalf("catalog too small: %d providers, %d models", len(c.Providers), len(c.Models))
	}
	types := map[string]bool{}
	for _, p := range c.Providers {
		if types[p.Type] || p.Name == "" {
			t.Fatalf("provider %+v duplicated or unnamed", p)
		}
		types[p.Type] = true
	}
	ids := map[string]Model{}
	for _, m := range c.Models {
		if _, dup := ids[m.ID]; dup {
			t.Fatalf("duplicate model id %s", m.ID)
		}
		ids[m.ID] = m
		if len(m.Targets) == 0 || m.Name == "" || m.Kind == "" {
			t.Fatalf("incomplete model %+v", m)
		}
		if m.InputPrice+m.OutputPrice+m.AudioMinutePrice+m.ImagePrice+m.VideoSecondPrice <= 0 {
			t.Fatalf("model %s has no price", m.ID)
		}
		for _, tg := range m.Targets {
			if !types[tg.Type] || tg.Model == "" {
				t.Fatalf("model %s target %+v: unknown provider type", m.ID, tg)
			}
		}
	}
	for id, first := range map[string]string{"openai/gpt-4o": "openai", "anthropic/claude-sonnet-4-5": "anthropic",
		"google/gemini-2.5-pro": "gemini", "openai/text-embedding-3-small": "openai"} {
		m, ok := ids[id]
		if !ok || m.InputPrice <= 0 || m.Targets[0].Type != first {
			t.Fatalf("%s: missing, unpriced or not first-party first: %+v", id, m)
		}
	}
	if m := ids["openai/text-embedding-3-small"]; m.Kind != "embedding" {
		t.Fatalf("embedding model kind = %q", m.Kind)
	}
	if m := ids["openai/gpt-4o"]; m.InputPrice != 2_500_000 || m.OutputPrice != 10_000_000 || m.ContextLength != 128000 {
		t.Fatalf("gpt-4o price/context: %+v", m)
	}
}

func TestSyncIsAdditiveAndIdempotent(t *testing.T) {
	ctx := context.Background()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kv.Close() })
	box, _ := store.NewBox("test-master-key-123456")
	s := store.New(kv, box)
	c, _ := Load()

	// an operator-edited model keeps its edits
	if err := s.CreateKeylessProvider(ctx, &store.Provider{ID: ProviderID("openai"), Name: "mine", Type: "openai"}); err != nil {
		t.Fatal(err)
	}
	mine := store.Model{ID: "openai/gpt-4o", Name: "Mine", InputPrice: 1, Enabled: false,
		Targets: []store.Target{{ProviderID: ProviderID("openai"), Model: "gpt-4o", Weight: 1}}}
	if err := s.PutModel(ctx, &mine); err != nil {
		t.Fatal(err)
	}
	np, nm, err := Sync(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if np != len(c.Providers)-1 || nm != len(c.Models)-1 {
		t.Fatalf("added %d providers, %d models; want %d, %d", np, nm, len(c.Providers)-1, len(c.Models)-1)
	}
	if p, _ := s.GetProvider(ctx, ProviderID("openai")); p.Name != "mine" {
		t.Fatalf("existing provider changed: %+v", p)
	}
	if m, _ := s.GetModel(ctx, "", "openai/gpt-4o"); m.Name != "Mine" || m.Enabled {
		t.Fatalf("existing model changed: %+v", m)
	}
	p, err := s.GetProvider(ctx, ProviderID("anthropic"))
	if err != nil || p.Enabled || len(p.Keys) != 0 || p.Type != "anthropic" || p.Name != "anthropic" {
		t.Fatalf("catalog provider: %+v %v", p, err)
	}
	m, err := s.GetModel(ctx, "", "anthropic/claude-sonnet-4-5")
	if err != nil || !m.Enabled || m.Targets[0].ProviderID != ProviderID("anthropic") || m.InputPrice <= 0 {
		t.Fatalf("catalog model: %+v %v", m, err)
	}
	if np, nm, err := Sync(ctx, s); err != nil || np != 0 || nm != 0 {
		t.Fatalf("second sync added %d, %d (%v)", np, nm, err)
	}
}
