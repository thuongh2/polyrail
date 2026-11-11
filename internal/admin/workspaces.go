package admin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/polyrail/polyrail/internal/store"
)

// Workspaces (D31, docs/API.md "Workspaces"). Org owners and admins create and delete
// workspaces and are admins of all of them; workspace admins manage the keys, BYOK providers,
// private models, policies, presets and trace exports of their workspace. Every org member is
// in the default workspace with their org role. Routes registered with a.ws accept any org
// member (and workspace-limited management tokens); their handlers check the workspace role.

type wsLimitCtx struct{}

// wsLimit is the workspace (stored form) a workspace-limited management token is confined to.
func wsLimit(r *http.Request) (string, bool) {
	ws, ok := r.Context().Value(wsLimitCtx{}).(string)
	return ws, ok
}

// ws registers a workspace-aware org route: any org member passes; the handler checks the
// caller's role in the workspace of what it touches (wsCan / wsRole).
func (a *API) ws(h http.HandlerFunc) http.HandlerFunc { return a.orgAuth(store.RoleViewer, true, h) }

// wsRole is the caller's role in workspace ws ("" = none) of the route's org.
func (a *API) wsRole(r *http.Request, ws string) (store.Role, error) {
	ws = store.WSID(ws)
	if lim, ok := wsLimit(r); ok {
		if lim != ws {
			return "", nil
		}
		return role(r), nil
	}
	if viaToken(r) && role(r).Rank() < store.RoleAdmin.Rank() { // an org-wide token below admin acts in the default workspace
		if ws == "" {
			return role(r), nil
		}
		return "", nil
	}
	return a.S.WorkspaceRole(r.Context(), r.PathValue("org"), ws, user(r).ID, role(r))
}

// wsCan requires role need in workspace ws: 404 when the caller cannot see the workspace,
// 403 when they can but their role is lower. Platform routes (org "_") always pass (operators).
func (a *API) wsCan(r *http.Request, ws string, need store.Role) error {
	if owner(r) == "" {
		return nil
	}
	rl, err := a.wsRole(r, ws)
	switch {
	case err != nil:
		return err
	case rl == "":
		return store.ErrNotFound
	case rl.Rank() < need.Rank():
		return errForbidden
	}
	return nil
}

// orgWide reports whether the caller acts for the whole org (owner/admin, not workspace-limited).
func orgWide(r *http.Request) bool {
	_, limited := wsLimit(r)
	return !limited && role(r).Rank() >= store.RoleAdmin.Rank()
}

// resolveWS turns a workspace_id input into the stored form: "" picks the default workspace (or
// a limited token's workspace); unknown ids are invalid.
func (a *API) resolveWS(r *http.Request, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		if lim, ok := wsLimit(r); ok {
			return lim, nil
		}
		return "", nil
	}
	w, err := a.S.GetWorkspace(r.Context(), r.PathValue("org"), id)
	if errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("%w: workspace %q not found", store.ErrInvalid, id)
	}
	if err != nil {
		return "", err
	}
	return store.WSID(w.ID), nil
}

// keepWS is the workspace of an updated resource: the stored one; an input naming another is
// refused (resources do not move between workspaces).
func (a *API) keepWS(r *http.Request, stored, in string) (string, error) {
	if strings.TrimSpace(in) == "" {
		return store.WSID(stored), nil
	}
	ws, err := a.resolveWS(r, in)
	if err == nil && ws != store.WSID(stored) {
		err = fmt.Errorf("%w: workspace_id cannot change", store.ErrInvalid)
	}
	return ws, err
}

// wsFilter returns which workspaces (stored form) a list shows: ?workspace=<id> alone (it must
// be visible to the caller with role need), else every workspace where the caller has need.
func (a *API) wsFilter(r *http.Request, need store.Role) (func(ws string) bool, error) {
	if owner(r) == "" {
		return func(string) bool { return true }, nil
	}
	if q := r.URL.Query().Get("workspace"); q != "" {
		ws, err := a.resolveWS(r, q)
		if err != nil {
			return nil, err
		}
		if err := a.wsCan(r, ws, need); err != nil {
			return nil, err
		}
		return func(x string) bool { return store.WSID(x) == ws }, nil
	}
	if orgWide(r) {
		return func(string) bool { return true }, nil
	}
	roles, err := a.wsRoles(r)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(slices.Collect(maps.Values(roles)), func(rl store.Role) bool { return rl.Rank() >= need.Rank() }) {
		return nil, errForbidden // e.g. an admin-only list for someone who administers no workspace
	}
	return func(x string) bool { return roles[store.WSID(x)].Rank() >= need.Rank() }, nil
}

// wsRoles is the caller's role in each workspace of the org they can see (stored ids).
func (a *API) wsRoles(r *http.Request) (map[string]store.Role, error) {
	ws, err := a.S.ListWorkspaces(r.Context(), r.PathValue("org"))
	if err != nil {
		return nil, err
	}
	out := map[string]store.Role{}
	for _, w := range ws {
		rl, err := a.wsRole(r, w.ID)
		if err != nil {
			return nil, err
		}
		if rl != "" {
			out[store.WSID(w.ID)] = rl
		}
	}
	return out, nil
}

func (a *API) registerWorkspaces(mux *http.ServeMux) {
	p := "/api/orgs/{org}/workspaces"
	mux.HandleFunc("GET "+p, a.ws(a.listWorkspaces))
	mux.HandleFunc("POST "+p, a.org(store.RoleAdmin, a.createWorkspace))
	mux.HandleFunc("GET "+p+"/{ws}", a.ws(a.getWorkspace))
	mux.HandleFunc("PATCH "+p+"/{ws}", a.org(store.RoleAdmin, a.patchWorkspace))
	mux.HandleFunc("DELETE "+p+"/{ws}", a.org(store.RoleAdmin, a.deleteWorkspace))
	mux.HandleFunc("GET "+p+"/{ws}/members", a.ws(a.listWorkspaceMembers))
	mux.HandleFunc("PUT "+p+"/{ws}/members/{user}", a.ws(a.putWorkspaceMember))
	mux.HandleFunc("DELETE "+p+"/{ws}/members/{user}", a.ws(a.removeWorkspaceMember))
}

type workspaceView struct {
	store.Workspace
	Role store.Role `json:"role"` // the caller's role in it
}

func (a *API) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := a.S.ListWorkspaces(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	out := []workspaceView{}
	for _, x := range ws {
		rl, err := a.wsRole(r, x.ID)
		if err != nil {
			fail(w, err)
			return
		}
		if rl != "" {
			out = append(out, workspaceView{x, rl})
		}
	}
	writeJSON(w, 200, out)
}

func (a *API) getWorkspace(w http.ResponseWriter, r *http.Request) {
	x, err := a.S.GetWorkspace(r.Context(), r.PathValue("org"), r.PathValue("ws"))
	if err == nil {
		err = a.wsCan(r, x.ID, store.RoleViewer)
	}
	if err != nil {
		fail(w, err)
		return
	}
	rl, _ := a.wsRole(r, x.ID)
	writeJSON(w, 200, workspaceView{*x, rl})
}

type workspaceInput struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (a *API) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in workspaceInput
	if !decode(w, r, &in) {
		return
	}
	org := r.PathValue("org")
	x := &store.Workspace{OrgID: org, Name: in.Name, Slug: strings.TrimSpace(in.Slug)}
	if err := a.S.CreateWorkspace(r.Context(), x); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "workspace.create", x.ID, x.Name)
	writeJSON(w, http.StatusCreated, workspaceView{*x, store.RoleAdmin})
}

func (a *API) patchWorkspace(w http.ResponseWriter, r *http.Request) {
	var in workspaceInput
	if !decode(w, r, &in) {
		return
	}
	org := r.PathValue("org")
	x, err := a.S.UpdateWorkspace(r.Context(), org, r.PathValue("ws"), in.Name, strings.TrimSpace(in.Slug))
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "workspace.update", x.ID, x.Name+" "+x.Slug)
	writeJSON(w, 200, workspaceView{*x, store.RoleAdmin})
}

// deleteWorkspace removes an empty workspace; one that still holds resources answers 409 with
// their counts (nothing is deleted with it). The default workspace cannot be deleted.
func (a *API) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ctx, org := r.Context(), r.PathValue("org")
	x, err := a.S.GetWorkspace(ctx, org, r.PathValue("ws"))
	if err != nil {
		fail(w, err)
		return
	}
	if x.IsDefault {
		fail(w, fmt.Errorf("%w: the default workspace cannot be deleted", store.ErrInvalid))
		return
	}
	counts, err := a.S.WorkspaceContents(ctx, org, x.ID)
	if err != nil {
		fail(w, err)
		return
	}
	if len(counts) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
			"message": "the workspace is not empty: move or delete its resources first", "counts": counts}})
		return
	}
	if err := a.S.DeleteWorkspace(ctx, org, x.ID); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "workspace.delete", x.ID, x.Name)
	w.WriteHeader(http.StatusNoContent)
}

type workspaceMemberView struct {
	UserID string     `json:"user_id"`
	Email  string     `json:"email"`
	Name   string     `json:"name"`
	Role   store.Role `json:"role"`
	// Via is how the user is in the workspace: org_role (owners and admins everywhere, every
	// member in the default workspace), member (added here) or idp (a group mapping).
	Via string `json:"via"`
}

// listWorkspaceMembers lists who is in a workspace (workspace admins).
func (a *API) listWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	ctx, org := r.Context(), r.PathValue("org")
	x, err := a.S.GetWorkspace(ctx, org, r.PathValue("ws"))
	if err == nil {
		err = a.wsCan(r, x.ID, store.RoleAdmin)
	}
	if err != nil {
		fail(w, err)
		return
	}
	ms, err := a.S.ListMembers(ctx, org)
	if err != nil {
		fail(w, err)
		return
	}
	explicit := map[string]store.WorkspaceMember{}
	if !x.IsDefault {
		wms, err := a.S.ListWorkspaceMembers(ctx, org, x.ID)
		if err != nil {
			fail(w, err)
			return
		}
		for _, m := range wms {
			explicit[m.UserID] = m
		}
	}
	out := []workspaceMemberView{}
	for _, m := range ms {
		v := workspaceMemberView{UserID: m.UserID, Via: "org_role"}
		switch wm, ok := explicit[m.UserID]; {
		case m.Role.Rank() >= store.RoleAdmin.Rank():
			v.Role = store.RoleAdmin
		case x.IsDefault:
			v.Role = m.Role
		case ok:
			v.Role, v.Via = wm.Role, "member"
			if wm.IdP {
				v.Via = "idp"
			}
		default:
			continue
		}
		if u, err := a.S.GetUser(ctx, m.UserID); err == nil {
			v.Email, v.Name = u.Email, u.Name
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	writeJSON(w, 200, out)
}

// putWorkspaceMember adds an org member to a workspace or changes their role (workspace admins).
func (a *API) putWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role store.Role `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, org, uid := r.Context(), r.PathValue("org"), r.PathValue("user")
	x, err := a.S.GetWorkspace(ctx, org, r.PathValue("ws"))
	if err == nil {
		err = a.wsCan(r, x.ID, store.RoleAdmin)
	}
	if err != nil {
		fail(w, err)
		return
	}
	m := store.WorkspaceMember{OrgID: org, WorkspaceID: x.ID, UserID: uid, Role: in.Role}
	if err := a.S.SetWorkspaceMember(ctx, m); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "workspace.member.set", x.ID, uid+" "+string(in.Role))
	writeJSON(w, 200, m)
}

func (a *API) removeWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	ctx, org, uid := r.Context(), r.PathValue("org"), r.PathValue("user")
	x, err := a.S.GetWorkspace(ctx, org, r.PathValue("ws"))
	if err == nil {
		err = a.wsCan(r, x.ID, store.RoleAdmin)
	}
	if err == nil {
		err = a.S.RemoveWorkspaceMember(ctx, org, x.ID, uid)
	}
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "workspace.member.remove", x.ID, uid)
	w.WriteHeader(http.StatusNoContent)
}

// workspaceRefs is /api/auth/me's per-org workspace list for a user with orgRole.
func (a *API) workspaceRefs(ctx context.Context, org, uid string, orgRole store.Role) []workspaceView {
	out := []workspaceView{}
	ws, err := a.S.ListWorkspaces(ctx, org)
	if err != nil {
		return out
	}
	for _, x := range ws {
		if rl, err := a.S.WorkspaceRole(ctx, org, x.ID, uid, orgRole); err == nil && rl != "" {
			out = append(out, workspaceView{x, rl})
		}
	}
	return out
}

// providerWS sets the workspace of org provider p (create: from input; update: the stored one)
// and checks the caller is its workspace admin. Sharing across workspaces is for org admins.
func (a *API) providerWS(r *http.Request, p, old *store.Provider) error {
	var err error
	if old != nil {
		if err = a.wsCan(r, old.WorkspaceID, store.RoleAdmin); err == nil {
			p.WorkspaceID, err = a.keepWS(r, old.WorkspaceID, p.WorkspaceID)
		}
	} else if p.WorkspaceID, err = a.resolveWS(r, p.WorkspaceID); err == nil {
		err = a.wsCan(r, p.WorkspaceID, store.RoleAdmin)
	}
	if err != nil {
		return err
	}
	if p.SharedAcrossWorkspaces != (old != nil && old.SharedAcrossWorkspaces) && !orgWide(r) {
		return errForbidden
	}
	return nil
}

// usageWorkspaces prepares workspace grouping and filtering of usage rollups (kept per key):
// key id → stored workspace id when grouping or filtering, and with ?workspace=<id> the filter
// (nil = no filter). Keys deleted since count in the default workspace.
func (a *API) usageWorkspaces(r *http.Request, org string, group bool) (map[string]string, func(string) bool, error) {
	q := r.URL.Query().Get("workspace")
	if org == "_" || (!group && q == "") {
		if q != "" {
			return nil, nil, fmt.Errorf("%w: workspace filters organization usage only", store.ErrInvalid)
		}
		return nil, nil, nil
	}
	keys, err := a.S.ListKeys(r.Context(), org)
	if err != nil {
		return nil, nil, err
	}
	keyWS := make(map[string]string, len(keys))
	for _, k := range keys {
		keyWS[k.ID] = store.WSID(k.WorkspaceID)
	}
	if q == "" {
		return keyWS, nil, nil
	}
	ws, err := a.resolveWS(r, q)
	if err != nil {
		return nil, nil, err
	}
	return keyWS, func(x string) bool { return x == ws }, nil
}

// syncWorkspaces applies the workspace group mappings of an org's IdP to a user in groups: they
// get the highest mapped role of each mapped workspace; a membership an earlier mapping set (IdP)
// is removed when no mapping matches any more. Memberships added by hand are left alone.
func (a *API) syncWorkspaces(ctx context.Context, id *store.Identity, uid string, groups []string) error {
	want := map[string]store.Role{}
	for _, m := range id.GroupMappings {
		if m.WorkspaceID == "" {
			continue
		}
		if _, ok := want[m.WorkspaceID]; !ok {
			want[m.WorkspaceID] = ""
		}
		rl := m.Role
		if rl == "" {
			rl = store.RoleMember
		}
		if slices.ContainsFunc(groups, func(g string) bool { return strings.EqualFold(g, m.Group) }) && rl.Rank() > want[m.WorkspaceID].Rank() {
			want[m.WorkspaceID] = rl
		}
	}
	for ws, rl := range want {
		cur, err := a.S.GetWorkspaceMember(ctx, id.OrgID, ws, uid)
		switch {
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return err
		case cur != nil && !cur.IdP: // added by an admin: not the IdP's to change
		case rl == "" && cur != nil:
			if err := a.S.RemoveWorkspaceMember(ctx, id.OrgID, ws, uid); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		case rl != "" && (cur == nil || cur.Role != rl):
			err := a.S.SetWorkspaceMember(ctx, store.WorkspaceMember{OrgID: id.OrgID, WorkspaceID: ws, UserID: uid, Role: rl, IdP: true})
			if err != nil && !errors.Is(err, store.ErrNotFound) { // a mapped workspace deleted since: skip it
				return err
			}
		}
	}
	return nil
}

// scimGroupNames lists the display names of the org's SCIM groups uid is in.
func (a *API) scimGroupNames(ctx context.Context, org, uid string) ([]string, error) {
	gs, err := a.S.ListSCIMGroups(ctx, org)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, g := range gs {
		if slices.Contains(g.Members, uid) {
			names = append(names, g.DisplayName)
		}
	}
	return names, nil
}
