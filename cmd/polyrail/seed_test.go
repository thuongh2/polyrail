package main

import (
	"context"
	"testing"

	"github.com/polyrail/polyrail/internal/catalog"
	"github.com/polyrail/polyrail/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kv.Close() })
	box, _ := store.NewBox("test-master-key-123456")
	return store.New(kv, box)
}

func TestSeedCatalogOnlyOnEmptyStore(t *testing.T) {
	ctx := context.Background()
	c, _ := catalog.Load()

	s := testStore(t)
	if err := seedCatalog(ctx, s); err != nil {
		t.Fatal(err)
	}
	ps, _ := s.ListProviders(ctx, false)
	ms, _ := s.ListModels(ctx, "")
	if len(ps) != len(c.Providers) || len(ms) != len(c.Models) {
		t.Fatalf("seeded %d providers, %d models; want %d, %d", len(ps), len(ms), len(c.Providers), len(c.Models))
	}
	for _, p := range ps {
		if p.Enabled || len(p.Keys) != 0 || p.OrgID != "" {
			t.Fatalf("seeded provider must be platform, disabled, keyless: %+v", p)
		}
	}
	for _, m := range ms {
		if !m.Enabled {
			t.Fatalf("seeded model %s disabled", m.ID)
		}
	}
	if a, _ := s.ListAudit(ctx, "_", 10); len(a) != 1 || a[0].Action != "catalog.seed" {
		t.Fatalf("want one catalog.seed audit entry, got %+v", a)
	}
	// second start: providers exist, so nothing is seeded again (deleted models stay deleted)
	_ = s.DeleteModel(ctx, "", "openai/gpt-4o")
	if err := seedCatalog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetModel(ctx, "", "openai/gpt-4o"); err == nil {
		t.Fatal("seed ran again on a store that already has providers")
	}

	t.Setenv("SEED_CATALOG", "0")
	off := testStore(t)
	if err := seedCatalog(ctx, off); err != nil {
		t.Fatal(err)
	}
	if ps, _ := off.ListProviders(ctx, false); len(ps) != 0 {
		t.Fatalf("SEED_CATALOG=0 still seeded %d providers", len(ps))
	}
}
