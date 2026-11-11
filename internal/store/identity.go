package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Enterprise identity per org (docs/API.md "Enterprise identity"): verified email domains,
// SAML SSO, SCIM provisioning and IdP group → role mappings. Everything an org owns lives under
// "ident/{org}/" (deleted with the org); the only global keys are domain claims "vdomain/{name}".

// Domain is an email domain an org claims. It is verified by a DNS TXT record
// _polyrail-verify.<name> = polyrail-verification=<token>; one org at most holds a verified claim.
type Domain struct {
	ID         string     `json:"id"`
	OrgID      string     `json:"org_id"`
	Name       string     `json:"name"`
	Token      string     `json:"token"` // published in DNS, so not a secret
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// GroupMapping maps an IdP group (SCIM group displayName or SAML groups attribute value) to an
// org role, or with WorkspaceID to a role in that workspace (D31; the org role is unchanged).
type GroupMapping struct {
	Group       string `json:"group"`
	Role        Role   `json:"role"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// SAMLConfig is an org's SAML 2.0 identity provider. The SP signing key is stored sealed
// apart from this record (SetSAMLKey); SPCertificate is its public half.
type SAMLConfig struct {
	Enabled           bool       `json:"enabled"`
	IdPMetadataURL    string     `json:"idp_metadata_url,omitempty"`
	IdPMetadataXML    string     `json:"idp_metadata_xml,omitempty"` // as given, or as fetched from the URL
	IdPEntityID       string     `json:"idp_entity_id,omitempty"`    // from the metadata
	MetadataFetchedAt *time.Time `json:"metadata_fetched_at,omitempty"`
	DefaultRole       Role       `json:"default_role"`
	AttrEmail         string     `json:"attribute_email,omitempty"`
	AttrName          string     `json:"attribute_name,omitempty"`
	AttrGroups        string     `json:"attribute_groups,omitempty"`
	AllowIdPInitiated bool       `json:"allow_idp_initiated"`
	// Enforce refuses password sign-in for users whose email is on one of the org's verified
	// domains (platform operators excepted).
	Enforce       bool   `json:"enforce"`
	SPCertificate string `json:"sp_certificate,omitempty"`
}

// SCIMConfig turns on SCIM 2.0 provisioning. Only the SHA-256 of the bearer token is stored
// (SetSCIMTokenHash); TokenPrefix is for display.
type SCIMConfig struct {
	Enabled        bool       `json:"enabled"`
	DefaultRole    Role       `json:"default_role"`
	TokenPrefix    string     `json:"token_prefix,omitempty"`
	TokenCreatedAt *time.Time `json:"token_created_at,omitempty"`
}

// Identity is an org's identity settings.
type Identity struct {
	OrgID         string         `json:"org_id"`
	SAML          SAMLConfig     `json:"saml"`
	SCIM          SCIMConfig     `json:"scim"`
	GroupMappings []GroupMapping `json:"group_mappings"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// SCIMUser is the SCIM view of a user in one org: the attributes the IdP manages and whether
// the IdP has the user active. A deprovisioned user stays recorded (Active false) so SAML
// just-in-time provisioning cannot bring them back; Deleted hides the record from SCIM reads.
type SCIMUser struct {
	OrgID      string    `json:"org_id"`
	UserID     string    `json:"user_id"`
	UserName   string    `json:"user_name"`
	ExternalID string    `json:"external_id,omitempty"`
	GivenName  string    `json:"given_name,omitempty"`
	FamilyName string    `json:"family_name,omitempty"`
	Formatted  string    `json:"formatted,omitempty"`
	Active     bool      `json:"active"`
	Deleted    bool      `json:"deleted,omitempty"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
}

// SCIMGroup is an IdP group pushed over SCIM; Members are Polyrail user ids.
type SCIMGroup struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	DisplayName string    `json:"display_name"`
	ExternalID  string    `json:"external_id,omitempty"`
	Members     []string  `json:"members"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
}

func identPrefix(org string) string   { return "ident/" + org + "/" }
func domainKey(org, id string) string { return identPrefix(org) + "d/" + id }

var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormDomain lowercases a domain name and checks it is a public-looking DNS name.
func NormDomain(name string) (string, error) {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	labels := strings.Split(n, ".")
	if len(n) > 253 || len(labels) < 2 {
		return "", fmt.Errorf("%w: domain must be a DNS name such as example.com", ErrInvalid)
	}
	for _, l := range labels {
		if !domainLabel.MatchString(l) {
			return "", fmt.Errorf("%w: domain must be a DNS name such as example.com", ErrInvalid)
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", fmt.Errorf("%w: domain must be a DNS name, not an address", ErrInvalid)
	}
	return n, nil
}

// EmailDomain is the normalized domain part of an email address ("" if there is none).
func EmailDomain(email string) string {
	_, d, ok := strings.Cut(normEmail(email), "@")
	if !ok {
		return ""
	}
	return d
}

func (s *Store) CreateDomain(ctx context.Context, d *Domain) error {
	name, err := NormDomain(d.Name)
	if err != nil {
		return err
	}
	tok := make([]byte, 20)
	if _, err := rand.Read(tok); err != nil {
		return err
	}
	existing, err := s.ListDomains(ctx, d.OrgID)
	if err != nil {
		return err
	}
	if len(existing) >= 50 {
		return fmt.Errorf("%w: at most 50 domains per organization", ErrInvalid)
	}
	for _, x := range existing {
		if x.Name == name {
			return ErrExists
		}
	}
	d.ID, d.Name, d.Token, d.Verified, d.VerifiedAt, d.CreatedAt = NewID(), name, hex.EncodeToString(tok), false, nil, time.Now().UTC()
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + d.OrgID); err != nil {
			return err
		}
		return putJSON(tx, domainKey(d.OrgID, d.ID), d)
	})
}

func (s *Store) ListDomains(ctx context.Context, org string) ([]Domain, error) {
	return listJSON[Domain](ctx, s.KV, identPrefix(org)+"d/", "", 0)
}

func (s *Store) GetDomain(ctx context.Context, org, id string) (*Domain, error) {
	return getJSON[Domain](s.get(ctx), domainKey(org, id))
}

// VerifyDomain marks the domain verified and claims it for the org. It fails with ErrExists
// when another org holds a verified claim on the same name.
func (s *Store) VerifyDomain(ctx context.Context, org, id string) (*Domain, error) {
	var out *Domain
	err := s.KV.Update(ctx, func(tx Tx) error {
		d, err := getJSON[Domain](tx.Get, domainKey(org, id))
		if err != nil {
			return err
		}
		ref := org + "/" + id
		if cur, err := tx.Get("vdomain/" + d.Name); err == nil && string(cur) != ref {
			return ErrExists
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := tx.Put("vdomain/"+d.Name, []byte(ref)); err != nil {
			return err
		}
		now := time.Now().UTC()
		d.Verified, d.VerifiedAt, out = true, &now, d
		return putJSON(tx, domainKey(org, id), d)
	})
	return out, err
}

func (s *Store) DeleteDomain(ctx context.Context, org, id string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		d, err := getJSON[Domain](tx.Get, domainKey(org, id))
		if err != nil {
			return err
		}
		if err := releaseClaim(tx, d); err != nil {
			return err
		}
		return tx.Delete(domainKey(org, id))
	})
}

func releaseClaim(tx Tx, d *Domain) error {
	if cur, err := tx.Get("vdomain/" + d.Name); err == nil && string(cur) == d.OrgID+"/"+d.ID {
		return tx.Delete("vdomain/" + d.Name)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// DomainOrg returns the org holding the verified claim on a domain (ErrNotFound if none).
func (s *Store) DomainOrg(ctx context.Context, name string) (string, error) {
	n, err := NormDomain(name)
	if err != nil {
		return "", ErrNotFound
	}
	ref, err := s.KV.Get(ctx, "vdomain/"+n)
	if err != nil {
		return "", err
	}
	org, _, _ := strings.Cut(string(ref), "/")
	return org, nil
}

// deleteOrgIdentity drops the org's domain claims and identity records.
func (s *Store) deleteOrgIdentity(ctx context.Context, org string) error {
	ds, err := s.ListDomains(ctx, org)
	if err != nil {
		return err
	}
	if err := s.KV.Update(ctx, func(tx Tx) error {
		for i := range ds {
			if err := releaseClaim(tx, &ds[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.DeletePrefix(ctx, identPrefix(org))
}

// GetIdentity returns the org's identity settings (zero settings if none are stored).
func (s *Store) GetIdentity(ctx context.Context, org string) (*Identity, error) {
	id, err := getJSON[Identity](s.get(ctx), identPrefix(org)+"config")
	if errors.Is(err, ErrNotFound) {
		return &Identity{OrgID: org, GroupMappings: []GroupMapping{}}, nil
	}
	return id, err
}

func (s *Store) UpdateIdentity(ctx context.Context, org string, fn func(*Identity) error) (*Identity, error) {
	var out *Identity
	err := s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + org); err != nil {
			return err
		}
		id, err := getJSON[Identity](tx.Get, identPrefix(org)+"config")
		if errors.Is(err, ErrNotFound) {
			id, err = &Identity{}, nil
		}
		if err != nil {
			return err
		}
		if err := fn(id); err != nil {
			return err
		}
		if id.GroupMappings == nil {
			id.GroupMappings = []GroupMapping{}
		}
		id.OrgID, id.UpdatedAt, out = org, time.Now().UTC(), id
		return putJSON(tx, identPrefix(org)+"config", id)
	})
	return out, err
}

// EnsureSAMLKey stores the org's SP key bundle (private key and certificate PEM), sealed, unless
// one is already stored, and returns the stored bundle: concurrent callers agree on one key.
func (s *Store) EnsureSAMLKey(ctx context.Context, org, bundlePEM string) (string, error) {
	if s.box == nil {
		return "", errors.New("store has no secret box")
	}
	sealed, err := s.box.Seal(bundlePEM)
	if err != nil {
		return "", err
	}
	var cur []byte
	err = s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + org); err != nil {
			return err
		}
		v, err := tx.Get(identPrefix(org) + "spkey")
		if err == nil {
			cur = v
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		cur = []byte(sealed)
		return tx.Put(identPrefix(org)+"spkey", cur)
	})
	if err != nil {
		return "", err
	}
	return s.box.Open(string(cur))
}

// SAMLKey returns the org's SP key bundle (private key and certificate PEM).
func (s *Store) SAMLKey(ctx context.Context, org string) (string, error) {
	raw, err := s.KV.Get(ctx, identPrefix(org)+"spkey")
	if err != nil {
		return "", err
	}
	if s.box == nil {
		return "", errors.New("store has no secret box")
	}
	return s.box.Open(string(raw))
}

// SetSCIMTokenHash stores the SHA-256 of the org's SCIM bearer token ("" removes it).
func (s *Store) SetSCIMTokenHash(ctx context.Context, org, hash string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		if hash == "" {
			return tx.Delete(identPrefix(org) + "scimhash")
		}
		return tx.Put(identPrefix(org)+"scimhash", []byte(hash))
	})
}

func (s *Store) SCIMTokenHash(ctx context.Context, org string) (string, error) {
	raw, err := s.KV.Get(ctx, identPrefix(org)+"scimhash")
	return string(raw), err
}

// ---- SCIM users and groups ----

func scimUserKey(org, uid string) string { return identPrefix(org) + "u/" + uid }
func scimGroupKey(org, id string) string { return identPrefix(org) + "g/" + id }

func (s *Store) GetSCIMUser(ctx context.Context, org, uid string) (*SCIMUser, error) {
	return getJSON[SCIMUser](s.get(ctx), scimUserKey(org, uid))
}

func (s *Store) PutSCIMUser(ctx context.Context, u *SCIMUser) error {
	return s.KV.Update(ctx, func(tx Tx) error { return putJSON(tx, scimUserKey(u.OrgID, u.UserID), u) })
}

func (s *Store) ListSCIMUsers(ctx context.Context, org string) ([]SCIMUser, error) {
	return listJSON[SCIMUser](ctx, s.KV, identPrefix(org)+"u/", "", 0)
}

func (s *Store) GetSCIMGroup(ctx context.Context, org, id string) (*SCIMGroup, error) {
	return getJSON[SCIMGroup](s.get(ctx), scimGroupKey(org, id))
}

func (s *Store) PutSCIMGroup(ctx context.Context, g *SCIMGroup) error {
	if g.ID == "" {
		g.ID = NewID()
	}
	if g.Members == nil {
		g.Members = []string{}
	}
	return s.KV.Update(ctx, func(tx Tx) error { return putJSON(tx, scimGroupKey(g.OrgID, g.ID), g) })
}

func (s *Store) DeleteSCIMGroup(ctx context.Context, org, id string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get(scimGroupKey(org, id)); err != nil {
			return err
		}
		return tx.Delete(scimGroupKey(org, id))
	})
}

func (s *Store) ListSCIMGroups(ctx context.Context, org string) ([]SCIMGroup, error) {
	return listJSON[SCIMGroup](ctx, s.KV, identPrefix(org)+"g/", "", 0)
}

// ConsumeOnce records key until expires and fails with ErrExists if it is already recorded
// and unexpired (SAML assertion replay protection, shared by every node through the KV).
// Expired records are pruned a few at a time.
func (s *Store) ConsumeOnce(ctx context.Context, key string, expires time.Time) error {
	now := time.Now()
	if old, err := s.KV.Scan(ctx, "once/", "", 50); err == nil {
		_ = s.KV.Update(ctx, func(tx Tx) error {
			for _, it := range old {
				if t, err := time.Parse(time.RFC3339, string(it.Value)); err == nil && now.After(t) {
					_ = tx.Delete(it.Key)
				}
			}
			return nil
		})
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		if v, err := tx.Get("once/" + key); err == nil {
			if t, err := time.Parse(time.RFC3339, string(v)); err != nil || now.Before(t) {
				return ErrExists
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return tx.Put("once/"+key, []byte(expires.UTC().Format(time.RFC3339)))
	})
}
