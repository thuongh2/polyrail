package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ProviderPrefs is OpenRouter's request-level `provider` object (field names as theirs), also
// saved on presets. The gateway applies it per request (internal/gateway/reqroute.go).
type ProviderPrefs struct {
	Order                  []string    `json:"order,omitempty"`  // provider names or types, tried first in this order
	Only                   []string    `json:"only,omitempty"`   // keep only these provider names or types
	Ignore                 []string    `json:"ignore,omitempty"` // drop these provider names or types
	AllowFallbacks         *bool       `json:"allow_fallbacks,omitempty"`
	RequireParameters      bool        `json:"require_parameters,omitempty"`
	Sort                   *SortPref   `json:"sort,omitempty"`
	MaxPrice               *MaxPrice   `json:"max_price,omitempty"`
	PreferredMinThroughput *Percentile `json:"preferred_min_throughput,omitempty"` // tokens/s
	PreferredMaxLatency    *Percentile `json:"preferred_max_latency,omitempty"`    // seconds
	Quantizations          []string    `json:"quantizations,omitempty"`            // accepted, not used (no target metadata)
	DataCollection         string      `json:"data_collection,omitempty"`          // "" | "allow" | "deny"
	ZDR                    *bool       `json:"zdr,omitempty"`
}

// SortPref is `sort`: a string ("price" | "throughput" | "latency") or {by, partition}.
type SortPref struct {
	By        string `json:"by"`
	Partition string `json:"partition,omitempty"` // "" or "model" (sort within each model) | "none" (across models)
}

func (s *SortPref) UnmarshalJSON(b []byte) error {
	var by string
	if err := json.Unmarshal(b, &by); err == nil {
		*s = SortPref{By: by}
		return nil
	}
	type plain SortPref
	return json.Unmarshal(b, (*plain)(s))
}

// MaxPrice caps prices in USD: prompt and completion per 1M tokens, request per request, image per image.
type MaxPrice struct {
	Prompt     *float64 `json:"prompt,omitempty"`
	Completion *float64 `json:"completion,omitempty"`
	Request    *float64 `json:"request,omitempty"`
	Image      *float64 `json:"image,omitempty"`
}

// Percentile is a number, or OpenRouter's percentile object of which p50 is used (other
// percentiles are accepted and ignored: the gateway keeps one EWMA per target, not a histogram).
type Percentile float64

func (p *Percentile) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		*p = Percentile(f)
		return nil
	}
	var obj struct {
		P50 *float64 `json:"p50"`
		P75 *float64 `json:"p75"`
		P90 *float64 `json:"p90"`
		P99 *float64 `json:"p99"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return errors.New("must be a number or an object of p50/p75/p90/p99 numbers")
	}
	*p = 0
	if obj.P50 != nil {
		*p = Percentile(*obj.P50)
	}
	return nil
}

var sortKeys = []string{"price", "throughput", "latency"}

// Validate checks values; field names and types are checked by the JSON decoder.
func (pp *ProviderPrefs) Validate() error {
	for name, l := range map[string][]string{"order": pp.Order, "only": pp.Only, "ignore": pp.Ignore, "quantizations": pp.Quantizations} {
		if len(l) > 50 {
			return fmt.Errorf("%w: provider.%s has more than 50 entries", ErrInvalid, name)
		}
		if slices.Contains(l, "") {
			return fmt.Errorf("%w: provider.%s has an empty entry", ErrInvalid, name)
		}
	}
	if s := pp.Sort; s != nil {
		if !slices.Contains(sortKeys, s.By) {
			return fmt.Errorf("%w: provider.sort must be one of price, throughput, latency", ErrInvalid)
		}
		if s.Partition != "" && s.Partition != "model" && s.Partition != "none" {
			return fmt.Errorf("%w: provider.sort.partition must be model or none", ErrInvalid)
		}
	}
	if mp := pp.MaxPrice; mp != nil {
		for _, v := range []*float64{mp.Prompt, mp.Completion, mp.Request, mp.Image} {
			if v != nil && (*v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0)) {
				return fmt.Errorf("%w: provider.max_price values must be numbers >= 0", ErrInvalid)
			}
		}
	}
	for _, v := range []*Percentile{pp.PreferredMinThroughput, pp.PreferredMaxLatency} {
		if v != nil && *v < 0 {
			return fmt.Errorf("%w: provider.preferred_* values must be >= 0", ErrInvalid)
		}
	}
	if d := pp.DataCollection; d != "" && d != "allow" && d != "deny" {
		return fmt.Errorf("%w: provider.data_collection must be allow or deny", ErrInvalid)
	}
	return nil
}

// ParseProviderPrefs decodes and validates a `provider` object; unknown fields are rejected so a
// misspelled filter never silently routes somewhere the caller excluded.
func ParseProviderPrefs(raw []byte) (*ProviderPrefs, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var pp ProviderPrefs
	if err := dec.Decode(&pp); err != nil {
		return nil, fmt.Errorf("%w: provider: %v", ErrInvalid, err)
	}
	if err := pp.Validate(); err != nil {
		return nil, err
	}
	return &pp, nil
}

// MaxRouteModels caps a request's or preset's `models` list.
const MaxRouteModels = 5

// Preset is a saved, org-scoped request template used as "model": "@preset/<slug>".
type Preset struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
	// WorkspaceID: only keys of this workspace resolve the preset ("" = default, D31).
	// shortcut: slugs stay unique per org; key presets by workspace if teams collide.
	WorkspaceID string         `json:"workspace_id,omitempty"`
	Slug        string         `json:"slug"`
	Model       string         `json:"model,omitempty"`
	Models      []string       `json:"models,omitempty"`
	Provider    *ProviderPrefs `json:"provider,omitempty"`
	Params      map[string]any `json:"params,omitempty"` // see PresetParams; request fields win
	CreatedAt   time.Time      `json:"created_at"`
}

var reSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// PresetParams are the request parameters a preset may set. "system" is a system prompt
// prepended to chat messages (and Responses instructions); the rest are the OpenAI fields.
var PresetParams = map[string]string{
	"temperature": "number", "top_p": "number", "frequency_penalty": "number", "presence_penalty": "number",
	"max_tokens": "int", "seed": "int", "stop": "stop", "system": "string",
}

// MaxPresetsPerOrg bounds the presets kept in every node's catalog snapshot.
const MaxPresetsPerOrg = 200

func (p *Preset) validate() error {
	if !reSlug.MatchString(p.Slug) {
		return fmt.Errorf("%w: slug must match ^[a-z0-9][a-z0-9-]{0,62}$", ErrInvalid)
	}
	if p.Model == "" && len(p.Models) == 0 {
		return fmt.Errorf("%w: model or models required", ErrInvalid)
	}
	if len(p.Models) > MaxRouteModels || strings.HasPrefix(p.Model, "@preset/") || slices.ContainsFunc(p.Models, func(m string) bool { return m == "" || strings.HasPrefix(m, "@preset/") }) {
		return fmt.Errorf("%w: models: at most %d model ids, none of them a preset", ErrInvalid, MaxRouteModels)
	}
	if p.Provider != nil {
		if err := p.Provider.Validate(); err != nil {
			return err
		}
	}
	for k, v := range p.Params {
		ok := false
		switch PresetParams[k] {
		case "number":
			_, ok = v.(float64)
		case "int":
			f, isNum := v.(float64)
			ok = isNum && f == math.Trunc(f) && f >= 0
		case "string":
			s, isStr := v.(string)
			ok = isStr && len(s) <= 32<<10
		case "stop":
			switch s := v.(type) {
			case string:
				ok = true
			case []any:
				ok = len(s) <= 4 && !slices.ContainsFunc(s, func(x any) bool { _, isStr := x.(string); return !isStr })
			}
		default:
			return fmt.Errorf("%w: params.%s is not a preset parameter", ErrInvalid, k)
		}
		if !ok {
			return fmt.Errorf("%w: params.%s has the wrong type", ErrInvalid, k)
		}
	}
	return nil
}

func presetKey(orgID, slug string) string { return "preset/" + orgID + "/" + slug }

// PutPreset creates or replaces the org's preset with p.Slug (keeping ID and CreatedAt).
// create=true fails with ErrExists when the slug is taken.
func (s *Store) PutPreset(ctx context.Context, p *Preset, create bool) error {
	if err := p.validate(); err != nil {
		return err
	}
	have, err := s.KV.Scan(ctx, presetKey(p.OrgID, ""), "", MaxPresetsPerOrg)
	if err != nil {
		return err
	}
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + p.OrgID); err != nil {
			return err
		}
		old, err := getJSON[Preset](tx.Get, presetKey(p.OrgID, p.Slug))
		switch {
		case err == nil && create:
			return ErrExists
		case err == nil:
			p.ID, p.CreatedAt = old.ID, old.CreatedAt
		case errors.Is(err, ErrNotFound) && !create:
			return ErrNotFound
		case errors.Is(err, ErrNotFound):
			if len(have) >= MaxPresetsPerOrg { // shortcut: counted outside the transaction, a soft cap
				return fmt.Errorf("%w: at most %d presets per organization", ErrInvalid, MaxPresetsPerOrg)
			}
			p.ID, p.CreatedAt = NewID(), time.Now().UTC()
		default:
			return err
		}
		return putJSON(tx, presetKey(p.OrgID, p.Slug), p)
	})
}

func (s *Store) GetPreset(ctx context.Context, orgID, slug string) (*Preset, error) {
	return getJSON[Preset](s.get(ctx), presetKey(orgID, slug))
}

// ListPresets returns an org's presets by slug; orgID "*" returns every org's (catalog load).
func (s *Store) ListPresets(ctx context.Context, orgID string) ([]Preset, error) {
	prefix := "preset/"
	if orgID != "*" {
		prefix = presetKey(orgID, "")
	}
	return listJSON[Preset](ctx, s.KV, prefix, "", 0)
}

func (s *Store) DeletePreset(ctx context.Context, orgID, slug string) error {
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get(presetKey(orgID, slug)); err != nil {
			return err
		}
		return tx.Delete(presetKey(orgID, slug))
	})
}
