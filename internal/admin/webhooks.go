package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/agent-gateway/agent-gateway/internal/gateway"
	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/webhook"
)

func (a *API) emit(org, typ string, data map[string]any) {
	if a.Hooks != nil {
		a.Hooks.Emit(org, typ, data)
	}
}

// checkWebhook validates a webhook URL (SSRF guard) and its event list.
func (a *API) checkWebhook(raw string, events []string) error {
	if err := webhook.CheckURL(raw, a.cfg.AllowPrivateURLs); err != nil {
		return fmt.Errorf("%w: url %v", store.ErrInvalid, err)
	}
	if len(events) == 0 || len(events) > len(webhook.Types)+1 {
		return fmt.Errorf("%w: events must list event types or \"*\"", store.ErrInvalid)
	}
	for _, e := range events {
		if e != "*" && !slices.Contains(webhook.Types, e) {
			return fmt.Errorf("%w: unknown event %q", store.ErrInvalid, e)
		}
	}
	return nil
}

// hostOf is what audit entries record of a webhook URL: paths and queries can carry tokens.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func (a *API) listWebhooks(w http.ResponseWriter, r *http.Request) {
	hs, err := a.S.ListWebhooks(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, hs) // Secret is json:"-"
}

type webhookInput struct {
	URL     *string   `json:"url"`
	Events  *[]string `json:"events"`
	Enabled *bool     `json:"enabled"`
}

func (a *API) createWebhook(w http.ResponseWriter, r *http.Request) {
	var in webhookInput
	if !decode(w, r, &in) {
		return
	}
	if in.URL == nil || in.Events == nil {
		fail(w, fmt.Errorf("%w: url and events required", store.ErrInvalid))
		return
	}
	if err := a.checkWebhook(*in.URL, *in.Events); err != nil {
		fail(w, err)
		return
	}
	org := r.PathValue("org")
	hs, err := a.S.ListWebhooks(r.Context(), org)
	if err != nil {
		fail(w, err)
		return
	}
	if len(hs) >= webhook.MaxPerOrg {
		fail(w, fmt.Errorf("%w: at most %d webhooks per organization", store.ErrInvalid, webhook.MaxPerOrg))
		return
	}
	secret, err := webhook.NewSecret()
	if err != nil {
		fail(w, err)
		return
	}
	h := &store.Webhook{OrgID: org, URL: *in.URL, Events: *in.Events, Secret: secret, Enabled: in.Enabled == nil || *in.Enabled}
	if err := a.S.CreateWebhook(r.Context(), h); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "webhook.create", h.ID, hostOf(h.URL))
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": h, "secret": secret}) // secret shown once
}

func (a *API) patchWebhook(w http.ResponseWriter, r *http.Request) {
	var in webhookInput
	if !decode(w, r, &in) {
		return
	}
	org, id := r.PathValue("org"), r.PathValue("id")
	cur, err := a.S.GetWebhook(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	u, ev := cur.URL, cur.Events
	if in.URL != nil {
		u = *in.URL
	}
	if in.Events != nil {
		ev = *in.Events
	}
	if err := a.checkWebhook(u, ev); err != nil {
		fail(w, err)
		return
	}
	h, err := a.S.UpdateWebhook(r.Context(), org, id, func(h *store.Webhook) error {
		h.URL, h.Events = u, ev
		if in.Enabled != nil {
			if *in.Enabled && !h.Enabled {
				h.Failures = 0 // re-enabling starts a fresh failure count
			}
			h.Enabled = *in.Enabled
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "webhook.update", id, fmt.Sprintf("host=%s enabled=%v", hostOf(h.URL), h.Enabled))
	writeJSON(w, 200, h)
}

func (a *API) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("id")
	if err := a.S.DeleteWebhook(r.Context(), org, id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "webhook.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// testWebhook sends one signed test.ping now. Only the receiver's status code is reported: its
// body is never read back to the tenant.
func (a *API) testWebhook(w http.ResponseWriter, r *http.Request) {
	if a.Hooks == nil {
		writeErr(w, http.StatusServiceUnavailable, "webhooks are not enabled")
		return
	}
	org, id := r.PathValue("org"), r.PathValue("id")
	h, err := a.S.GetWebhook(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	status, err := a.Hooks.Send(r.Context(), *h, webhook.NewEvent(org, webhook.TestPing, map[string]any{"webhook_id": id}))
	out := map[string]any{"ok": err == nil && status >= 200 && status < 300, "status": status}
	switch {
	case errors.Is(err, gateway.ErrPrivateAddress):
		out["error"] = "url does not resolve to a public address"
	case err != nil && webhook.CheckURL(h.URL, a.cfg.AllowPrivateURLs) != nil:
		out["error"] = "url is not allowed"
	case err != nil:
		out["error"] = "delivery failed (connection error or timeout)"
	}
	writeJSON(w, 200, out)
}

// checkSemanticCache validates and fills defaults; an empty model turns the cache off (nil).
func (a *API) checkSemanticCache(org string, sc *store.SemanticCache) (*store.SemanticCache, error) {
	if sc.Model == "" {
		return nil, nil
	}
	if m, ok := a.G.Snapshot().Model(org, sc.Model); !ok || m.Kind != "embedding" {
		return nil, fmt.Errorf("%w: semantic_cache.model must be an embedding model available to the organization", store.ErrInvalid)
	}
	out := *sc
	if out.Threshold == 0 {
		out.Threshold = gateway.SemDefaultThresh
	}
	if !(out.Threshold >= 0.5 && out.Threshold <= 0.999) {
		return nil, fmt.Errorf("%w: semantic_cache.threshold must be between 0.5 and 0.999", store.ErrInvalid)
	}
	if out.TTLSeconds == 0 {
		out.TTLSeconds = gateway.SemDefaultTTL
	}
	if out.TTLSeconds < 1 || out.TTLSeconds > gateway.SemMaxTTL {
		return nil, fmt.Errorf("%w: semantic_cache.ttl_seconds must be between 1 and %d", store.ErrInvalid, gateway.SemMaxTTL)
	}
	return &out, nil
}
