package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MgmtToken is an org-scoped bearer token for the org admin API (/api/orgs/{org}/...), used by
// scripts and CI instead of a browser session. Only the SHA-256 of the secret is stored.
type MgmtToken struct {
	ID     string `json:"id"`
	OrgID  string `json:"org_id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"` // first chars of the secret, for display
	Hash   string `json:"-"`
	Role   Role   `json:"role"` // admin, member or viewer
	// WorkspaceID limits the token to one workspace's resources (its id, "default" included);
	// "" = the whole org (D31).
	WorkspaceID string     `json:"workspace_id,omitempty"`
	CreatedBy   string     `json:"created_by"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	Revoked     bool       `json:"revoked"`
	CreatedAt   time.Time  `json:"created_at"`
}

// storedMgmtToken keeps the hash in the persisted form while hiding it from API JSON.
type storedMgmtToken struct {
	MgmtToken
	Hash string `json:"hash"`
}

func mgmtKey(org, id string) string { return "mgmt/" + org + "/" + id }

func (s *Store) CreateMgmtToken(ctx context.Context, t *MgmtToken, hash string) error {
	if t.OrgID == "" || strings.Contains(t.OrgID, "/") || hash == "" {
		return fmt.Errorf("%w: org and hash required", ErrInvalid)
	}
	if t.Role != RoleAdmin && t.Role != RoleMember && t.Role != RoleViewer {
		return fmt.Errorf("%w: role must be admin, member or viewer", ErrInvalid)
	}
	t.ID, t.CreatedAt, t.Hash = NewID(), time.Now().UTC(), hash
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + t.OrgID); err != nil {
			return err
		}
		if err := tx.Put("mhash/"+hash, []byte(t.OrgID+"/"+t.ID)); err != nil {
			return err
		}
		return putJSON(tx, mgmtKey(t.OrgID, t.ID), storedMgmtToken{*t, hash})
	})
}

// MgmtTokenByHash finds a token by the hash of its secret (revoked and expired ones too).
func (s *Store) MgmtTokenByHash(ctx context.Context, hash string) (*MgmtToken, error) {
	ref, err := s.KV.Get(ctx, "mhash/"+hash)
	if err != nil {
		return nil, err
	}
	st, err := getJSON[storedMgmtToken](s.get(ctx), "mgmt/"+string(ref))
	if err != nil {
		return nil, err
	}
	st.MgmtToken.Hash = st.Hash
	return &st.MgmtToken, nil
}

func (s *Store) ListMgmtTokens(ctx context.Context, org string) ([]MgmtToken, error) {
	return listJSON[MgmtToken](ctx, s.KV, "mgmt/"+org+"/", "", 0)
}

// UpdateMgmtToken applies fn (name, revoked, last used) and keeps the hash.
func (s *Store) UpdateMgmtToken(ctx context.Context, org, id string, fn func(*MgmtToken)) (*MgmtToken, error) {
	var out *MgmtToken
	err := s.KV.Update(ctx, func(tx Tx) error {
		st, err := getJSON[storedMgmtToken](tx.Get, mgmtKey(org, id))
		if err != nil {
			return err
		}
		fn(&st.MgmtToken)
		st.MgmtToken.ID, st.MgmtToken.OrgID, out = id, org, &st.MgmtToken
		return putJSON(tx, mgmtKey(org, id), st)
	})
	return out, err
}

func (s *Store) DeleteMgmtToken(ctx context.Context, org, id string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		st, err := getJSON[storedMgmtToken](tx.Get, mgmtKey(org, id))
		if err != nil {
			return err
		}
		if err := tx.Delete("mhash/" + st.Hash); err != nil {
			return err
		}
		return tx.Delete(mgmtKey(org, id))
	})
}

func (s *Store) deleteOrgTokens(ctx context.Context, org string) error {
	ts, err := listJSON[storedMgmtToken](ctx, s.KV, "mgmt/"+org+"/", "", 0)
	if err != nil {
		return err
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		for _, t := range ts {
			if err := tx.Delete("mhash/" + t.Hash); err != nil {
				return err
			}
			if err := tx.Delete(mgmtKey(org, t.ID)); err != nil {
				return err
			}
		}
		return nil
	})
}
