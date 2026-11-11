package admin

import (
	"net/http"
	"slices"

	"github.com/polyrail/polyrail/internal/store"
)

// Presets (docs/API.md "Request routing"): org-scoped saved routing + parameters, used as
// "model": "@preset/<slug>". Members can list them (to use them); admins change them.

func (a *API) registerPresets(mux *http.ServeMux) {
	p := "/api/orgs/{org}/presets"
	mux.HandleFunc("GET "+p, a.ws(a.listPresets))
	mux.HandleFunc("POST "+p, a.ws(a.putPreset))
	mux.HandleFunc("GET "+p+"/{slug}", a.ws(a.getPreset))
	mux.HandleFunc("PUT "+p+"/{slug}", a.ws(a.putPreset))
	mux.HandleFunc("DELETE "+p+"/{slug}", a.ws(a.deletePreset))
}

type presetInput struct {
	Slug     string               `json:"slug"`
	Model    string               `json:"model"`
	Models   []string             `json:"models"`
	Provider *store.ProviderPrefs `json:"provider"`
	Params   map[string]any       `json:"params"`
	// WorkspaceID: create only ("" = default workspace)
	WorkspaceID string `json:"workspace_id"`
}

func (a *API) listPresets(w http.ResponseWriter, r *http.Request) {
	ps, err := a.S.ListPresets(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	show, err := a.wsFilter(r, store.RoleViewer)
	if err != nil {
		fail(w, err)
		return
	}
	ps = slices.DeleteFunc(ps, func(p store.Preset) bool { return !show(p.WorkspaceID) })
	if ps == nil {
		ps = []store.Preset{}
	}
	writeJSON(w, 200, ps)
}

func (a *API) getPreset(w http.ResponseWriter, r *http.Request) {
	p, err := a.S.GetPreset(r.Context(), r.PathValue("org"), r.PathValue("slug"))
	if err == nil {
		err = a.wsCan(r, p.WorkspaceID, store.RoleViewer)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, p)
}

// putPreset creates (POST, slug in the body) or replaces (PUT, slug in the path) a preset.
func (a *API) putPreset(w http.ResponseWriter, r *http.Request) {
	var in presetInput
	if !decode(w, r, &in) {
		return
	}
	create := r.Method == http.MethodPost
	if !create {
		in.Slug = r.PathValue("slug")
	}
	org := r.PathValue("org")
	p := &store.Preset{OrgID: org, Slug: in.Slug, Model: in.Model, Models: in.Models, Provider: in.Provider, Params: in.Params}
	var err error
	if old, gerr := a.S.GetPreset(r.Context(), org, in.Slug); gerr == nil && !create {
		if err = a.wsCan(r, old.WorkspaceID, store.RoleAdmin); err == nil {
			p.WorkspaceID, err = a.keepWS(r, old.WorkspaceID, in.WorkspaceID)
		}
	} else if p.WorkspaceID, err = a.resolveWS(r, in.WorkspaceID); err == nil {
		err = a.wsCan(r, p.WorkspaceID, store.RoleAdmin)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err := a.S.PutPreset(r.Context(), p, create); err != nil {
		fail(w, err)
		return
	}
	action, status := "preset.update", http.StatusOK
	if create {
		action, status = "preset.create", http.StatusCreated
	}
	a.audit(r, org, action, p.Slug, p.Model)
	writeJSON(w, status, p)
}

func (a *API) deletePreset(w http.ResponseWriter, r *http.Request) {
	org, slug := r.PathValue("org"), r.PathValue("slug")
	p, err := a.S.GetPreset(r.Context(), org, slug)
	if err == nil {
		err = a.wsCan(r, p.WorkspaceID, store.RoleAdmin)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err := a.S.DeletePreset(r.Context(), org, slug); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "preset.delete", slug, "")
	w.WriteHeader(http.StatusNoContent)
}
