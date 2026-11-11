package gateway

import (
	"context"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

func TestAllowTargetRegions(t *testing.T) {
	ep, err := compilePolicy(store.APIKey{ID: "k"}, []store.Policy{
		{Name: "eu", IsOrgDefault: true, AllowedRegions: []string{"EU"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		region string
		decl   bool
		want   bool
	}{
		{"europe", true, true},
		{"eu-west-1", true, true},
		{"us", true, false},
		{"", true, false},  // empty region is global, not listed
		{"", false, false}, // undeclared privacy is global too
		{"global", true, false},
	}
	for _, c := range cases {
		p := store.Provider{Name: "p"}
		if c.decl {
			p.Privacy = &store.ProviderPrivacy{Region: c.region}
		}
		if got := ep.AllowTarget(p); got != c.want {
			t.Errorf("region %q (declared %v): %v, want %v", c.region, c.decl, got, c.want)
		}
	}
	ep, _ = compilePolicy(store.APIKey{ID: "k"}, []store.Policy{{Name: "g", IsOrgDefault: true, AllowedRegions: []string{"global", "us"}}})
	if !ep.AllowTarget(store.Provider{}) || !ep.AllowTarget(store.Provider{Privacy: &store.ProviderPrivacy{Region: "us-east-1"}}) ||
		ep.AllowTarget(store.Provider{Privacy: &store.ProviderPrivacy{Region: "eu"}}) {
		t.Fatal("global listed: undeclared and us allowed, europe refused")
	}
}

// setRegion declares the region of the provider named name and reloads the catalog.
func (f *fixture) setRegion(t *testing.T, name, region string) {
	t.Helper()
	ctx := context.Background()
	for id, p := range f.g.Snapshot().Providers {
		if p.Name != name {
			continue
		}
		pp, _ := f.g.S.GetProviderSecrets(ctx, id)
		pp.Privacy = &store.ProviderPrivacy{Region: region}
		for i := range pp.Keys {
			pp.Keys[i].Sealed = ""
		}
		if err := f.g.S.PutProvider(ctx, pp); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRegionLockAndPolicyRegions(t *testing.T) {
	f := setupWith(t, func(c *Config) { c.RegionLock = "europe" })
	f.setRegion(t, "ok", "eu-central-1")
	before := f.badHits.Load()
	resp := f.post(t, "/v1/chat/completions", f.key, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 || f.badHits.Load() != before {
		t.Fatalf("REGION_LOCK=europe must only use the eu provider: %d, bad hits %d->%d", resp.StatusCode, before, f.badHits.Load())
	}
	f.setRegion(t, "ok", "us")
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	e := errBody(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || e["code"] != "policy_no_eligible_provider" || e["policy"] != "REGION_LOCK" {
		t.Fatalf("no provider in the locked region: %d %v", resp.StatusCode, e)
	}

	// policy allowed_regions without a lock
	f = setup(t)
	f.setRegion(t, "ok", "us")
	f.putPolicy(t, store.Policy{Name: "us only", IsOrgDefault: true, AllowedRegions: []string{"us"}})
	before = f.badHits.Load()
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	resp.Body.Close()
	if resp.StatusCode != 200 || f.badHits.Load() != before {
		t.Fatalf("allowed_regions us: %d, bad hits %d->%d", resp.StatusCode, before, f.badHits.Load())
	}
	f.setRegion(t, "ok", "europe")
	resp = f.post(t, "/v1/chat/completions", f.key, polChat)
	e = errBody(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || e["code"] != "policy_no_eligible_provider" || e["policy"] != "us only" {
		t.Fatalf("allowed_regions refusal: %d %v", resp.StatusCode, e)
	}
}
