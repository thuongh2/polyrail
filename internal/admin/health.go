package admin

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
)

// Public provider health (no auth): built from the hourly health/<hour>/<model>|<provider>
// counters the gateway records per attempted target. Only public models and providers are
// shown, by name; provider ids, keys and base URLs never leave this file.

type healthStats struct {
	Requests      int64    `json:"requests"`
	Errors        int64    `json:"errors"`
	Uptime        *float64 `json:"uptime"`         // successful / attempts; null without traffic
	AvgLatencyMs  int64    `json:"avg_latency_ms"` // over successful attempts
	ThroughputTPS *float64 `json:"throughput_tps"` // output tokens per second of successful latency
}

type hourStats struct {
	Hour         string `json:"hour"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
	AvgLatencyMs int64  `json:"avg_latency_ms"`
}

func round(x float64, places int) *float64 {
	p := math.Pow(10, float64(places))
	v := math.Round(x*p) / p
	return &v
}

func statsOf(a store.Agg) healthStats {
	s := healthStats{Requests: a[store.AggRequests], Errors: a[store.AggErrors]}
	if ok := s.Requests - s.Errors; s.Requests > 0 {
		s.Uptime = round(float64(ok)/float64(s.Requests), 4)
		if ok > 0 {
			s.AvgLatencyMs = a[store.AggLatencyMs] / ok
		}
	}
	if lat := a[store.AggLatencyMs]; lat > 0 {
		s.ThroughputTPS = round(float64(a[store.AggOutTokens])/(float64(lat)/1000), 2)
	}
	return s
}

func healthStatus(s healthStats, open bool) string {
	switch {
	case s.Requests == 0:
		return "no_data"
	case open || *s.Uptime < 0.9:
		return "down"
	case *s.Uptime < 0.99:
		return "degraded"
	}
	return "operational"
}

// healthHours reads the last n hourly buckets, oldest first. model "" reads every pair.
// Each bucket maps "<model>|<providerID>" to its counters.
func (a *API) healthHours(ctx context.Context, n int, model string, now time.Time) ([]time.Time, []map[string]store.Agg, error) {
	hours, out := make([]time.Time, n), make([]map[string]store.Agg, n)
	cur := now.UTC().Truncate(time.Hour)
	for i := range n {
		h := cur.Add(-time.Duration(n-1-i) * time.Hour)
		prefix := "health/" + usage.HealthHour(h) + "/"
		scan := prefix
		if model != "" {
			scan += model + "|"
		}
		m, err := a.S.KV.ScanAgg(ctx, scan)
		if err != nil {
			return nil, nil, err
		}
		bucket := make(map[string]store.Agg, len(m))
		for k, v := range m {
			bucket[strings.TrimPrefix(k, prefix)] = v
		}
		hours[i], out[i] = h, bucket
	}
	return hours, out, nil
}

func publicHeaders(w http.ResponseWriter) { w.Header().Set("Cache-Control", "public, max-age=30") }

type providerHealth struct {
	Name          string      `json:"name"`
	Type          string      `json:"type"`
	UpstreamModel string      `json:"upstream_model"`
	Weight        float64     `json:"weight"`
	Status        string      `json:"status"`
	Stats         healthStats `json:"stats_24h"`
	Hourly        []hourStats `json:"hourly"`
}

// GET /api/public/models/{id...}: a public model with per-provider health (24 h stats, 72 h hourly).
func (a *API) publicModel(w http.ResponseWriter, r *http.Request) {
	snap := a.G.Snapshot()
	m, ok := snap.Models["_|"+r.PathValue("id")]
	if !ok || !m.Enabled || m.OrgID != "" {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	hours, buckets, err := a.healthHours(r.Context(), 72, m.ID, time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	var total store.Agg
	provs := []providerHealth{}
	for _, t := range m.Targets {
		p, ok := snap.Providers[t.ProviderID]
		if !ok || p.OrgID != "" {
			continue
		}
		key := m.ID + "|" + p.ID
		var day store.Agg
		hourly := make([]hourStats, len(hours))
		for i, h := range hours {
			b := buckets[i][key]
			hs := hourStats{Hour: h.Format(time.RFC3339), Requests: b[store.AggRequests], Errors: b[store.AggErrors]}
			if ok := hs.Requests - hs.Errors; ok > 0 {
				hs.AvgLatencyMs = b[store.AggLatencyMs] / ok
			}
			hourly[i] = hs
			if i >= len(hours)-24 {
				day.Add(b)
			}
		}
		total.Add(day)
		st := statsOf(day)
		provs = append(provs, providerHealth{Name: p.Name, Type: p.Type, UpstreamModel: t.Model, Weight: t.Weight,
			Status: healthStatus(st, a.G.TargetOpen(p.ID, t.Model)), Stats: st, Hourly: hourly})
	}
	publicHeaders(w)
	writeJSON(w, 200, map[string]any{"model": snap.Info(m), "stats_24h": statsOf(total), "providers": provs})
}

// GET /api/public/status: every public provider's health over its public models, last 24 h.
func (a *API) publicStatus(w http.ResponseWriter, r *http.Request) {
	snap := a.G.Snapshot()
	now := time.Now()
	_, buckets, err := a.healthHours(r.Context(), 24, "", now)
	if err != nil {
		fail(w, err)
		return
	}
	type acc struct {
		models, open, targets int
		agg                   store.Agg
	}
	byProv := map[string]*acc{}
	for id, p := range snap.Providers {
		if p.OrgID == "" {
			byProv[id] = &acc{}
		}
	}
	for _, m := range snap.Models {
		if m.OrgID != "" || !m.Enabled {
			continue
		}
		seen := map[string]bool{}
		for _, t := range m.Targets {
			c, ok := byProv[t.ProviderID]
			if !ok {
				continue
			}
			c.targets++
			if a.G.TargetOpen(t.ProviderID, t.Model) {
				c.open++
			}
			if !seen[t.ProviderID] {
				seen[t.ProviderID] = true
				c.models++
				for _, b := range buckets {
					c.agg.Add(b[m.ID+"|"+t.ProviderID])
				}
			}
		}
	}
	type provStatus struct {
		Name   string      `json:"name"`
		Type   string      `json:"type"`
		Status string      `json:"status"`
		Models int         `json:"models"`
		Stats  healthStats `json:"stats_24h"`
	}
	out := make([]provStatus, 0, len(byProv))
	for id, c := range byProv {
		p, st := snap.Providers[id], statsOf(c.agg)
		out = append(out, provStatus{Name: p.Name, Type: p.Type, Models: c.models, Stats: st,
			Status: healthStatus(st, c.targets > 0 && c.open == c.targets)}) // down when every target's circuit is open
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	publicHeaders(w)
	writeJSON(w, 200, map[string]any{"providers": out, "updated_at": now.UTC().Format(time.RFC3339)})
}
