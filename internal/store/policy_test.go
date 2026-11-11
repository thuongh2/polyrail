package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPolicyCRUDValidationAndOrgDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "acme"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	bad := []Policy{
		{OrgID: o.ID}, // no name
		{OrgID: o.ID, Name: "b", Budget: &PolicyBudget{LimitMicros: 1, Interval: "hourly"}},
		{OrgID: o.ID, Name: "b", Budget: &PolicyBudget{LimitMicros: 0, Interval: "daily"}},
		{OrgID: o.ID, Name: "f", Filters: []PolicyFilter{{Pattern: "(", Action: "block"}}},
		{OrgID: o.ID, Name: "f", Filters: []PolicyFilter{{Pattern: "x", Action: "drop"}}},
		{OrgID: o.ID, Name: "f", Filters: []PolicyFilter{{Pattern: "x", Action: "redact", Name: "bad name!"}}},
		{OrgID: o.ID, Name: "f", Filters: []PolicyFilter{{Pattern: strings.Repeat("a", MaxPolicyPatternBytes+1), Action: "block"}}},
		{OrgID: o.ID, Name: "m", AllowedModels: []string{""}},
	}
	for i, p := range bad {
		if err := s.PutPolicy(ctx, &p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
	p := &Policy{OrgID: o.ID, Name: "default", IsOrgDefault: true, AllowedModels: []string{"openai/*"},
		Budget: &PolicyBudget{LimitMicros: 5, Interval: "weekly"}, Filters: []PolicyFilter{{Pattern: `\d{4}`, Action: "redact", Name: "pin"}}}
	if err := s.PutPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.CreatedAt.IsZero() {
		t.Fatalf("id/created not set: %+v", p)
	}
	p.Name = "renamed"
	if err := s.PutPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPolicy(ctx, &Policy{ID: "missing", OrgID: o.ID, Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of missing policy: %v", err)
	}
	ps, err := s.ListPolicies(ctx, o.ID)
	if err != nil || len(ps) != 1 || ps[0].Name != "renamed" {
		t.Fatalf("list: %+v %v", ps, err)
	}
	if _, err := s.GetPolicy(ctx, "other-org", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-org get: %v", err)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.ListPolicies(ctx, o.ID); len(ps) != 0 {
		t.Fatalf("policies survive org delete: %+v", ps)
	}
}

func TestNormalizeRegion(t *testing.T) {
	for in, want := range map[string]string{
		"": "global", " Global ": "global", "eu": "europe", "EU-West-1": "europe", "europe-west4": "europe",
		"us": "us", "us-east-1": "us", "USA": "us", "apac": "apac", "ap-southeast-1": "ap-southeast-1",
	} {
		if got := NormalizeRegion(in); got != want {
			t.Errorf("NormalizeRegion(%q) = %q, want %q", in, got, want)
		}
	}
	p := &Policy{Name: "r", AllowedRegions: []string{"EU", "us-east-1", "eu"}}
	if err := p.validate(); err != nil || strings.Join(p.AllowedRegions, ",") != "europe,us" {
		t.Fatalf("allowed_regions normalized and deduplicated: %v %v", p.AllowedRegions, err)
	}
}
