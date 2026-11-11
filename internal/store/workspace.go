package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Workspaces (D31, docs/API.md "Workspaces") partition an org: API keys, BYOK providers, private
// models, policies, presets, trace exports, knowledge bases and management tokens carry a
// WorkspaceID. Every org has exactly one default workspace, whose id is DefaultWorkspace; data
// stored before workspaces has an empty WorkspaceID, which means the default workspace. Writes
// store the default workspace as "" (WSID), so comparisons never need the org's default id.

// DefaultWorkspace is the id of every org's default workspace.
const DefaultWorkspace = "default"

// MaxWorkspacesPerOrg bounds the workspaces of one org.
const MaxWorkspacesPerOrg = 100

type Workspace struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
}

// WorkspaceMember gives a user a role (admin, member or viewer) in a non-default workspace. Org
// owners and admins are admins of every workspace; every org member belongs to the default
// workspace with their org role. IdP marks memberships set by SCIM/SAML group mappings, which
// a group change may remove again.
type WorkspaceMember struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Role        Role   `json:"role"`
	IdP         bool   `json:"idp,omitempty"`
}

// WSID is the stored form of a workspace id: "" for the default workspace.
func WSID(id string) string {
	if id == DefaultWorkspace {
		return ""
	}
	return id
}

// WSRef is the API form of a stored workspace id: DefaultWorkspace for "".
func WSRef(id string) string {
	if id == "" {
		return DefaultWorkspace
	}
	return id
}

func wsKey(org, id string) string          { return "ws/" + org + "/" + id }
func wsMemKey(org, ws, user string) string { return "wsmem/" + org + "/" + ws + "/" + user }

func defaultWorkspace(org string, at time.Time) *Workspace {
	return &Workspace{ID: DefaultWorkspace, OrgID: org, Name: "Default", Slug: DefaultWorkspace, IsDefault: true, CreatedAt: at}
}

// ValidWSRole reports whether r is a workspace role.
func ValidWSRole(r Role) bool { return r == RoleAdmin || r == RoleMember || r == RoleViewer }

// GetWorkspace returns a workspace of org; "" and DefaultWorkspace return the default one,
// created on first read for orgs made before workspaces.
func (s *Store) GetWorkspace(ctx context.Context, org, id string) (*Workspace, error) {
	if strings.Contains(org, "/") || strings.Contains(id, "/") {
		return nil, ErrNotFound
	}
	w, err := getJSON[Workspace](s.get(ctx), wsKey(org, WSRef(id)))
	if WSRef(id) != DefaultWorkspace || !errors.Is(err, ErrNotFound) {
		return w, err
	}
	o, err := s.GetOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	w = defaultWorkspace(org, o.CreatedAt)
	return w, s.KV.Update(ctx, func(tx Tx) error { return putJSON(tx, wsKey(org, w.ID), w) })
}

// ListWorkspaces returns org's workspaces, the default one first.
func (s *Store) ListWorkspaces(ctx context.Context, org string) ([]Workspace, error) {
	def, err := s.GetWorkspace(ctx, org, DefaultWorkspace)
	if err != nil {
		return nil, err
	}
	ws, err := listJSON[Workspace](ctx, s.KV, "ws/"+org+"/", "", 0)
	if err != nil {
		return nil, err
	}
	out := []Workspace{*def}
	for _, w := range ws {
		if !w.IsDefault {
			out = append(out, w)
		}
	}
	return out, nil
}

func (w *Workspace) validate() error {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" || len(w.Name) > 100 {
		return fmt.Errorf("%w: workspace name required (at most 100 characters)", ErrInvalid)
	}
	if !reSlug.MatchString(w.Slug) {
		return fmt.Errorf("%w: slug must match ^[a-z0-9][a-z0-9-]{0,62}$", ErrInvalid)
	}
	return nil
}

// CreateWorkspace adds a (non-default) workspace to w.OrgID; slugs are unique per org.
func (s *Store) CreateWorkspace(ctx context.Context, w *Workspace) error {
	if err := w.validate(); err != nil {
		return err
	}
	have, err := s.ListWorkspaces(ctx, w.OrgID) // also creates the default workspace
	if err != nil {
		return err
	}
	if len(have) >= MaxWorkspacesPerOrg { // shortcut: counted outside the transaction, a soft cap
		return fmt.Errorf("%w: at most %d workspaces per organization", ErrInvalid, MaxWorkspacesPerOrg)
	}
	for _, h := range have {
		if h.Slug == w.Slug {
			return ErrExists
		}
	}
	w.ID, w.IsDefault, w.CreatedAt = NewID(), false, time.Now().UTC()
	return s.update(ctx, func(tx Tx) error { return putJSON(tx, wsKey(w.OrgID, w.ID), w) })
}

// UpdateWorkspace renames a workspace (name and slug; slugs stay unique, the default keeps its).
func (s *Store) UpdateWorkspace(ctx context.Context, org, id, name, slug string) (*Workspace, error) {
	cur, err := s.GetWorkspace(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if name != "" {
		cur.Name = name
	}
	if slug != "" && !cur.IsDefault {
		cur.Slug = slug
	}
	if err := cur.validate(); err != nil {
		return nil, err
	}
	have, err := s.ListWorkspaces(ctx, org)
	if err != nil {
		return nil, err
	}
	for _, h := range have {
		if h.Slug == cur.Slug && h.ID != cur.ID {
			return nil, ErrExists
		}
	}
	return cur, s.update(ctx, func(tx Tx) error { return putJSON(tx, wsKey(org, cur.ID), cur) })
}

// WorkspaceContents counts what a workspace holds, by resource type (only non-zero counts).
func (s *Store) WorkspaceContents(ctx context.Context, org, id string) (map[string]int, error) {
	ws := WSID(id)
	out := map[string]int{}
	add := func(name string, n int) {
		if n > 0 {
			out[name] += n
		}
	}
	count := func(name string, items int, err error) error {
		add(name, items)
		return err
	}
	keys, err := s.ListKeys(ctx, org)
	if err := count("api_keys", countWS(keys, func(k APIKey) string { return k.WorkspaceID }, ws), err); err != nil {
		return nil, err
	}
	provs, err := s.ListProviders(ctx, false)
	if err := count("providers", countWS(provs, func(p Provider) string {
		if p.OrgID != org {
			return "\x00" // never matches
		}
		return p.WorkspaceID
	}, ws), err); err != nil {
		return nil, err
	}
	models, err := s.ListModels(ctx, org)
	if err := count("models", countWS(models, func(m Model) string { return m.WorkspaceID }, ws), err); err != nil {
		return nil, err
	}
	pols, err := s.ListPolicies(ctx, org)
	if err := count("policies", countWS(pols, func(p Policy) string { return p.WorkspaceID }, ws), err); err != nil {
		return nil, err
	}
	presets, err := s.ListPresets(ctx, org)
	if err := count("presets", countWS(presets, func(p Preset) string { return p.WorkspaceID }, ws), err); err != nil {
		return nil, err
	}
	traces, err := s.ListTraceExports(ctx, org)
	if err := count("trace_exports", countWS(traces, func(t TraceExport) string { return t.WorkspaceID }, ws), err); err != nil {
		return nil, err
	}
	kbs, err := s.ListKBs(ctx, org)
	if err := count("knowledge_bases", countWS(kbs, func(k KB) string { return k.WorkspaceID }, ws), err); err != nil {
		return nil, err
	}
	toks, err := s.ListMgmtTokens(ctx, org)
	if err := count("mgmt_tokens", countWS(toks, func(t MgmtToken) string {
		if t.WorkspaceID == "" {
			return "\x00" // an org-wide token belongs to no workspace
		}
		return t.WorkspaceID
	}, ws), err); err != nil {
		return nil, err
	}
	return out, nil
}

func countWS[T any](items []T, wsOf func(T) string, ws string) int {
	n := 0
	for _, it := range items {
		if WSID(wsOf(it)) == ws {
			n++
		}
	}
	return n
}

// DeleteWorkspace removes an empty non-default workspace and its memberships. Callers check
// emptiness with WorkspaceContents first (resources are never cascade-deleted).
func (s *Store) DeleteWorkspace(ctx context.Context, org, id string) error {
	if WSID(id) == "" {
		return fmt.Errorf("%w: the default workspace cannot be deleted", ErrInvalid)
	}
	if _, err := s.GetWorkspace(ctx, org, id); err != nil {
		return err
	}
	if err := s.DeletePrefix(ctx, "wsmem/"+org+"/"+id+"/"); err != nil {
		return err
	}
	return s.update(ctx, func(tx Tx) error { return tx.Delete(wsKey(org, id)) })
}

// SetWorkspaceMember adds or updates a membership of a non-default workspace; the user must be
// an org member.
func (s *Store) SetWorkspaceMember(ctx context.Context, m WorkspaceMember) error {
	if WSID(m.WorkspaceID) == "" {
		return fmt.Errorf("%w: every org member belongs to the default workspace", ErrInvalid)
	}
	if !ValidWSRole(m.Role) {
		return fmt.Errorf("%w: workspace role must be admin, member or viewer", ErrInvalid)
	}
	if _, err := s.GetWorkspace(ctx, m.OrgID, m.WorkspaceID); err != nil {
		return err
	}
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get("mem/" + m.OrgID + "/" + m.UserID); errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: user is not a member of this organization", ErrInvalid)
		} else if err != nil {
			return err
		}
		return putJSON(tx, wsMemKey(m.OrgID, m.WorkspaceID, m.UserID), m)
	})
}

func (s *Store) GetWorkspaceMember(ctx context.Context, org, ws, user string) (*WorkspaceMember, error) {
	return getJSON[WorkspaceMember](s.get(ctx), wsMemKey(org, ws, user))
}

func (s *Store) RemoveWorkspaceMember(ctx context.Context, org, ws, user string) error {
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get(wsMemKey(org, ws, user)); err != nil {
			return err
		}
		return tx.Delete(wsMemKey(org, ws, user))
	})
}

func (s *Store) ListWorkspaceMembers(ctx context.Context, org, ws string) ([]WorkspaceMember, error) {
	return listJSON[WorkspaceMember](ctx, s.KV, "wsmem/"+org+"/"+ws+"/", "", 0)
}

// removeUserWorkspaces drops every workspace membership of user in org.
func (s *Store) removeUserWorkspaces(ctx context.Context, org, user string) error {
	ms, err := listJSON[WorkspaceMember](ctx, s.KV, "wsmem/"+org+"/", "", 0)
	if err != nil {
		return err
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		for _, m := range ms {
			if m.UserID == user {
				if err := tx.Delete(wsMemKey(org, m.WorkspaceID, user)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// WorkspaceRole is a user's role in workspace ws of org, given their org role: admin for org
// owners and admins, the org role in the default workspace, else the membership role ("" = none).
func (s *Store) WorkspaceRole(ctx context.Context, org, ws, user string, orgRole Role) (Role, error) {
	switch {
	case orgRole.Rank() >= RoleAdmin.Rank():
		return RoleAdmin, nil
	case WSID(ws) == "":
		return orgRole, nil
	}
	m, err := s.GetWorkspaceMember(ctx, org, ws, user)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return m.Role, nil
}

type wsCtx struct{}

// WithWorkspace marks ctx as work for workspace ws (knowledge-base embedding without an API key).
func WithWorkspace(ctx context.Context, ws string) context.Context {
	return context.WithValue(ctx, wsCtx{}, WSID(ws))
}

// WorkspaceOf is the workspace WithWorkspace set on ctx ("" = default).
func WorkspaceOf(ctx context.Context) string {
	ws, _ := ctx.Value(wsCtx{}).(string)
	return ws
}

// WorkspaceSet reports the workspace WithWorkspace set on ctx and whether one was set.
func WorkspaceSet(ctx context.Context) (string, bool) {
	ws, ok := ctx.Value(wsCtx{}).(string)
	return ws, ok
}
