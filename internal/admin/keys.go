package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

// Provider key management (docs/KEYS.md): list with status and 24 h stats, bulk add, patch,
// delete, bulk actions, pinned test and hourly stats, for platform providers (/api/admin) and
// org providers (/api/orgs/{org}, admin role). Key values never leave as more than MaskKey.

const (
	maxKeysPerAdd   = 1000
	maxBulkIDs      = 1000
	maxBulkTests    = 100
	bulkTestWorkers = 8
	maxKeyLen       = 4096
)

// cloudKeyTypes take per-key config (endpoint, region, account) that bulk add cannot express.
var cloudKeyTypes = map[string]bool{"azure": true, "bedrock": true, "bedrock_mantle": true, "sagemaker": true,
	"vertex": true, "cloudflare": true}

func (a *API) registerKeys(mux *http.ServeMux) {
	for _, s := range []struct {
		prefix string
		wrap   func(http.HandlerFunc) http.HandlerFunc
	}{
		{"/api/orgs/{org}", func(h http.HandlerFunc) http.HandlerFunc { return a.org(store.RoleAdmin, h) }},
		{"/api/admin", func(h http.HandlerFunc) http.HandlerFunc { return a.operator(a.platform(h)) }},
	} {
		p := s.prefix + "/providers/{id}/keys"
		mux.HandleFunc("GET "+p, s.wrap(a.listProviderKeys))
		mux.HandleFunc("POST "+p, s.wrap(a.addProviderKeys))
		mux.HandleFunc("POST "+p+"/bulk", s.wrap(a.bulkProviderKeys))
		mux.HandleFunc("PATCH "+p+"/{kid}", s.wrap(a.patchProviderKey))
		mux.HandleFunc("DELETE "+p+"/{kid}", s.wrap(a.deleteProviderKey))
		mux.HandleFunc("POST "+p+"/{kid}/test", s.wrap(a.testProviderKey))
		mux.HandleFunc("GET "+p+"/{kid}/stats", s.wrap(a.providerKeyStats))
	}
}

type keyStats24h struct {
	Requests     int64 `json:"requests"`
	Errors       int64 `json:"errors"`
	AvgLatencyMs int64 `json:"avg_latency_ms"` // over successful attempts
}

type keyView struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Masked        string      `json:"masked"`
	Weight        float64     `json:"weight"`
	Models        []string    `json:"models"`
	Enabled       bool        `json:"enabled"`
	Status        string      `json:"status"`
	StatusReason  string      `json:"status_reason,omitempty"`
	CooldownUntil *time.Time  `json:"cooldown_until"`
	LastUsedAt    *time.Time  `json:"last_used_at"`
	Stats24h      keyStats24h `json:"stats_24h"`
}

// providerSecrets returns {id} with secrets decrypted when it belongs to the route's owner.
func (a *API) providerSecrets(r *http.Request) (*store.Provider, error) {
	p, err := a.S.GetProviderSecrets(r.Context(), r.PathValue("id"))
	if err != nil || p.OrgID != owner(r) {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func avgLatency(a store.Agg) int64 {
	if ok := a[store.AggRequests] - a[store.AggErrors]; ok > 0 {
		return a[store.AggLatencyMs] / ok
	}
	return 0
}

// keyHours reads provider pid's per-key counters for the last n hours, oldest first.
func (a *API) keyHours(ctx context.Context, pid string, n int, now time.Time) ([]time.Time, []map[string]store.Agg, error) {
	hours, out := make([]time.Time, n), make([]map[string]store.Agg, n)
	cur := now.UTC().Truncate(time.Hour)
	for i := range n {
		h := cur.Add(-time.Duration(n-1-i) * time.Hour)
		prefix := usage.KeyStatPrefix(pid, h)
		m, err := a.S.KV.ScanAgg(ctx, prefix)
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

// viewKeys builds the key list of p (secrets decrypted) with status and the last 24 h of stats.
// last_used_at is this node's last request on the key, else the start of the latest hour with
// recorded traffic in the window.
func (a *API) viewKeys(ctx context.Context, p *store.Provider) ([]keyView, error) {
	hours, buckets, err := a.keyHours(ctx, p.ID, 24, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]keyView, 0, len(p.Keys))
	for _, k := range p.Keys {
		v := keyView{ID: k.ID, Name: k.Name, Weight: k.Weight, Models: k.Models, Enabled: !k.Disabled, StatusReason: k.StatusReason}
		if v.Models == nil {
			v.Models = []string{}
		}
		if v.Weight <= 0 {
			v.Weight = 1 // routing treats an unset weight as 1 (gateway catalog)
		}
		switch {
		case k.Value != "":
			v.Masked = gateway.MaskKey(k.Value)
		case k.Name != "": // cloud keys (endpoint/region/IAM): the list shows the key name
			v.Masked = k.Name
		default:
			v.Masked = gateway.MaskKey(k.ID)
		}
		v.Status, v.CooldownUntil = a.G.KeyStatus(k)
		if !k.Quarantined {
			v.StatusReason = ""
		}
		var sum store.Agg
		var lastHour time.Time
		for i, b := range buckets {
			if s, ok := b[k.ID]; ok {
				sum.Add(s)
				if s[store.AggRequests] > 0 {
					lastHour = hours[i]
				}
			}
		}
		v.Stats24h = keyStats24h{Requests: sum[store.AggRequests], Errors: sum[store.AggErrors], AvgLatencyMs: avgLatency(sum)}
		if t := a.G.KeyLastUsed(k.ID); !t.IsZero() {
			t = t.UTC()
			v.LastUsedAt = &t
		} else if !lastHour.IsZero() {
			v.LastUsedAt = &lastHour
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *API) listProviderKeys(w http.ResponseWriter, r *http.Request) {
	p, err := a.providerSecrets(r)
	if err != nil {
		fail(w, err)
		return
	}
	out, err := a.viewKeys(r.Context(), p)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// splitKeys parses the "keys" field: one string (separated by newlines or commas) or an array.
func splitKeys(raw json.RawMessage) ([]string, error) {
	var list []string
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		list = strings.FieldsFunc(one, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%w: keys must be a string or an array of strings", store.ErrInvalid)
	}
	out := list[:0]
	for _, k := range list {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out, nil
}

func validWeight(w float64) bool { return w >= 0 && w <= 1e6 && !math.IsNaN(w) && !math.IsInf(w, 0) }

func checkKeyModels(models []string) error {
	if len(models) > 1000 {
		return fmt.Errorf("%w: at most 1000 models per key", store.ErrInvalid)
	}
	for _, m := range models {
		if m == "" || len(m) > 200 {
			return fmt.Errorf("%w: model names must be 1-200 characters", store.ErrInvalid)
		}
	}
	return nil
}

// reload applies a key change to this node's catalog now (other nodes follow on their poll).
func (a *API) reload(ctx context.Context) {
	if err := a.G.Reload(ctx); err != nil {
		slog.Error("catalog reload after key change failed", "err", err)
	}
}

func (a *API) addProviderKeys(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Keys   json.RawMessage `json:"keys"`
		Weight *float64        `json:"weight"`
		Models []string        `json:"models"`
	}
	if !decode(w, r, &in) {
		return
	}
	p, err := a.S.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil || p.OrgID != owner(r) {
		fail(w, store.ErrNotFound)
		return
	}
	if cloudKeyTypes[p.Type] {
		fail(w, fmt.Errorf("%w: %s keys carry per-key config; add them in the provider dialog", store.ErrInvalid, p.Type))
		return
	}
	if len(in.Keys) == 0 {
		fail(w, fmt.Errorf("%w: keys required", store.ErrInvalid))
		return
	}
	vals, err := splitKeys(in.Keys)
	if err != nil {
		fail(w, err)
		return
	}
	if len(vals) == 0 || len(vals) > maxKeysPerAdd {
		fail(w, fmt.Errorf("%w: send 1 to %d keys per call", store.ErrInvalid, maxKeysPerAdd))
		return
	}
	weight := 1.0
	if in.Weight != nil {
		if !validWeight(*in.Weight) {
			fail(w, fmt.Errorf("%w: weight must be between 0 and 1000000", store.ErrInvalid))
			return
		}
		weight = *in.Weight
	}
	if err := checkKeyModels(in.Models); err != nil {
		fail(w, err)
		return
	}
	invalid := 0
	keys := make([]store.ProviderKey, 0, len(vals))
	for _, v := range vals {
		if len(v) > maxKeyLen || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
			invalid++ // whitespace or control characters inside a value: not one key
			continue
		}
		keys = append(keys, store.ProviderKey{Value: v, Weight: weight, Models: in.Models})
	}
	added, dups := []store.ProviderKey{}, 0
	if len(keys) > 0 {
		if added, dups, err = a.S.AddProviderKeys(r.Context(), owner(r), p.ID, keys); err != nil {
			fail(w, err)
			return
		}
	}
	a.reload(r.Context())
	a.audit(r, r.PathValue("org"), "provider.keys.add", p.ID, fmt.Sprintf("added=%d duplicates=%d invalid=%d", len(added), dups, invalid))
	writeJSON(w, 200, map[string]int{"added": len(added), "duplicates": dups, "invalid": invalid})
}

// keyIndex finds key kid in p.
func keyIndex(p *store.Provider, kid string) int {
	return slices.IndexFunc(p.Keys, func(k store.ProviderKey) bool { return k.ID == kid })
}

func (a *API) patchProviderKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool     `json:"enabled"`
		Weight  *float64  `json:"weight"`
		Name    *string   `json:"name"`
		Models  *[]string `json:"models"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Weight != nil && !validWeight(*in.Weight) {
		fail(w, fmt.Errorf("%w: weight must be between 0 and 1000000", store.ErrInvalid))
		return
	}
	if in.Name != nil && len(*in.Name) > 100 {
		fail(w, fmt.Errorf("%w: name is at most 100 characters", store.ErrInvalid))
		return
	}
	if in.Models != nil {
		if err := checkKeyModels(*in.Models); err != nil {
			fail(w, err)
			return
		}
	}
	org, pid, kid := owner(r), r.PathValue("id"), r.PathValue("kid")
	wasQuarantined := false
	_, err := a.S.UpdateProviderKeys(r.Context(), org, pid, func(p *store.Provider) error {
		i := keyIndex(p, kid)
		if i < 0 {
			return store.ErrNotFound
		}
		k := &p.Keys[i]
		wasQuarantined = k.Quarantined
		if in.Enabled != nil {
			k.Disabled = !*in.Enabled
		}
		if in.Weight != nil {
			k.Weight = *in.Weight
		}
		if in.Name != nil {
			k.Name = strings.TrimSpace(*in.Name)
		}
		if in.Models != nil {
			k.Models = *in.Models
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	if in.Enabled != nil && *in.Enabled { // a manual re-enable also lifts a quarantine and cooldown
		a.G.ClearKey(kid)
		if wasQuarantined {
			if err := a.G.SetKeyQuarantine(r.Context(), org, pid, kid, false, ""); err != nil {
				fail(w, err)
				return
			}
		}
	}
	a.reload(r.Context())
	a.audit(r, r.PathValue("org"), "provider.key.update", pid+"/"+kid, patchDetail(in.Enabled, in.Weight, in.Name != nil, in.Models != nil))
	a.writeKey(w, r, kid)
}

func patchDetail(enabled *bool, weight *float64, name, models bool) string {
	var parts []string
	if enabled != nil {
		parts = append(parts, "enabled="+strconv.FormatBool(*enabled))
	}
	if weight != nil {
		parts = append(parts, "weight="+strconv.FormatFloat(*weight, 'f', -1, 64))
	}
	if name {
		parts = append(parts, "name")
	}
	if models {
		parts = append(parts, "models")
	}
	return strings.Join(parts, " ")
}

// writeKey answers with the current view of key kid.
func (a *API) writeKey(w http.ResponseWriter, r *http.Request, kid string) {
	p, err := a.providerSecrets(r)
	if err != nil {
		fail(w, err)
		return
	}
	vs, err := a.viewKeys(r.Context(), p)
	if err != nil {
		fail(w, err)
		return
	}
	for _, v := range vs {
		if v.ID == kid {
			writeJSON(w, 200, v)
			return
		}
	}
	fail(w, store.ErrNotFound)
}

// removeKeys deletes ids from p; cloud types keep at least one key (their config lives on keys).
func removeKeys(p *store.Provider, ids map[string]bool) error {
	keep := slices.DeleteFunc(slices.Clone(p.Keys), func(k store.ProviderKey) bool { return ids[k.ID] })
	if len(keep) == 0 && cloudKeyTypes[p.Type] {
		return fmt.Errorf("%w: %s needs at least one key", store.ErrInvalid, p.Type)
	}
	p.Keys = keep
	return nil
}

func (a *API) deleteProviderKey(w http.ResponseWriter, r *http.Request) {
	pid, kid := r.PathValue("id"), r.PathValue("kid")
	_, err := a.S.UpdateProviderKeys(r.Context(), owner(r), pid, func(p *store.Provider) error {
		if keyIndex(p, kid) < 0 {
			return store.ErrNotFound
		}
		return removeKeys(p, map[string]bool{kid: true})
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.G.ClearKey(kid)
	a.reload(r.Context())
	a.audit(r, r.PathValue("org"), "provider.key.delete", pid+"/"+kid, "")
	w.WriteHeader(http.StatusNoContent)
}

type bulkResult struct {
	ID        string `json:"id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Status    int    `json:"status,omitempty"`     // test only: upstream status
	LatencyMs int64  `json:"latency_ms,omitempty"` // test only
}

func (a *API) bulkProviderKeys(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	limit := maxBulkIDs
	if in.Action == "test" {
		limit = maxBulkTests
	}
	if len(in.IDs) == 0 || len(in.IDs) > limit {
		fail(w, fmt.Errorf("%w: send 1 to %d ids for %q", store.ErrInvalid, limit, in.Action))
		return
	}
	org, pid := owner(r), r.PathValue("id")
	p, err := a.S.GetProvider(r.Context(), pid)
	if err != nil || p.OrgID != org {
		fail(w, store.ErrNotFound)
		return
	}
	results := make([]bulkResult, len(in.IDs))
	known := map[string]bool{}
	for _, k := range p.Keys {
		known[k.ID] = true
	}
	ids := map[string]bool{}
	for i, id := range in.IDs {
		results[i] = bulkResult{ID: id, OK: known[id]}
		if !known[id] {
			results[i].Error = "not found"
			continue
		}
		ids[id] = true
	}
	switch in.Action {
	case "enable", "disable", "delete":
		if len(ids) > 0 {
			_, err = a.S.UpdateProviderKeys(r.Context(), org, pid, func(p *store.Provider) error {
				if in.Action == "delete" {
					return removeKeys(p, ids)
				}
				for i := range p.Keys {
					if ids[p.Keys[i].ID] {
						p.Keys[i].Disabled = in.Action == "disable"
					}
				}
				return nil
			})
			if err != nil {
				fail(w, err)
				return
			}
		}
		if in.Action == "delete" {
			for id := range ids {
				a.G.ClearKey(id)
			}
		}
	case "clear":
		for i := range results {
			if !results[i].OK {
				continue
			}
			a.G.ClearKey(results[i].ID)
			if err := a.G.SetKeyQuarantine(r.Context(), org, pid, results[i].ID, false, ""); err != nil {
				results[i].OK, results[i].Error = false, "could not clear status"
			}
		}
	case "test":
		if err := a.G.Reload(r.Context()); err != nil {
			fail(w, err)
			return
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, bulkTestWorkers)
		for i := range results {
			if !results[i].OK {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(res *bulkResult) {
				defer func() { <-sem; wg.Done() }()
				t, err := a.G.TestKey(r.Context(), pid, res.ID, "")
				if err != nil {
					res.OK, res.Error = false, testErr(err)
					return
				}
				res.OK, res.Status, res.LatencyMs, res.Error = t.OK, t.Status, t.LatencyMs, t.Error
			}(&results[i])
		}
		wg.Wait()
	default:
		fail(w, fmt.Errorf("%w: action must be enable, disable, delete, test or clear", store.ErrInvalid))
		return
	}
	a.reload(r.Context())
	if in.Action != "test" {
		a.audit(r, r.PathValue("org"), "provider.keys."+in.Action, pid, fmt.Sprintf("ids=%d", len(ids)))
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

// testErr is what a test reports for an error before any upstream call.
func testErr(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "not found"
	case errors.Is(err, store.ErrInvalid):
		return strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": ")
	}
	return "test failed"
}

func (a *API) testProviderKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model string `json:"model"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if len(in.Model) > 200 {
		fail(w, fmt.Errorf("%w: model is at most 200 characters", store.ErrInvalid))
		return
	}
	pid, kid := r.PathValue("id"), r.PathValue("kid")
	p, err := a.S.GetProvider(r.Context(), pid)
	if err != nil || p.OrgID != owner(r) || keyIndex(p, kid) < 0 {
		fail(w, store.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := a.G.Reload(ctx); err != nil {
		fail(w, err)
		return
	}
	res, err := a.G.TestKey(ctx, pid, kid, in.Model)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

type keyHour struct {
	Hour         string `json:"hour"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
	AvgLatencyMs int64  `json:"avg_latency_ms"`
	InTokens     int64  `json:"in_tokens"`
	OutTokens    int64  `json:"out_tokens"`
}

func (a *API) providerKeyStats(w http.ResponseWriter, r *http.Request) {
	n := 72
	if s := r.URL.Query().Get("hours"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v < 1 || v > 720 {
			fail(w, fmt.Errorf("%w: hours must be 1-720", store.ErrInvalid))
			return
		}
		n = v
	}
	pid, kid := r.PathValue("id"), r.PathValue("kid")
	p, err := a.S.GetProvider(r.Context(), pid)
	if err != nil || p.OrgID != owner(r) || keyIndex(p, kid) < 0 {
		fail(w, store.ErrNotFound)
		return
	}
	hours, buckets, err := a.keyHours(r.Context(), pid, n, time.Now())
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]keyHour, n)
	for i, h := range hours {
		s := buckets[i][kid]
		out[i] = keyHour{Hour: h.Format(time.RFC3339), Requests: s[store.AggRequests], Errors: s[store.AggErrors],
			AvgLatencyMs: avgLatency(s), InTokens: s[store.AggInTokens], OutTokens: s[store.AggOutTokens]}
	}
	writeJSON(w, 200, out)
}
