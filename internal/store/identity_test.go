package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDomainClaimIsExclusive(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a, b := &Org{Name: "a"}, &Org{Name: "b"}
	for _, o := range []*Org{a, b} {
		if err := s.CreateOrg(ctx, o, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"", "localhost", "*.acme.com", "10.0.0.1", "acme..com", "-acme.com", "a b.com"} {
		if err := s.CreateDomain(ctx, &Domain{OrgID: a.ID, Name: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("domain %q: %v", bad, err)
		}
	}
	da := &Domain{OrgID: a.ID, Name: "Acme.COM."}
	if err := s.CreateDomain(ctx, da); err != nil || da.Name != "acme.com" || len(da.Token) < 32 {
		t.Fatalf("create: %v %+v", err, da)
	}
	if err := s.CreateDomain(ctx, &Domain{OrgID: a.ID, Name: "acme.com"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate in one org: %v", err)
	}
	db := &Domain{OrgID: b.ID, Name: "acme.com"} // a pending claim by another org is allowed
	if err := s.CreateDomain(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyDomain(ctx, a.ID, da.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyDomain(ctx, b.ID, db.ID); !errors.Is(err, ErrExists) {
		t.Fatalf("second org verified the same domain: %v", err)
	}
	if org, err := s.DomainOrg(ctx, "ACME.com"); err != nil || org != a.ID {
		t.Fatalf("domain org: %q %v", org, err)
	}
	// deleting the claim frees the domain
	if err := s.DeleteDomain(ctx, a.ID, da.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyDomain(ctx, b.ID, db.ID); err != nil {
		t.Fatal(err)
	}
	// deleting the org frees its claims and identity records
	if _, err := s.EnsureSAMLKey(ctx, b.ID, "-----BEGIN PRIVATE KEY-----secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrg(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DomainOrg(ctx, "acme.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim kept after org delete: %v", err)
	}
	if items, _ := s.KV.Scan(ctx, "ident/"+b.ID+"/", "", 0); len(items) != 0 {
		t.Fatalf("identity records kept: %d", len(items))
	}
}

func TestIdentitySecretsSealed(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "a"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	if k, err := s.EnsureSAMLKey(ctx, o.ID, "PRIVATE-KEY-PEM"); err != nil || k != "PRIVATE-KEY-PEM" {
		t.Fatal(k, err)
	}
	if k, err := s.EnsureSAMLKey(ctx, o.ID, "OTHER"); err != nil || k != "PRIVATE-KEY-PEM" {
		t.Fatalf("second key replaced the first: %q %v", k, err)
	}
	items, _ := s.KV.Scan(ctx, "ident/", "", 0)
	for _, it := range items {
		if strings.Contains(string(it.Value), "PRIVATE-KEY-PEM") {
			t.Fatalf("key stored in plaintext at %s", it.Key)
		}
	}
	if k, err := s.SAMLKey(ctx, o.ID); err != nil || k != "PRIVATE-KEY-PEM" {
		t.Fatalf("open key: %q %v", k, err)
	}
	id, err := s.UpdateIdentity(ctx, o.ID, func(x *Identity) error { x.SCIM.Enabled = true; return nil })
	if err != nil || !id.SCIM.Enabled {
		t.Fatal(err)
	}
	if _, err := s.UpdateIdentity(ctx, "nope", func(*Identity) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("identity for a missing org: %v", err)
	}
}
