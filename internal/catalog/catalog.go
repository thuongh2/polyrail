// Package catalog embeds the default production model catalog, generated from models.dev (MIT,
// see LICENSE.models.dev) by scripts/gencatalog.py, and writes it into a store: one disabled,
// keyless platform provider per type ("cat-<type>") and every model routed to those providers.
// Models stay hidden until an operator adds a key and enables a provider (gateway visibility rule).
package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/polyrail/polyrail/internal/store"
)

//go:embed catalog.json
var raw []byte

type Provider struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Docs string `json:"docs"`
}

type Target struct {
	Type  string `json:"type"`
	Model string `json:"model"`
}

type Model struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	Description      string   `json:"description"`
	ContextLength    int      `json:"context_length"`
	MaxOutput        int      `json:"max_output_tokens"`
	Modalities       []string `json:"modalities"`
	InputPrice       int64    `json:"input_price_micros"`
	OutputPrice      int64    `json:"output_price_micros"`
	AudioMinutePrice int64    `json:"audio_minute_price_micros"`
	ImagePrice       int64    `json:"image_price_micros"`
	VideoSecondPrice int64    `json:"video_second_price_micros"`
	Targets          []Target `json:"targets"`
}

type Catalog struct {
	Source    string     `json:"source"`
	Providers []Provider `json:"providers"`
	Models    []Model    `json:"models"`
}

// Load parses the embedded catalog.
func Load() (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("embedded catalog: %w", err)
	}
	return &c, nil
}

// ProviderID is the deterministic id of the catalog provider of a type, so concurrent or repeated
// seeding on several nodes writes the same records instead of duplicates.
func ProviderID(typ string) string { return "cat-" + typ }

// Sync adds the catalog providers and public models missing from s; existing records are never
// changed. Providers are created disabled with no keys.
func Sync(ctx context.Context, s *store.Store) (providers, models int, err error) {
	c, err := Load()
	if err != nil {
		return 0, 0, err
	}
	for _, p := range c.Providers {
		if err := s.CreateKeylessProvider(ctx, &store.Provider{ID: ProviderID(p.Type), Name: p.Type, Type: p.Type}); err != nil {
			if errors.Is(err, store.ErrExists) { // already there (an earlier sync, or another node)
				continue
			}
			return providers, models, err
		}
		providers++
	}
	have, err := s.ListModels(ctx, "")
	if err != nil {
		return providers, models, err
	}
	exists := make(map[string]bool, len(have))
	for _, m := range have {
		exists[m.ID] = true
	}
	for _, cm := range c.Models {
		if exists[cm.ID] {
			continue
		}
		m := store.Model{ID: cm.ID, Name: cm.Name, Description: cm.Description, Kind: cm.Kind,
			ContextLength: cm.ContextLength, MaxOutput: cm.MaxOutput, Modalities: cm.Modalities,
			InputPrice: cm.InputPrice, OutputPrice: cm.OutputPrice, AudioMinutePrice: cm.AudioMinutePrice,
			ImagePrice: cm.ImagePrice, VideoSecondPrice: cm.VideoSecondPrice, Enabled: true}
		for _, t := range cm.Targets {
			m.Targets = append(m.Targets, store.Target{ProviderID: ProviderID(t.Type), Model: t.Model, Weight: 1})
		}
		if len(m.Targets) > 1 {
			m.Strategy = "priority" // first-party first, then the cheapest; the others are fallbacks
		}
		if err := s.PutModel(ctx, &m); err != nil {
			return providers, models, fmt.Errorf("catalog model %s: %w", cm.ID, err)
		}
		models++
	}
	return providers, models, nil
}
