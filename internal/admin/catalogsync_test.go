package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/catalog"
	"github.com/agent-gateway/agent-gateway/internal/store"
)

func TestCatalogTypesAreSupported(t *testing.T) {
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Providers {
		if !supportedTypes[p.Type] {
			t.Errorf("catalog provider type %q is not a supported provider type", p.Type)
		}
	}
}

func TestCatalogSyncAndVisibility(t *testing.T) {
	f := setup(t, Config{})
	ctx := context.Background()
	c, _ := catalog.Load()

	expect(t, f.do(t, "owner", "POST", "/api/admin/catalog/sync", ""), 403, "org owner sync")
	expect(t, f.req(t, "op", "POST", "/api/admin/catalog/sync", "", false), 403, "sync without CSRF header")

	var got map[string]int
	r := f.do(t, "op", "POST", "/api/admin/catalog/sync", "")
	expect(t, r, 200, "operator sync")
	r.json(t, &got)
	if got["providers_added"] != len(c.Providers) || got["models_added"] != len(c.Models) {
		t.Fatalf("first sync = %v, want %d providers, %d models", got, len(c.Providers), len(c.Models))
	}
	r = f.do(t, "op", "POST", "/api/admin/catalog/sync", "")
	expect(t, r, 200, "second sync")
	r.json(t, &got)
	if got["providers_added"] != 0 || got["models_added"] != 0 {
		t.Fatalf("second sync must add nothing: %v", got)
	}
	audit, _ := f.s.ListAudit(ctx, "_", 10)
	if len(audit) == 0 || audit[0].Action != "catalog.sync" {
		t.Fatalf("no catalog.sync audit entry: %+v", audit)
	}

	// every catalog provider is disabled: nothing is listed or reachable yet
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	r = f.do(t, "", "GET", "/api/public/models", "")
	if strings.Contains(r.body, "openai/gpt-4o") {
		t.Fatalf("catalog model listed while its provider is disabled: %.200s", r.body)
	}
	expect(t, f.do(t, "", "GET", "/api/public/models/openai/gpt-4o", ""), 404, "hidden model page")
	if r = f.do(t, "owner", "GET", "/api/orgs/"+f.orgA.ID+"/models", ""); strings.Contains(r.body, "openai/gpt-4o") {
		t.Fatal("org console lists a model with no enabled provider")
	}

	p, _ := f.s.GetProvider(ctx, catalog.ProviderID("openai"))
	p.Keys, p.Enabled = []store.ProviderKey{{Value: "sk-test"}}, true
	if err := f.s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if r = f.do(t, "", "GET", "/api/public/models", ""); !strings.Contains(r.body, `"openai/gpt-4o"`) || strings.Contains(r.body, `"anthropic/claude-sonnet-4-5"`) {
		t.Fatalf("after enabling openai only its models are listed: %.300s", r.body)
	}
	expect(t, f.do(t, "", "GET", "/api/public/models/openai/gpt-4o", ""), 200, "live model page")
}
