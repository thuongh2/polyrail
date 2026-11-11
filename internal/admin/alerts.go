package admin

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/polyrail/polyrail/internal/alert"
	"github.com/polyrail/polyrail/internal/store"
)

// Alert channels (docs/KEYS.md): shoutrrr URLs per org (admin role) or platform (operator).
// The URL is write-only: responses show the service name, audit entries too.

func (a *API) registerAlerts(mux *http.ServeMux) {
	for _, s := range []struct {
		prefix string
		wrap   func(http.HandlerFunc) http.HandlerFunc
	}{
		{"/api/orgs/{org}", func(h http.HandlerFunc) http.HandlerFunc { return a.org(store.RoleAdmin, h) }},
		{"/api/admin", func(h http.HandlerFunc) http.HandlerFunc { return a.operator(a.platform(h)) }},
	} {
		p := s.prefix + "/alerts"
		mux.HandleFunc("GET "+p, s.wrap(a.listAlerts))
		mux.HandleFunc("POST "+p, s.wrap(a.createAlert))
		mux.HandleFunc("PATCH "+p+"/{aid}", s.wrap(a.patchAlert))
		mux.HandleFunc("DELETE "+p+"/{aid}", s.wrap(a.deleteAlert))
		mux.HandleFunc("POST "+p+"/{aid}/test", s.wrap(a.testAlert))
	}
}

type alertInput struct {
	Name    *string   `json:"name"`
	URL     *string   `json:"url"`
	Events  *[]string `json:"events"`
	Enabled *bool     `json:"enabled"`
}

func checkAlertEvents(events []string) error {
	if len(events) == 0 || len(events) > len(alert.Types)+1 {
		return fmt.Errorf("%w: events must list event types or \"*\"", store.ErrInvalid)
	}
	for _, e := range events {
		if e != "*" && !slices.Contains(alert.Types, e) {
			return fmt.Errorf("%w: unknown event %q (one of %s or *)", store.ErrInvalid, e, strings.Join(alert.Types, ", "))
		}
	}
	return nil
}

func (a *API) checkAlertURL(r *http.Request, raw string) error {
	if err := alert.CheckURL(raw, owner(r) != "", a.cfg.AllowPrivateURLs); err != nil {
		return fmt.Errorf("%w: url: %v", store.ErrInvalid, err)
	}
	return nil
}

func checkAlertName(n string) error {
	if strings.TrimSpace(n) == "" || len(n) > 100 {
		return fmt.Errorf("%w: name must be 1-100 characters", store.ErrInvalid)
	}
	return nil
}

func (a *API) listAlerts(w http.ResponseWriter, r *http.Request) {
	cs, err := a.S.ListAlertChannels(r.Context(), owner(r))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, cs) // URL is json:"-"
}

func (a *API) createAlert(w http.ResponseWriter, r *http.Request) {
	var in alertInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil || in.URL == nil || in.Events == nil {
		fail(w, fmt.Errorf("%w: name, url and events required", store.ErrInvalid))
		return
	}
	if err := checkAlertName(*in.Name); err != nil {
		fail(w, err)
		return
	}
	if err := checkAlertEvents(*in.Events); err != nil {
		fail(w, err)
		return
	}
	if err := a.checkAlertURL(r, *in.URL); err != nil {
		fail(w, err)
		return
	}
	org := owner(r)
	cs, err := a.S.ListAlertChannels(r.Context(), org)
	if err != nil {
		fail(w, err)
		return
	}
	if len(cs) >= alert.MaxPerScope {
		fail(w, fmt.Errorf("%w: at most %d alert channels", store.ErrInvalid, alert.MaxPerScope))
		return
	}
	c := &store.AlertChannel{OrgID: org, Name: strings.TrimSpace(*in.Name), URL: *in.URL, Service: alert.Service(*in.URL),
		Events: *in.Events, Enabled: in.Enabled == nil || *in.Enabled}
	if err := a.S.CreateAlertChannel(r.Context(), c); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "alert.create", c.ID, c.Service)
	writeJSON(w, http.StatusCreated, c)
}

func (a *API) patchAlert(w http.ResponseWriter, r *http.Request) {
	var in alertInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name != nil {
		if err := checkAlertName(*in.Name); err != nil {
			fail(w, err)
			return
		}
	}
	if in.Events != nil {
		if err := checkAlertEvents(*in.Events); err != nil {
			fail(w, err)
			return
		}
	}
	if in.URL != nil {
		if err := a.checkAlertURL(r, *in.URL); err != nil {
			fail(w, err)
			return
		}
	}
	org, id := owner(r), r.PathValue("aid")
	c, err := a.S.UpdateAlertChannel(r.Context(), org, id, func(c *store.AlertChannel) error {
		if in.Name != nil {
			c.Name = strings.TrimSpace(*in.Name)
		}
		if in.URL != nil {
			c.URL, c.Service = *in.URL, alert.Service(*in.URL)
		}
		if in.Events != nil {
			c.Events = *in.Events
		}
		if in.Enabled != nil {
			if *in.Enabled && !c.Enabled {
				c.Failures = 0 // re-enabling starts a fresh failure count
			}
			c.Enabled = *in.Enabled
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "alert.update", id, fmt.Sprintf("service=%s enabled=%v url_changed=%v", c.Service, c.Enabled, in.URL != nil))
	writeJSON(w, 200, c)
}

func (a *API) deleteAlert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("aid")
	if err := a.S.DeleteAlertChannel(r.Context(), owner(r), id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "alert.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// testAlert sends a test message now. Tenants see only whether it worked (a service's error can
// describe the host it reached); operators get the error text.
func (a *API) testAlert(w http.ResponseWriter, r *http.Request) {
	if a.Alerts == nil {
		writeErr(w, http.StatusServiceUnavailable, "alerts are not enabled")
		return
	}
	org, id := owner(r), r.PathValue("aid")
	c, err := a.S.GetAlertChannel(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": true}
	if err := a.Alerts.Test(r.Context(), *c); err != nil {
		msg := "send failed: check the URL, credentials and the service's settings"
		if org == "" {
			msg = err.Error()
			if len(msg) > 300 {
				msg = msg[:300]
			}
		}
		out = map[string]any{"ok": false, "error": msg}
	}
	writeJSON(w, 200, out)
}
