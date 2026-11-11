package admin

// Model import: discover lists a provider's upstream models through Bifrost core's
// ListModelsRequest (with the provider's own keys); import creates catalog models routed to it.
// Platform scope (/api/admin/providers/{id}/...) creates public models; org scope
// (/api/orgs/{org}/providers/{id}/...) creates that org's private models from its own providers.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

type discoveredModel struct {
	Upstream      string `json:"upstream"`
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	ContextLength int    `json:"context_length,omitempty"`
	MaxOutput     int    `json:"max_output_tokens,omitempty"`
	// Upstream list prices (OpenRouter-style providers), micro-USD per 1M tokens.
	InputPrice  *int64 `json:"input_price_micros,omitempty"`
	OutputPrice *int64 `json:"output_price_micros,omitempty"`
	CacheRead   *int64 `json:"cache_read_price_micros,omitempty"`
	CacheWrite  *int64 `json:"cache_write_price_micros,omitempty"`
}

// ownedProvider loads {id} if it belongs to the route's owner (platform or {org}).
func (a *API) ownedProvider(w http.ResponseWriter, r *http.Request) (*store.Provider, bool) {
	p, err := a.S.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil || p.OrgID != owner(r) {
		fail(w, store.ErrNotFound)
		return nil, false
	}
	return p, true
}

// perTokenMicros converts an upstream price in USD per token to micro-USD per 1M tokens.
func perTokenMicros(s *string) *int64 {
	if s == nil {
		return nil
	}
	v, err := strconv.ParseFloat(*s, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) { // "-1" means variable pricing
		return nil
	}
	m := int64(math.Round(v * 1e12))
	return &m
}

func (a *API) discoverModels(w http.ResponseWriter, r *http.Request) {
	p, ok := a.ownedProvider(w, r)
	if !ok {
		return
	}
	if p.OrgID != "" { // re-check at request time: DNS may have changed since the provider was saved
		if err := checkBaseURL(p.BaseURL, a.cfg.AllowPrivateURLs); err != nil {
			fail(w, err)
			return
		}
		if !a.cfg.AllowPrivateURLs && p.BaseURL != "" && gateway.FollowsUpstreamURLs(p.Type) { // saved before the rule existed
			fail(w, fmt.Errorf("%w: org %s providers use the vendor's API; leave base_url empty", store.ErrInvalid, p.Type))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.G.Reload(ctx); err != nil {
		fail(w, err)
		return
	}
	if _, loaded := a.G.Snapshot().Providers[p.ID]; !loaded {
		writeErr(w, http.StatusConflict, "provider is disabled or another provider of this type is active")
		return
	}
	key := gateway.BifrostKey(*p)
	out := []discoveredModel{}
	req := &schemas.BifrostListModelsRequest{Provider: key}
	for page := 0; page < 20; page++ { // shortcut: at most 20 pages; enough for every listed provider today
		resp, berr := a.G.BF.ListModelsRequest(schemas.NewBifrostContext(ctx, schemas.NoDeadline), req)
		if berr != nil {
			status := http.StatusBadGateway
			if berr.StatusCode != nil {
				status = *berr.StatusCode
			}
			msg := fmt.Sprintf("upstream provider returned an error (status %d)", status)
			if p.OrgID == "" && berr.Error != nil && berr.Error.Message != "" { // tenants never read upstream bodies (SSRF)
				msg = "upstream: " + berr.Error.Message
			}
			writeErr(w, http.StatusBadGateway, msg)
			return
		}
		for _, m := range resp.Data {
			d := discoveredModel{Upstream: strings.TrimPrefix(m.ID, string(key)+"/")}
			if m.Name != nil {
				d.Name = *m.Name
			}
			if m.Description != nil {
				d.Description = *m.Description
			}
			if m.ContextLength != nil {
				d.ContextLength = *m.ContextLength
			} else if m.MaxInputTokens != nil {
				d.ContextLength = *m.MaxInputTokens
			}
			if m.MaxOutputTokens != nil {
				d.MaxOutput = *m.MaxOutputTokens
			}
			if m.Pricing != nil {
				d.InputPrice, d.OutputPrice = perTokenMicros(m.Pricing.Prompt), perTokenMicros(m.Pricing.Completion)
				d.CacheRead, d.CacheWrite = perTokenMicros(m.Pricing.InputCacheRead), perTokenMicros(m.Pricing.InputCacheWrite)
			}
			out = append(out, d)
		}
		if resp.NextPageToken == "" {
			break
		}
		req.PageToken = resp.NextPageToken
	}
	writeJSON(w, 200, map[string]any{"provider": p.ID, "models": out})
}

type importModel struct {
	Upstream      string `json:"upstream"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Kind          string `json:"kind"`
	ContextLength int    `json:"context_length"`
	MaxOutput     int    `json:"max_output_tokens"`
	InputPrice    int64  `json:"input_price_micros"`
	OutputPrice   int64  `json:"output_price_micros"`
	CacheRead     int64  `json:"cache_read_price_micros"`
	CacheWrite    int64  `json:"cache_write_price_micros"`
}

func (a *API) importModels(w http.ResponseWriter, r *http.Request) {
	p, ok := a.ownedProvider(w, r)
	if !ok {
		return
	}
	var body struct {
		Models []importModel `json:"models"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Models) == 0 || len(body.Models) > 500 {
		fail(w, fmt.Errorf("%w: models must list 1 to 500 entries", store.ErrInvalid))
		return
	}
	ctx := r.Context()
	seen := map[string]bool{}
	for _, m := range body.Models { // validate everything before writing anything
		switch {
		case !reModelID.MatchString(m.ID) || strings.Contains(m.ID, ".."):
			fail(w, fmt.Errorf("%w: model id %q must look like vendor/name (letters, digits, . _ : - /)", store.ErrInvalid, m.ID))
			return
		case m.Upstream == "" || len(m.Upstream) > 256:
			fail(w, fmt.Errorf("%w: model %q needs an upstream id (at most 256 characters)", store.ErrInvalid, m.ID))
			return
		case m.InputPrice < 0 || m.OutputPrice < 0 || m.CacheRead < 0 || m.CacheWrite < 0 || m.ContextLength < 0 || m.MaxOutput < 0:
			fail(w, fmt.Errorf("%w: model %q: prices and limits must be >= 0", store.ErrInvalid, m.ID))
			return
		case seen[m.ID]:
			fail(w, fmt.Errorf("%w: model id %q listed twice", store.ErrInvalid, m.ID))
			return
		}
		seen[m.ID] = true
		if _, err := a.S.GetModel(ctx, p.OrgID, m.ID); err == nil {
			writeErr(w, http.StatusConflict, fmt.Sprintf("model %q already exists; edit it instead", m.ID))
			return
		}
	}
	created := make([]string, 0, len(body.Models))
	for _, im := range body.Models {
		name := im.Name
		if name == "" {
			name = im.ID
		}
		m := store.Model{ID: im.ID, OrgID: p.OrgID, Name: name, Description: im.Description, Kind: im.Kind,
			ContextLength: im.ContextLength, MaxOutput: im.MaxOutput, InputPrice: im.InputPrice, OutputPrice: im.OutputPrice,
			CacheReadPrice: im.CacheRead, CacheWritePrice: im.CacheWrite, Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: im.Upstream, Weight: 1}}}
		if err := a.S.PutModel(ctx, &m); err != nil {
			for _, id := range created { // undo, so a failed import leaves the catalog as it was
				_ = a.S.DeleteModel(ctx, p.OrgID, id)
			}
			fail(w, err)
			return
		}
		created = append(created, m.ID)
	}
	for _, im := range body.Models {
		a.audit(r, r.PathValue("org"), "model.import", im.ID, fmt.Sprintf("provider=%s upstream=%s in=%d out=%d micros/1M", p.ID, im.Upstream, im.InputPrice, im.OutputPrice))
	}
	writeJSON(w, 200, map[string]any{"created": created})
}
