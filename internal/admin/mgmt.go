package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

// Management API tokens (docs/API.md "Management tokens"): org-scoped bearer tokens for the
// org admin API, for scripts and CI. A token acts as a synthetic principal with its role in its
// org only: never on platform routes (/api/admin, /api/auth, org listing) and never on /v1.

const MgmtPrefix = "pr-mgmt-"

type mgmtCtx struct{}

// viaToken reports whether the request is authenticated by a management token.
func viaToken(r *http.Request) bool { return r.Context().Value(mgmtCtx{}) != nil }

// mgmtBearer returns the management token secret sent with r, or "".
func mgmtBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") && strings.HasPrefix(strings.TrimSpace(h[7:]), MgmtPrefix) {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// tokenOrg authenticates a management token for an org route. The token's role is capped by its
// creator's current role, so removing or demoting the creator narrows or ends the token. CSRF is
// not needed: a cross-site page cannot set the Authorization header.
func (a *API) tokenOrg(min store.Role, h http.HandlerFunc, w http.ResponseWriter, r *http.Request, secret string) {
	ctx := r.Context()
	t, err := a.S.MgmtTokenByHash(ctx, gateway.HashKey(secret))
	if errors.Is(err, store.ErrNotFound) || (err == nil && (t.Revoked || (t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt)))) {
		fail(w, errUnauthorized)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if t.OrgID != r.PathValue("org") {
		fail(w, store.ErrNotFound) // like a non-member: org ids cannot be probed
		return
	}
	rl, err := a.creatorRole(ctx, t)
	if err != nil {
		fail(w, err)
		return
	}
	if rl.Rank() > t.Role.Rank() {
		rl = t.Role
	}
	if rl.Rank() < min.Rank() {
		fail(w, errForbidden)
		return
	}
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > time.Minute { // one write per minute at most
		now := time.Now().UTC()
		_, _ = a.S.UpdateMgmtToken(ctx, t.OrgID, t.ID, func(x *store.MgmtToken) { x.LastUsedAt = &now })
	}
	u := &store.User{ID: "token:" + t.ID, Email: "token:" + t.Name, Name: t.Name}
	ctx = context.WithValue(ctx, userCtx{}, u)
	ctx = context.WithValue(ctx, roleCtx{}, rl)
	h(w, r.WithContext(context.WithValue(ctx, mgmtCtx{}, t.ID)))
}

// creatorRole is the token creator's current role in the token's org (owner for operators).
func (a *API) creatorRole(ctx context.Context, t *store.MgmtToken) (store.Role, error) {
	u, err := a.S.GetUser(ctx, t.CreatedBy)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
		return "", errUnauthorized
	}
	if err != nil {
		return "", err
	}
	if u.Operator {
		return store.RoleOwner, nil
	}
	m, err := a.S.GetMember(ctx, t.OrgID, u.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errUnauthorized
	}
	if err != nil {
		return "", err
	}
	return m.Role, nil
}

func (a *API) registerMgmt(mux *http.ServeMux) {
	p := "/api/orgs/{org}/mgmt-tokens"
	wrap := func(h http.HandlerFunc) http.HandlerFunc { return a.org(store.RoleAdmin, a.sessionOnly(h)) }
	mux.HandleFunc("GET "+p, wrap(a.listMgmtTokens))
	mux.HandleFunc("POST "+p, wrap(a.createMgmtToken))
	mux.HandleFunc("PATCH "+p+"/{id}", wrap(a.patchMgmtToken))
	mux.HandleFunc("DELETE "+p+"/{id}", wrap(a.deleteMgmtToken))
}

// sessionOnly refuses management tokens: a leaked token cannot mint tokens that outlive it.
func (a *API) sessionOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if viaToken(r) {
			fail(w, errForbidden)
			return
		}
		h(w, r)
	}
}

func (a *API) listMgmtTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := a.S.ListMgmtTokens(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ts)
}

func (a *API) createMgmtToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string     `json:"name"`
		Role          store.Role `json:"role"`
		ExpiresInDays int        `json:"expires_in_days"` // 0 = never
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role == "" {
		in.Role = store.RoleViewer
	}
	if in.Role == store.RoleOwner || in.Role.Rank() == 0 {
		fail(w, fmt.Errorf("%w: role must be admin, member or viewer", store.ErrInvalid))
		return
	}
	if in.Role.Rank() > role(r).Rank() {
		fail(w, errForbidden)
		return
	}
	if in.ExpiresInDays < 0 || in.ExpiresInDays > 3650 {
		fail(w, fmt.Errorf("%w: expires_in_days must be 0..3650", store.ErrInvalid))
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		fail(w, fmt.Errorf("%w: name required (at most 100 characters)", store.ErrInvalid))
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fail(w, err)
		return
	}
	secret := MgmtPrefix + base64.RawURLEncoding.EncodeToString(b)
	t := &store.MgmtToken{OrgID: r.PathValue("org"), Name: name, Prefix: secret[:14], Role: in.Role, CreatedBy: user(r).ID}
	if in.ExpiresInDays > 0 {
		exp := time.Now().UTC().AddDate(0, 0, in.ExpiresInDays)
		t.ExpiresAt = &exp
	}
	if err := a.S.CreateMgmtToken(r.Context(), t, gateway.HashKey(secret)); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, t.OrgID, "mgmt_token.create", t.ID, t.Name+" role="+string(t.Role))
	writeJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
}

func (a *API) patchMgmtToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    *string `json:"name"`
		Revoked *bool   `json:"revoked"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name != nil && (strings.TrimSpace(*in.Name) == "" || len(*in.Name) > 100) {
		fail(w, fmt.Errorf("%w: name required (at most 100 characters)", store.ErrInvalid))
		return
	}
	org, id := r.PathValue("org"), r.PathValue("id")
	t, err := a.S.UpdateMgmtToken(r.Context(), org, id, func(t *store.MgmtToken) {
		if in.Name != nil {
			t.Name = strings.TrimSpace(*in.Name)
		}
		if in.Revoked != nil && *in.Revoked {
			t.Revoked = true // one-way, like API keys
		}
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "mgmt_token.update", id, fmt.Sprintf("name=%s revoked=%v", t.Name, t.Revoked))
	writeJSON(w, 200, t)
}

func (a *API) deleteMgmtToken(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("id")
	if err := a.S.DeleteMgmtToken(r.Context(), org, id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "mgmt_token.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}
