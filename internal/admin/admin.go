// Package admin serves the management API used by the web UI: auth, orgs, members, keys,
// providers, models, usage, logs, audit and platform settings.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/gateway"
	"github.com/agent-gateway/agent-gateway/internal/limits"
	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
	"github.com/maximhq/bifrost/core/mcp"
)

type Config struct {
	MasterKey        string
	SecureCookies    bool
	AllowSignup      bool
	SignupPrepaid    bool
	AllowPrivateURLs bool // let tenants point private providers at internal addresses
	LogRetentionDays int
	OIDC             *OIDCConfig
}

type API struct {
	S    *store.Store
	G    *gateway.Gateway
	Lim  limits.Limiter
	cfg  Config
	sign signer
	oidc *oidcClient
}

func New(ctx context.Context, s *store.Store, g *gateway.Gateway, lim limits.Limiter, cfg Config) (*API, error) {
	a := &API{S: s, G: g, Lim: lim, cfg: cfg, sign: newSigner(cfg.MasterKey)}
	if cfg.OIDC != nil {
		c, err := newOIDC(ctx, *cfg.OIDC)
		if err != nil {
			return nil, err
		}
		a.oidc = c
	}
	return a, nil
}

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

type userCtx struct{}
type roleCtx struct{}

func user(r *http.Request) *store.User { return r.Context().Value(userCtx{}).(*store.User) }
func role(r *http.Request) store.Role  { return r.Context().Value(roleCtx{}).(store.Role) }

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", csrf(a.login))
	mux.HandleFunc("POST /api/auth/logout", csrf(a.logout))
	mux.HandleFunc("POST /api/auth/signup", csrf(a.signup))
	mux.HandleFunc("GET /api/auth/config", a.authConfig)
	mux.HandleFunc("GET /api/auth/oidc/login", a.oidcLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", a.oidcCallback)
	mux.HandleFunc("GET /api/auth/me", a.authed(a.me))
	mux.HandleFunc("GET /api/auth/session", a.session)
	mux.HandleFunc("POST /api/auth/password", a.authed(a.changePassword))
	mux.HandleFunc("GET /api/public/models", a.publicModels)
	mux.HandleFunc("GET /api/public/models/{id...}", a.publicModel)
	mux.HandleFunc("GET /api/public/status", a.publicStatus)

	mux.HandleFunc("GET /api/orgs", a.authed(a.listOrgs))
	mux.HandleFunc("POST /api/orgs", a.operator(a.createOrg))
	mux.HandleFunc("GET /api/orgs/{org}", a.org(store.RoleViewer, a.getOrg))
	mux.HandleFunc("PATCH /api/orgs/{org}", a.org(store.RoleAdmin, a.patchOrg))
	mux.HandleFunc("DELETE /api/orgs/{org}", a.org(store.RoleOwner, a.deleteOrg))
	mux.HandleFunc("POST /api/orgs/{org}/credits", a.operator(a.addCredits))
	mux.HandleFunc("GET /api/orgs/{org}/members", a.org(store.RoleViewer, a.listMembers))
	mux.HandleFunc("POST /api/orgs/{org}/members", a.org(store.RoleAdmin, a.addMember))
	mux.HandleFunc("PUT /api/orgs/{org}/members/{user}", a.org(store.RoleAdmin, a.setMemberRole))
	mux.HandleFunc("DELETE /api/orgs/{org}/members/{user}", a.org(store.RoleAdmin, a.removeMember))
	mux.HandleFunc("GET /api/orgs/{org}/keys", a.org(store.RoleViewer, a.listKeys))
	mux.HandleFunc("POST /api/orgs/{org}/keys", a.org(store.RoleMember, a.createKey))
	mux.HandleFunc("PATCH /api/orgs/{org}/keys/{id}", a.org(store.RoleMember, a.patchKey))
	mux.HandleFunc("GET /api/orgs/{org}/providers", a.org(store.RoleAdmin, a.listProviders))
	mux.HandleFunc("POST /api/orgs/{org}/providers", a.org(store.RoleAdmin, a.putProvider))
	mux.HandleFunc("PUT /api/orgs/{org}/providers/{id}", a.org(store.RoleAdmin, a.putProvider))
	mux.HandleFunc("DELETE /api/orgs/{org}/providers/{id}", a.org(store.RoleAdmin, a.deleteProvider))
	mux.HandleFunc("POST /api/orgs/{org}/providers/{id}/discover", a.org(store.RoleAdmin, a.discoverModels))
	mux.HandleFunc("POST /api/orgs/{org}/providers/{id}/import", a.org(store.RoleAdmin, a.importModels))
	mux.HandleFunc("GET /api/orgs/{org}/models", a.org(store.RoleViewer, a.listModels))
	mux.HandleFunc("POST /api/orgs/{org}/models", a.org(store.RoleAdmin, a.putModel))
	mux.HandleFunc("PUT /api/orgs/{org}/models/{id...}", a.org(store.RoleAdmin, a.putModel))
	mux.HandleFunc("DELETE /api/orgs/{org}/models/{id...}", a.org(store.RoleAdmin, a.deleteModel))
	mux.HandleFunc("GET /api/orgs/{org}/usage", a.org(store.RoleViewer, a.usageReport))
	mux.HandleFunc("GET /api/orgs/{org}/logs", a.org(store.RoleViewer, a.listLogs))
	mux.HandleFunc("GET /api/orgs/{org}/audit", a.org(store.RoleAdmin, a.listAudit))

	// platform (operator) scope; org "_" means public resources
	mux.HandleFunc("GET /api/admin/providers", a.operator(a.platform(a.listProviders)))
	mux.HandleFunc("POST /api/admin/providers", a.operator(a.platform(a.putProvider)))
	mux.HandleFunc("PUT /api/admin/providers/{id}", a.operator(a.platform(a.putProvider)))
	mux.HandleFunc("DELETE /api/admin/providers/{id}", a.operator(a.platform(a.deleteProvider)))
	mux.HandleFunc("POST /api/admin/providers/{id}/discover", a.operator(a.platform(a.discoverModels)))
	mux.HandleFunc("POST /api/admin/providers/{id}/import", a.operator(a.platform(a.importModels)))
	mux.HandleFunc("GET /api/admin/models", a.operator(a.platform(a.listModels)))
	mux.HandleFunc("POST /api/admin/models", a.operator(a.platform(a.putModel)))
	mux.HandleFunc("PUT /api/admin/models/{id...}", a.operator(a.platform(a.putModel)))
	mux.HandleFunc("DELETE /api/admin/models/{id...}", a.operator(a.platform(a.deleteModel)))
	mux.HandleFunc("GET /api/admin/usage", a.operator(a.platform(a.usageReport)))
	mux.HandleFunc("GET /api/admin/audit", a.operator(a.platform(a.listAudit)))
	mux.HandleFunc("GET /api/admin/users", a.operator(a.listUsers))
	mux.HandleFunc("PATCH /api/admin/users/{id}", a.operator(a.patchUser))
	mux.HandleFunc("GET /api/admin/mcp", a.operator(a.listMCP))
	mux.HandleFunc("POST /api/admin/mcp", a.operator(a.putMCP))
	mux.HandleFunc("PUT /api/admin/mcp/{id}", a.operator(a.putMCP))
	mux.HandleFunc("DELETE /api/admin/mcp/{id}", a.operator(a.deleteMCP))
}

// ---- middleware ----

// csrf requires the X-GW-CSRF header on non-GET requests; a cross-site form cannot set it.
func csrf(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-GW-CSRF") != "1" {
			writeErr(w, http.StatusForbidden, "missing X-GW-CSRF header")
			return
		}
		h(w, r)
	}
}

// authed requires a session. Mutations also require the X-GW-CSRF header, which a cross-site
// form cannot set.
func (a *API) authed(h http.HandlerFunc) http.HandlerFunc {
	return csrf(func(w http.ResponseWriter, r *http.Request) {
		u, err := a.currentUser(r)
		if err != nil {
			fail(w, err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userCtx{}, u)))
	})
}

func (a *API) operator(h http.HandlerFunc) http.HandlerFunc {
	return a.authed(func(w http.ResponseWriter, r *http.Request) {
		if !user(r).Operator {
			fail(w, errForbidden)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), roleCtx{}, store.RoleOwner)))
	})
}

// platform marks an operator route as acting on public resources (org "_").
func (a *API) platform(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("org", "_")
		h(w, r)
	}
}

// org requires membership of {org} with at least min role. Operators act as owners everywhere.
// Non-members get 404 so org ids cannot be probed.
func (a *API) org(min store.Role, h http.HandlerFunc) http.HandlerFunc {
	return a.authed(func(w http.ResponseWriter, r *http.Request) {
		u, orgID := user(r), r.PathValue("org")
		var rl store.Role
		if u.Operator {
			if _, err := a.S.GetOrg(r.Context(), orgID); err != nil {
				fail(w, err)
				return
			}
			rl = store.RoleOwner
		} else {
			m, err := a.S.GetMember(r.Context(), orgID, u.ID)
			if err != nil {
				fail(w, err)
				return
			}
			rl = m.Role
		}
		if rl.Rank() < min.Rank() {
			fail(w, errForbidden)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), roleCtx{}, rl)))
	})
}

// ---- helpers ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": msg}})
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnauthorized):
		writeErr(w, http.StatusUnauthorized, "sign in required")
	case errors.Is(err, errForbidden):
		writeErr(w, http.StatusForbidden, "you do not have permission for this action")
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrExists):
		writeErr(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		slog.Error("admin api", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// orInvalid returns err if set, else an ErrInvalid with msg.
func orInvalid(err error, msg string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", store.ErrInvalid, msg)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func (a *API) audit(r *http.Request, org, action, target, detail string) {
	u := user(r)
	if err := a.S.Audit(r.Context(), store.AuditEntry{OrgID: org, ActorID: u.ID, Actor: u.Email,
		Action: action, Target: target, Detail: detail}); err != nil {
		slog.Error("audit write failed", "err", err, "action", action)
	}
}

// owner returns the store owner id for {org}: "" for platform resources.
func owner(r *http.Request) string {
	if o := r.PathValue("org"); o != "_" {
		return o
	}
	return ""
}

func usdToMicros(usd float64) int64 { return int64(math.Round(usd * 1e6)) }

// ---- auth info ----

func (a *API) authConfig(w http.ResponseWriter, r *http.Request) {
	var platform, org []string
	for t := range supportedTypes {
		platform = append(platform, t)
		if gateway.MultiInstance(t) {
			org = append(org, t)
		}
	}
	sort.Strings(platform)
	sort.Strings(org)
	writeJSON(w, 200, map[string]any{"signup": a.cfg.AllowSignup, "sso": a.oidc != nil,
		"provider_types": map[string][]string{"platform": platform, "org": org}})
}

// session is /me without the 401: signed-out visitors get {"user": null}, so public pages can
// ask "who is this?" without an error in the browser console.
func (a *API) session(w http.ResponseWriter, r *http.Request) {
	u, err := a.currentUser(r)
	if err != nil {
		if errors.Is(err, errUnauthorized) {
			writeJSON(w, 200, map[string]any{"user": nil, "orgs": []any{}})
			return
		}
		fail(w, err)
		return
	}
	a.me(w, r.WithContext(context.WithValue(r.Context(), userCtx{}, u)))
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	roles, err := a.S.UserOrgs(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	type orgRef struct {
		ID   string     `json:"id"`
		Name string     `json:"name"`
		Role store.Role `json:"role"`
	}
	orgs := []orgRef{}
	for id, rl := range roles {
		if o, err := a.S.GetOrg(r.Context(), id); err == nil {
			orgs = append(orgs, orgRef{id, o.Name, rl})
		}
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].Name < orgs[j].Name })
	writeJSON(w, 200, map[string]any{"user": u, "orgs": orgs})
}

// ---- orgs ----

type orgView struct {
	store.Org
	SpentMicros   int64 `json:"spent_micros"`
	BalanceMicros int64 `json:"balance_micros"`
}

func (a *API) view(ctx context.Context, o *store.Org) (orgView, error) {
	spent, err := a.G.Usage.Spent(ctx, usage.OrgSpendKey(o.ID))
	return orgView{*o, spent, o.CreditsMicros - spent}, err
}

func (a *API) listOrgs(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	if u.Operator {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 500 {
			limit = 100
		}
		orgs, err := a.S.ListOrgs(r.Context(), r.URL.Query().Get("after"), limit)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, 200, orgs)
		return
	}
	a.me(w, r)
}

func (a *API) createOrg(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string `json:"name"`
		Prepaid    bool   `json:"prepaid"`
		OwnerEmail string `json:"owner_email"`
	}
	if !decode(w, r, &in) {
		return
	}
	ownerID := ""
	if in.OwnerEmail != "" {
		u, err := a.S.GetUserByEmail(r.Context(), in.OwnerEmail)
		if err != nil {
			fail(w, fmt.Errorf("%w: owner %s is not a user", store.ErrInvalid, in.OwnerEmail))
			return
		}
		ownerID = u.ID
	}
	o := &store.Org{Name: in.Name, Prepaid: in.Prepaid}
	if err := a.S.CreateOrg(r.Context(), o, ownerID); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, o.ID, "org.create", o.ID, o.Name)
	writeJSON(w, http.StatusCreated, o)
}

func (a *API) getOrg(w http.ResponseWriter, r *http.Request) {
	o, err := a.S.GetOrg(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	v, err := a.view(r.Context(), o)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *API) patchOrg(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         *string           `json:"name"`
		Guardrails   *store.Guardrails `json:"guardrails"`
		MaxTokensCap *int              `json:"max_tokens_cap"`
		Prepaid      *bool             `json:"prepaid"`
		MCPServers   *[]string         `json:"mcp_servers"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Prepaid != nil || in.MCPServers != nil) && !user(r).Operator {
		fail(w, errForbidden)
		return
	}
	if in.Guardrails != nil {
		for _, p := range in.Guardrails.BlockedPatterns {
			if _, err := regexp.Compile(p); err != nil {
				fail(w, fmt.Errorf("%w: blocked pattern %q: %v", store.ErrInvalid, p, err))
				return
			}
		}
	}
	o, err := a.S.UpdateOrg(r.Context(), r.PathValue("org"), func(o *store.Org) error {
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			o.Name = *in.Name
		}
		if in.Guardrails != nil {
			o.Guardrails = *in.Guardrails
		}
		if in.MaxTokensCap != nil {
			o.MaxTokensCap = max(0, *in.MaxTokensCap)
		}
		if in.Prepaid != nil {
			o.Prepaid = *in.Prepaid
		}
		if in.MCPServers != nil {
			o.MCPServers = *in.MCPServers
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.G.InvalidateAuth() // cached principals carry the org's guardrails and billing mode
	a.audit(r, o.ID, "org.update", o.ID, "")
	writeJSON(w, 200, o)
}

func (a *API) deleteOrg(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("org")
	if err := a.S.DeleteOrg(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	a.G.InvalidateAuth()
	a.audit(r, "_", "org.delete", id, "") // the org's own audit log is gone with it
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) addCredits(w http.ResponseWriter, r *http.Request) {
	var in struct {
		USD  float64 `json:"usd"`
		Note string  `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.USD == 0 || math.IsNaN(in.USD) || math.Abs(in.USD) > 1e9 {
		fail(w, fmt.Errorf("%w: usd must be a non-zero amount", store.ErrInvalid))
		return
	}
	o, err := a.S.UpdateOrg(r.Context(), r.PathValue("org"), func(o *store.Org) error {
		o.CreditsMicros += usdToMicros(in.USD)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.G.InvalidateAuth()
	a.audit(r, o.ID, "org.credits", o.ID, fmt.Sprintf("%+.2f USD %s", in.USD, in.Note))
	v, _ := a.view(r.Context(), o)
	writeJSON(w, 200, v)
}

// ---- members ----

type memberView struct {
	store.Member
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (a *API) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := a.S.ListMembers(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]memberView, 0, len(ms))
	for _, m := range ms {
		v := memberView{Member: m}
		if u, err := a.S.GetUser(r.Context(), m.UserID); err == nil {
			v.Email, v.Name = u.Email, u.Name
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

// canAssign: admins manage non-owner roles; only owners grant or change ownership.
func canAssign(actor, target store.Role) bool {
	return target.Rank() > 0 && (actor == store.RoleOwner || target.Rank() < store.RoleOwner.Rank())
}

func (a *API) addMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string     `json:"email"`
		Role     store.Role `json:"role"`
		Password string     `json:"password"`
		Name     string     `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !canAssign(role(r), in.Role) {
		fail(w, errForbidden)
		return
	}
	ctx := r.Context()
	u, err := a.S.GetUserByEmail(ctx, in.Email)
	if errors.Is(err, store.ErrNotFound) {
		u = &store.User{Email: in.Email, Name: in.Name}
		if in.Password != "" {
			if u.PasswordHash, err = HashPassword(in.Password); err != nil {
				fail(w, err)
				return
			}
		}
		err = a.S.CreateUser(ctx, u) // without a password the user signs in with SSO
	}
	if err != nil {
		fail(w, err)
		return
	}
	org := r.PathValue("org")
	if old, err := a.S.GetMember(ctx, org, u.ID); err == nil && !canAssign(role(r), old.Role) {
		fail(w, errForbidden)
		return
	}
	if err := a.S.SetMember(ctx, store.Member{OrgID: org, UserID: u.ID, Role: in.Role}); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "member.add", u.Email, string(in.Role))
	writeJSON(w, http.StatusCreated, memberView{store.Member{OrgID: org, UserID: u.ID, Role: in.Role}, u.Email, u.Name})
}

// lastOwner reports whether the org has exactly one owner left.
func (a *API) lastOwner(ctx context.Context, org string) (bool, error) {
	ms, err := a.S.ListMembers(ctx, org)
	n := 0
	for _, m := range ms {
		if m.Role == store.RoleOwner {
			n++
		}
	}
	return n == 1, err
}

func (a *API) setMemberRole(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role store.Role `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	org, uid := r.PathValue("org"), r.PathValue("user")
	old, err := a.S.GetMember(r.Context(), org, uid)
	if err != nil {
		fail(w, err)
		return
	}
	if !canAssign(role(r), in.Role) || !canAssign(role(r), old.Role) {
		fail(w, errForbidden)
		return
	}
	if old.Role == store.RoleOwner && in.Role != store.RoleOwner {
		if last, err := a.lastOwner(r.Context(), org); err != nil || last {
			fail(w, orInvalid(err, "an organization needs at least one owner"))
			return
		}
	}
	if err := a.S.SetMember(r.Context(), store.Member{OrgID: org, UserID: uid, Role: in.Role}); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "member.role", uid, string(old.Role)+" -> "+string(in.Role))
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request) {
	org, uid := r.PathValue("org"), r.PathValue("user")
	old, err := a.S.GetMember(r.Context(), org, uid)
	if err != nil {
		fail(w, err)
		return
	}
	if !canAssign(role(r), old.Role) {
		fail(w, errForbidden)
		return
	}
	if old.Role == store.RoleOwner {
		if last, err := a.lastOwner(r.Context(), org); err != nil || last {
			fail(w, orInvalid(err, "an organization needs at least one owner"))
			return
		}
	}
	if err := a.S.RemoveMember(r.Context(), org, uid); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "member.remove", uid, "")
	w.WriteHeader(http.StatusNoContent)
}

// ---- API keys ----

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.S.ListKeys(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, keys)
}

type keyInput struct {
	Name             *string   `json:"name"`
	RPM              *int      `json:"rpm"`
	TPM              *int      `json:"tpm"`
	MonthlyBudgetUSD *float64  `json:"monthly_budget_usd"`
	Models           *[]string `json:"models"`
	MCPServers       *[]string `json:"mcp_servers"`
	ExpiresInDays    *int      `json:"expires_in_days"`
	Revoked          *bool     `json:"revoked"`
}

func (in keyInput) apply(k *store.APIKey) {
	if in.Name != nil {
		k.Name = *in.Name
	}
	if in.RPM != nil {
		k.RPM = max(0, *in.RPM)
	}
	if in.TPM != nil {
		k.TPM = max(0, *in.TPM)
	}
	if in.MonthlyBudgetUSD != nil {
		k.MonthlyBudget = max(0, usdToMicros(*in.MonthlyBudgetUSD))
	}
	if in.Models != nil {
		k.Models = *in.Models
	}
	if in.MCPServers != nil {
		k.MCPServers = *in.MCPServers
	}
	if in.ExpiresInDays != nil && *in.ExpiresInDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, *in.ExpiresInDays)
		k.ExpiresAt = &t
	}
	if in.Revoked != nil && *in.Revoked {
		k.Revoked = true // revocation is one-way
	}
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var in keyInput
	if !decode(w, r, &in) {
		return
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		fail(w, err)
		return
	}
	secret := gateway.KeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	k := &store.APIKey{OrgID: r.PathValue("org"), CreatedBy: user(r).ID, Prefix: secret[:12]}
	in.Revoked = nil
	in.apply(k)
	if strings.TrimSpace(k.Name) == "" {
		k.Name = "key " + time.Now().UTC().Format("2006-01-02")
	}
	if err := a.S.CreateKey(r.Context(), k, gateway.HashKey(secret)); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, k.OrgID, "key.create", k.ID, k.Name)
	writeJSON(w, http.StatusCreated, map[string]any{"key": k, "secret": secret})
}

func (a *API) patchKey(w http.ResponseWriter, r *http.Request) {
	var in keyInput
	if !decode(w, r, &in) {
		return
	}
	org, id := r.PathValue("org"), r.PathValue("id")
	cur, err := a.S.GetKey(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	if role(r).Rank() < store.RoleAdmin.Rank() {
		// members may rename or revoke their own keys, not lift limits an admin set
		if cur.CreatedBy != user(r).ID || in.RPM != nil || in.TPM != nil || in.MonthlyBudgetUSD != nil || in.Models != nil || in.MCPServers != nil || in.ExpiresInDays != nil {
			fail(w, errForbidden)
			return
		}
	}
	k, err := a.S.UpdateKey(r.Context(), org, id, func(k *store.APIKey) error { in.apply(k); return nil })
	if err != nil {
		fail(w, err)
		return
	}
	a.G.InvalidateAuth() // revocation and new limits apply on this node immediately
	a.audit(r, org, "key.update", id, fmt.Sprintf("revoked=%v rpm=%d tpm=%d", k.Revoked, k.RPM, k.TPM))
	writeJSON(w, 200, k)
}

// ---- providers ----

// supportedTypes are the Bifrost provider types the platform configures. azure, bedrock and vertex
// take per-key config (ProviderKey.Azure/Bedrock/Vertex) instead of, or next to, an API key;
// ollama, sgl and vllm take their server URL, databricks its workspace URL, from base_url.
// Not offered (need auth or key config this schema lacks): github-copilot (OAuth device flow),
// bedrock_mantle and sagemaker (AWS SigV4 key config), cloudflare (per-key account id).
var supportedTypes = map[string]bool{"openai": true, "anthropic": true, "gemini": true, "cohere": true,
	"mistral": true, "groq": true, "ollama": true, "cerebras": true, "perplexity": true, "openrouter": true,
	"nebius": true, "parasail": true, "sgl": true, "elevenlabs": true, "azure": true, "bedrock": true, "vertex": true,
	"deepseek": true, "xai": true, "fireworks": true, "huggingface": true, "replicate": true, "vllm": true,
	"databricks": true, "minimax": true, "byteplus": true, "sarvam": true, "wafer": true, "typesafe": true,
	"opencode-go": true, "opencode-zen": true, "runway": true, "runware": true}

// needsBaseURL are the types with no default host: base_url is their server or workspace URL.
var needsBaseURL = map[string]bool{"ollama": true, "sgl": true, "vllm": true, "databricks": true}

// cloudID matches AWS/GCP regions and GCP project ids; both are formatted into upstream hostnames
// and paths by Bifrost, so anything else could redirect requests.
var cloudID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// checkKeyConfig validates the per-key config the provider type needs. tenant is true for org
// providers: they may not use the gateway host's own cloud identity (IAM), only explicit secrets.
// old is the stored provider on update (nil on create), used to see which secrets are kept.
func checkKeyConfig(p, old *store.Provider, tenant bool, allowPrivate bool) error {
	if needsBaseURL[p.Type] && p.BaseURL == "" {
		return fmt.Errorf("%w: %s needs base_url (its server or workspace URL)", store.ErrInvalid, p.Type)
	}
	cloud := p.Type == "azure" || p.Type == "bedrock" || p.Type == "vertex"
	if cloud && p.BaseURL != "" {
		return fmt.Errorf("%w: base_url is not used by %s; set the endpoint or region on each key", store.ErrInvalid, p.Type)
	}
	if cloud && len(p.Keys) == 0 {
		return fmt.Errorf("%w: %s needs at least one key", store.ErrInvalid, p.Type)
	}
	for _, k := range p.Keys {
		if (k.Azure != nil && p.Type != "azure") || (k.Bedrock != nil && p.Type != "bedrock") || (k.Vertex != nil && p.Type != "vertex") {
			return fmt.Errorf("%w: key config does not match provider type %s", store.ErrInvalid, p.Type)
		}
		switch p.Type {
		case "azure":
			if k.Azure == nil || k.Azure.Endpoint == "" {
				return fmt.Errorf("%w: azure keys need an endpoint", store.ErrInvalid)
			}
			if err := checkBaseURL(k.Azure.Endpoint, allowPrivate); err != nil {
				return fmt.Errorf("%w: azure endpoint must be a public http(s) URL", store.ErrInvalid)
			}
		case "bedrock":
			b := k.Bedrock
			if b == nil || !cloudID.MatchString(b.Region) {
				return fmt.Errorf("%w: bedrock keys need a region such as us-east-1", store.ErrInvalid)
			}
			if (b.AccessKey == "") != (b.SecretKey == "") {
				return fmt.Errorf("%w: bedrock access_key and secret_key go together", store.ErrInvalid)
			}
			if tenant && k.Value == "" && b.AccessKey == "" && !keepsCredential(old, k.ID) {
				// empty credentials mean the gateway host's AWS identity: operators only
				return fmt.Errorf("%w: bedrock keys need an API key or access_key and secret_key", store.ErrInvalid)
			}
		case "vertex":
			v := k.Vertex
			if v == nil || !cloudID.MatchString(v.ProjectID) || !cloudID.MatchString(v.Region) {
				return fmt.Errorf("%w: vertex keys need project_id and region", store.ErrInvalid)
			}
			if v.AuthCredentials != "" && !json.Valid([]byte(v.AuthCredentials)) {
				return fmt.Errorf("%w: vertex auth_credentials must be service-account JSON", store.ErrInvalid)
			}
		}
	}
	return nil
}

// checkBaseURL blocks tenants from pointing providers at internal networks (SSRF). The gateway
// re-checks at request time as well.
func checkBaseURL(raw string, allowPrivate bool) error {
	if raw == "" {
		return nil
	}
	if allowPrivate {
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: base_url must be an http(s) URL", store.ErrInvalid)
		}
		return nil
	}
	if err := gateway.CheckPublicURL(raw); err != nil {
		return fmt.Errorf("%w: base_url must be a public http(s) URL", store.ErrInvalid)
	}
	return nil
}

// keepsCredential reports whether the stored key id holds an API key or AWS access key that an
// update with empty inputs would keep.
func keepsCredential(old *store.Provider, id string) bool {
	if old == nil || id == "" {
		return false
	}
	for _, k := range old.Keys {
		if k.ID == id && (k.Sealed != "" || (k.Bedrock != nil && k.Bedrock.SealedAccessKey != "")) {
			return true
		}
	}
	return false
}

func redact(p store.Provider) store.Provider {
	keys := make([]store.ProviderKey, len(p.Keys))
	for i, k := range p.Keys {
		keys[i] = k.Redacted()
	}
	p.Keys = keys
	return p
}

func (a *API) listProviders(w http.ResponseWriter, r *http.Request) {
	ps, err := a.S.ListProviders(r.Context(), false)
	if err != nil {
		fail(w, err)
		return
	}
	out := []store.Provider{}
	for _, p := range ps {
		if p.OrgID == owner(r) {
			out = append(out, redact(p))
		}
	}
	writeJSON(w, 200, out)
}

func (a *API) putProvider(w http.ResponseWriter, r *http.Request) {
	var p store.Provider
	if !decode(w, r, &p) {
		return
	}
	p.OrgID, p.ID = owner(r), r.PathValue("id")
	if !supportedTypes[p.Type] || (p.OrgID != "" && !gateway.MultiInstance(p.Type)) {
		// tenants get the multi-instance types only, so they never take a platform provider's slot
		fail(w, fmt.Errorf("%w: unsupported provider type %q here (OpenAI-compatible APIs use type openai + base_url)", store.ErrInvalid, p.Type))
		return
	}
	if err := checkBaseURL(p.BaseURL, p.OrgID == "" || a.cfg.AllowPrivateURLs); err != nil {
		fail(w, err)
		return
	}
	var old *store.Provider
	if p.ID != "" {
		if o, err := a.S.GetProvider(r.Context(), p.ID); err == nil && o.OrgID == p.OrgID {
			old = o
		}
	}
	if err := checkKeyConfig(&p, old, p.OrgID != "", p.OrgID == "" || a.cfg.AllowPrivateURLs); err != nil {
		fail(w, err)
		return
	}
	if err := a.S.PutProvider(r.Context(), &p); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "provider.put", p.ID, p.Name+" "+p.Type+" "+p.BaseURL)
	writeJSON(w, 200, redact(p))
}

func (a *API) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := a.S.GetProvider(r.Context(), id)
	if err != nil || p.OrgID != owner(r) {
		fail(w, store.ErrNotFound)
		return
	}
	if err := a.S.DeleteProvider(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "provider.delete", id, p.Name)
	w.WriteHeader(http.StatusNoContent)
}

// ---- models ----

type modelView struct {
	gateway.ModelInfo
	Targets []store.Target `json:"targets,omitempty"`
	Enabled bool           `json:"enabled"`
	Owner   string         `json:"owner"` // "platform" or "org"
}

func (a *API) listModels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var ms []store.Model
	var err error
	if o := owner(r); o == "" {
		ms, err = a.S.ListModels(ctx, "")
	} else {
		ms, err = a.S.ListModels(ctx, o)
		if err == nil {
			var pub []store.Model
			pub, err = a.S.ListModels(ctx, "")
			for _, m := range pub {
				if m.Enabled {
					ms = append(ms, m)
				}
			}
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]modelView, 0, len(ms))
	for _, m := range ms {
		v := modelView{ModelInfo: gateway.Info(m), Enabled: m.Enabled, Owner: "platform"}
		if m.OrgID != "" {
			v.Owner = "org"
		}
		if m.OrgID == owner(r) { // targets only for models the caller manages
			v.Targets = m.Targets
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (a *API) putModel(w http.ResponseWriter, r *http.Request) {
	var m store.Model
	if !decode(w, r, &m) {
		return
	}
	if id := r.PathValue("id"); id != "" {
		m.ID = id
	}
	m.OrgID = owner(r)
	if !reModelID.MatchString(m.ID) || strings.Contains(m.ID, "..") {
		fail(w, fmt.Errorf("%w: model id must look like vendor/name (letters, digits, . _ : - /)", store.ErrInvalid))
		return
	}
	if err := a.S.PutModel(r.Context(), &m); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "model.put", m.ID, fmt.Sprintf("in=%d out=%d micros/1M", m.InputPrice, m.OutputPrice))
	writeJSON(w, 200, m)
}

var reModelID = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._:/-]+$`)

func (a *API) deleteModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := a.S.GetModel(r.Context(), owner(r), id); err != nil {
		fail(w, err)
		return
	}
	if err := a.S.DeleteModel(r.Context(), owner(r), id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "model.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) publicModels(w http.ResponseWriter, r *http.Request) {
	out := []gateway.ModelInfo{}
	snap := a.G.Snapshot()
	for _, m := range snap.Visible("") {
		if m.OrgID == "" {
			out = append(out, snap.Info(m))
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, 200, map[string]any{"data": out})
}

// ---- usage, logs, audit ----

type usageRow struct {
	Key          string  `json:"key"`
	Requests     int64   `json:"requests"`
	Errors       int64   `json:"errors"`
	InTokens     int64   `json:"in_tokens"`
	OutTokens    int64   `json:"out_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	CacheHits    int64   `json:"cache_hits"`
}

func row(key string, a store.Agg) usageRow {
	u := usageRow{Key: key, Requests: a[store.AggRequests], Errors: a[store.AggErrors], InTokens: a[store.AggInTokens],
		OutTokens: a[store.AggOutTokens], CostUSD: float64(a[store.AggCostMicros]) / 1e6, CacheHits: a[store.AggCacheHits]}
	if u.Requests > 0 {
		u.AvgLatencyMs = a[store.AggLatencyMs] / u.Requests
	}
	return u
}

func rows(m map[string]store.Agg) []usageRow {
	out := make([]usageRow, 0, len(m))
	for k, a := range m {
		out = append(out, row(k, a))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CostUSD > out[j].CostUSD || (out[i].CostUSD == out[j].CostUSD && out[i].Key < out[j].Key)
	})
	return out
}

func (a *API) usageReport(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 366 {
		days = 30
	}
	org := r.PathValue("org")
	daily := []usageRow{}
	byModel, byKey := map[string]store.Agg{}, map[string]store.Agg{}
	var total store.Agg
	now := time.Now()
	for d := days - 1; d >= 0; d-- {
		day := store.Day(now.AddDate(0, 0, -d))
		prefix := "agg/" + org + "/" + day + "/"
		m, err := a.S.KV.ScanAgg(r.Context(), prefix)
		if err != nil {
			fail(w, err)
			return
		}
		var dayTotal store.Agg
		for k, v := range m {
			rest := strings.TrimPrefix(k, prefix)
			switch {
			case rest == "t":
				dayTotal = v
			case strings.HasPrefix(rest, "m/"):
				cur := byModel[rest[2:]]
				cur.Add(v)
				byModel[rest[2:]] = cur
				if org == "_" { // platform rollups only have per-model rows
					dayTotal.Add(v)
				}
			case strings.HasPrefix(rest, "k/"):
				cur := byKey[rest[2:]]
				cur.Add(v)
				byKey[rest[2:]] = cur
			}
		}
		total.Add(dayTotal)
		daily = append(daily, row(day, dayTotal))
	}
	writeJSON(w, 200, map[string]any{"total": row("total", total), "daily": daily, "models": rows(byModel), "keys": rows(byKey)})
}

func (a *API) listLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	logs, err := a.S.ListLogs(r.Context(), r.PathValue("org"), a.cfg.LogRetentionDays, limit)
	if err != nil {
		fail(w, err)
		return
	}
	if logs == nil {
		logs = []store.RequestLog{}
	}
	writeJSON(w, 200, logs)
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := a.S.ListAudit(r.Context(), r.PathValue("org"), 200)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, entries)
}

// ---- platform: users, MCP ----

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := a.S.ListUsers(r.Context(), r.URL.Query().Get("after"), 200)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, us)
}

func (a *API) patchUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Operator *bool `json:"operator"`
		Disabled *bool `json:"disabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if id == user(r).ID {
		fail(w, fmt.Errorf("%w: you cannot change your own operator or disabled flag", store.ErrInvalid))
		return
	}
	u, err := a.S.UpdateUser(r.Context(), id, func(u *store.User) error {
		if in.Operator != nil {
			u.Operator = *in.Operator
		}
		if in.Disabled != nil {
			u.Disabled = *in.Disabled
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, "_", "user.update", u.Email, fmt.Sprintf("operator=%v disabled=%v", u.Operator, u.Disabled))
	writeJSON(w, 200, u)
}

func (a *API) listMCP(w http.ResponseWriter, r *http.Request) {
	servers, err := a.S.ListMCP(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	clients, err := a.G.MCPTools(r.Context())
	if err != nil {
		slog.Warn("mcp status", "err", err)
	}
	writeJSON(w, 200, map[string]any{"servers": servers, "connected": clients})
}

func (a *API) putMCP(w http.ResponseWriter, r *http.Request) {
	var m store.MCPServer
	if !decode(w, r, &m) {
		return
	}
	m.ID = r.PathValue("id")
	if err := checkBaseURL(m.URL, true); err != nil { // operators may use internal MCP servers
		fail(w, err)
		return
	}
	if err := mcp.ValidateMCPClientName(m.Name); err != nil { // Bifrost refuses other names at connect time
		fail(w, fmt.Errorf("%w: mcp server %v", store.ErrInvalid, err))
		return
	}
	if err := a.S.PutMCP(r.Context(), &m); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, "_", "mcp.put", m.ID, m.Name+" "+m.URL)
	writeJSON(w, 200, m)
}

func (a *API) deleteMCP(w http.ResponseWriter, r *http.Request) {
	if err := a.S.DeleteMCP(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, "_", "mcp.delete", r.PathValue("id"), "")
	w.WriteHeader(http.StatusNoContent)
}
