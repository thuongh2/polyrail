package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/polyrail/polyrail/internal/broadcast"
	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

// Trace exports (docs/API.md "Trace exports", D30): per org (admin role) or platform (operator).
// Headers are write-only: responses list header names, audit entries the endpoint host.

func (a *API) registerTraceExports(mux *http.ServeMux) {
	for _, s := range []struct {
		prefix string
		wrap   func(http.HandlerFunc) http.HandlerFunc
	}{
		{"/api/orgs/{org}", a.ws}, // workspace admins (D31)
		{"/api/admin", func(h http.HandlerFunc) http.HandlerFunc { return a.operator(a.platform(h)) }},
	} {
		p := s.prefix + "/trace-exports"
		mux.HandleFunc("GET "+p, s.wrap(a.listTraceExports))
		mux.HandleFunc("POST "+p, s.wrap(a.createTraceExport))
		mux.HandleFunc("PATCH "+p+"/{tid}", s.wrap(a.patchTraceExport))
		mux.HandleFunc("DELETE "+p+"/{tid}", s.wrap(a.deleteTraceExport))
		mux.HandleFunc("POST "+p+"/{tid}/test", s.wrap(a.testTraceExport))
	}
}

type traceExportInput struct {
	Name     *string            `json:"name"`
	Type     *string            `json:"type"`
	Endpoint *string            `json:"endpoint"`
	Headers  *map[string]string `json:"headers"` // replaces all headers when present
	Langfuse *struct {
		PublicKey string `json:"public_key"`
		SecretKey string `json:"secret_key"`
	} `json:"langfuse"` // sets the Authorization header of a langfuse export
	SampleRate     *float64  `json:"sample_rate"`
	KeyIDs         *[]string `json:"key_ids"`
	IncludeContent *bool     `json:"include_content"`
	Enabled        *bool     `json:"enabled"`
	WorkspaceID    string    `json:"workspace_id"` // org exports, create only ("" = default: every workspace's requests)
}

// apply copies the set fields of in onto t.
func (in traceExportInput) apply(t *store.TraceExport) error {
	if in.Name != nil {
		t.Name = *in.Name
	}
	if in.Type != nil {
		t.Type = *in.Type
	}
	if in.Endpoint != nil {
		if e := strings.TrimSpace(*in.Endpoint); e != t.Endpoint {
			t.Headers = nil // secrets entered for one endpoint never follow it to another (send them again)
			t.Endpoint = e
		}
	}
	if in.Headers != nil {
		t.Headers = *in.Headers
	}
	if lf := in.Langfuse; lf != nil {
		if t.Type != "langfuse" || lf.PublicKey == "" || lf.SecretKey == "" {
			return fmt.Errorf("%w: langfuse needs type langfuse, public_key and secret_key", store.ErrInvalid)
		}
		if t.Headers == nil {
			t.Headers = map[string]string{}
		}
		t.Headers["Authorization"] = broadcast.LangfuseAuth(lf.PublicKey, lf.SecretKey)
	}
	if in.SampleRate != nil {
		t.SampleRate = *in.SampleRate
	}
	if in.KeyIDs != nil {
		t.KeyIDs = *in.KeyIDs
	}
	if in.IncludeContent != nil {
		t.IncludeContent = *in.IncludeContent
	}
	if in.Enabled != nil {
		if *in.Enabled && !t.Enabled {
			t.Failures = 0 // re-enabling starts a fresh failure count
		}
		t.Enabled = *in.Enabled
	}
	return nil
}

// checkTraceExport validates the endpoint (SSRF guard: https on a public host unless private
// URLs are allowed) and that an org export's key filter names keys of that org.
func (a *API) checkTraceExport(ctx context.Context, t *store.TraceExport) error {
	if t.Type == "langfuse" && t.Headers["Authorization"] == "" {
		return fmt.Errorf("%w: langfuse needs langfuse.public_key and langfuse.secret_key", store.ErrInvalid)
	}
	if err := broadcast.CheckURL(*t, a.cfg.AllowPrivateURLs); err != nil {
		return fmt.Errorf("%w: endpoint %v", store.ErrInvalid, err)
	}
	if t.OrgID != "" {
		for _, id := range t.KeyIDs {
			k, err := a.S.GetKey(ctx, t.OrgID, id)
			if errors.Is(err, store.ErrNotFound) || (err == nil && t.WorkspaceID != "" && store.WSID(k.WorkspaceID) != t.WorkspaceID) {
				return fmt.Errorf("%w: key %q is not a key of this organization or workspace", store.ErrInvalid, id)
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *API) reloadTraces(ctx context.Context) {
	if a.Traces != nil {
		if err := a.Traces.Reload(ctx); err != nil {
			slog.Error("trace exports reload failed", "err", err)
		}
	}
}

func (a *API) listTraceExports(w http.ResponseWriter, r *http.Request) {
	ts, err := a.S.ListTraceExports(r.Context(), owner(r))
	if err != nil {
		fail(w, err)
		return
	}
	show, err := a.wsFilter(r, store.RoleAdmin)
	if err != nil {
		fail(w, err)
		return
	}
	ts = slices.DeleteFunc(ts, func(t store.TraceExport) bool { return !show(t.WorkspaceID) })
	writeJSON(w, 200, ts) // Headers is json:"-"
}

func (a *API) createTraceExport(w http.ResponseWriter, r *http.Request) {
	var in traceExportInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil || in.Type == nil || in.Endpoint == nil {
		fail(w, fmt.Errorf("%w: name, type and endpoint required", store.ErrInvalid))
		return
	}
	org := owner(r)
	t := &store.TraceExport{OrgID: org, SampleRate: 1, Enabled: true}
	if org != "" {
		var err error
		if t.WorkspaceID, err = a.resolveWS(r, in.WorkspaceID); err == nil {
			err = a.wsCan(r, t.WorkspaceID, store.RoleAdmin)
		}
		if err != nil {
			fail(w, err)
			return
		}
	}
	if err := in.apply(t); err != nil {
		fail(w, err)
		return
	}
	if org != "" && t.IncludeContent && !orgWide(r) { // content is for org admins only (D28)
		fail(w, errForbidden)
		return
	}
	if err := a.checkTraceExport(r.Context(), t); err != nil {
		fail(w, err)
		return
	}
	ts, err := a.S.ListTraceExports(r.Context(), org)
	if err != nil {
		fail(w, err)
		return
	}
	if len(ts) >= broadcast.MaxPerScope {
		fail(w, fmt.Errorf("%w: at most %d trace exports", store.ErrInvalid, broadcast.MaxPerScope))
		return
	}
	if err := a.S.CreateTraceExport(r.Context(), t); err != nil {
		fail(w, err)
		return
	}
	a.reloadTraces(r.Context())
	a.audit(r, r.PathValue("org"), "trace_export.create", t.ID, t.Type+" "+hostOf(t.Endpoint))
	writeJSON(w, http.StatusCreated, t)
}

func (a *API) patchTraceExport(w http.ResponseWriter, r *http.Request) {
	var in traceExportInput
	if !decode(w, r, &in) {
		return
	}
	org, id := owner(r), r.PathValue("tid")
	cur, err := a.S.GetTraceExport(r.Context(), org, id)
	if err == nil {
		err = a.wsCan(r, cur.WorkspaceID, store.RoleAdmin)
	}
	if err == nil {
		_, err = a.keepWS(r, cur.WorkspaceID, in.WorkspaceID)
	}
	if err == nil && org != "" && !orgWide(r) && (cur.IncludeContent || (in.IncludeContent != nil && *in.IncludeContent)) {
		err = errForbidden // exports carrying content are for org admins only (D28); others may delete them
	}
	if err == nil {
		if err = in.apply(cur); err == nil {
			err = a.checkTraceExport(r.Context(), cur) // outside the write: it resolves DNS
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	t, err := a.S.UpdateTraceExport(r.Context(), org, id, func(t *store.TraceExport) error { return in.apply(t) })
	if err != nil {
		fail(w, err)
		return
	}
	a.reloadTraces(r.Context())
	a.audit(r, r.PathValue("org"), "trace_export.update", id, fmt.Sprintf("%s %s enabled=%v", t.Type, hostOf(t.Endpoint), t.Enabled))
	writeJSON(w, 200, t)
}

func (a *API) deleteTraceExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("tid")
	if t, err := a.S.GetTraceExport(r.Context(), owner(r), id); err == nil {
		if err := a.wsCan(r, t.WorkspaceID, store.RoleAdmin); err != nil {
			fail(w, err)
			return
		}
	}
	if err := a.S.DeleteTraceExport(r.Context(), owner(r), id); err != nil {
		fail(w, err)
		return
	}
	a.reloadTraces(r.Context())
	a.audit(r, r.PathValue("org"), "trace_export.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// testTraceExport posts one test span now. Only the collector's status code is reported: its
// body is never read back to the tenant.
func (a *API) testTraceExport(w http.ResponseWriter, r *http.Request) {
	if a.Traces == nil {
		writeErr(w, http.StatusServiceUnavailable, "trace export is not enabled")
		return
	}
	org, id := owner(r), r.PathValue("tid")
	t, err := a.S.GetTraceExport(r.Context(), org, id)
	if err == nil {
		err = a.wsCan(r, t.WorkspaceID, store.RoleAdmin)
	}
	if err != nil {
		fail(w, err)
		return
	}
	status, err := a.Traces.Send(r.Context(), *t, org)
	out := map[string]any{"ok": err == nil && status >= 200 && status < 300, "status": status}
	switch {
	case errors.Is(err, gateway.ErrPrivateAddress):
		out["error"] = "endpoint does not resolve to a public address"
	case err != nil && broadcast.CheckURL(*t, a.cfg.AllowPrivateURLs) != nil:
		out["error"] = "endpoint is not allowed"
	case err != nil:
		out["error"] = "export failed (connection error or timeout)"
	}
	a.audit(r, r.PathValue("org"), "trace_export.test", id, fmt.Sprintf("status=%d", status))
	writeJSON(w, 200, out)
}
