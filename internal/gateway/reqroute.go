package gateway

// OpenRouter-compatible request routing (docs/API.md "Request routing", D27): the `models`
// fallback list, the `provider` preferences object, :nitro / :floor shortcuts, org presets
// ("@preset/<slug>"), usage accounting in the body and the `user` end-user id. Chat completions,
// Responses and legacy completions read these fields; they never reach the upstream.

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// reqRoute is one request's routing options, carried on its principal (see withRoute).
type reqRoute struct {
	models  []routeModel // [0] is the requested model, then the `models` fallbacks
	prefs   *store.ProviderPrefs
	usage   bool     // usage.include: add usage.cost to the response body
	endUser string   // `user`, at most 128 bytes
	needs   []string // modalities the request uses (require_parameters)
}

type routeModel struct {
	id   string
	sort string // "throughput" (:nitro) | "price" (:floor) | ""
}

const presetPrefix = "@preset/"

// routedFields are dropped from the body after they are read: they are gateway-level.
var routedFields = []string{"models", "provider", "preset", "usage"}

func badField(format string, args ...any) *apiError {
	return errf(400, "invalid_request_error", "invalid_routing", format, args...)
}

// routeBody reads the routing fields of a request body for endpoint "chat", "responses" or
// "completions", applies a preset, and returns the body to decode (model set to the resolved id,
// routing fields removed). The route is nil when the request uses none of these features.
func (g *Gateway) routeBody(p *principal, body []byte, endpoint string) ([]byte, *reqRoute, *apiError) {
	if !gjson.ValidBytes(body) {
		return body, nil, nil // the handler's own decode reports it
	}
	f := gjson.GetManyBytes(body, "model", "models", "provider", "preset", "usage", "user")
	model, models, provider, preset, usageF, user := f[0], f[1], f[2], f[3], f[4], f[5]
	if model.Type != gjson.String && model.Exists() {
		return body, nil, nil
	}
	id := model.String()
	if !models.Exists() && !provider.Exists() && !preset.Exists() && !usageF.Exists() && !user.Exists() &&
		!strings.HasPrefix(id, presetPrefix) && !strings.HasSuffix(id, ":nitro") && !strings.HasSuffix(id, ":floor") {
		return body, nil, nil
	}
	rr := &reqRoute{}
	var err error

	// preset: "model": "@preset/<slug>" or "preset": "<slug>"; request fields win over it
	slug := ""
	if strings.HasPrefix(id, presetPrefix) {
		slug = strings.TrimPrefix(id, presetPrefix)
		id = ""
	} else if preset.Exists() {
		if preset.Type != gjson.String {
			return nil, nil, badField("preset must be a string")
		}
		slug = preset.String()
	}
	var ps *store.Preset
	if slug != "" {
		pr, ok := g.snap.Load().Preset(p.Org.ID, p.ws(), slug)
		if !ok {
			return nil, nil, errf(404, "invalid_request_error", "preset_not_found", "preset %q not found", slug)
		}
		ps = &pr
		if body, err = applyPresetParams(body, pr.Params, endpoint); err != nil {
			return nil, nil, badField("cannot apply preset: %v", err)
		}
	}

	var list []string
	switch {
	case models.Exists():
		if !models.IsArray() {
			return nil, nil, badField("models must be an array of model ids")
		}
		for _, m := range models.Array() {
			if m.Type != gjson.String || m.String() == "" || strings.HasPrefix(m.String(), presetPrefix) {
				return nil, nil, badField("models must be an array of model ids (not presets)")
			}
			list = append(list, m.String())
		}
	case ps != nil:
		list = ps.Models
	}
	if len(list) > store.MaxRouteModels {
		return nil, nil, badField("models has %d entries; at most %d", len(list), store.MaxRouteModels)
	}
	if id == "" && ps != nil {
		id = ps.Model
	}
	if id == "" && len(list) > 0 {
		id = list[0]
	}
	if id == "" {
		return body, nil, nil // the handler reports the missing model
	}
	for _, m := range append([]string{id}, list...) {
		rm := routeModel{id: m}
		if base, ok := strings.CutSuffix(m, ":nitro"); ok {
			rm = routeModel{base, "throughput"}
		} else if base, ok := strings.CutSuffix(m, ":floor"); ok {
			rm = routeModel{base, "price"}
		}
		if !slices.ContainsFunc(rr.models, func(x routeModel) bool { return x.id == rm.id }) {
			rr.models = append(rr.models, rm)
		}
	}

	switch {
	case provider.Exists():
		if !provider.IsObject() {
			return nil, nil, badField("provider must be an object")
		}
		if rr.prefs, err = store.ParseProviderPrefs([]byte(provider.Raw)); err != nil {
			return nil, nil, badField("%s", strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
		}
	case ps != nil:
		rr.prefs = ps.Provider
	}
	if usageF.Exists() {
		inc := usageF.Get("include")
		if !usageF.IsObject() || (inc.Exists() && !inc.IsBool()) {
			return nil, nil, badField(`usage must be an object like {"include": true}`)
		}
		rr.usage = inc.Bool()
	}
	if user.Exists() {
		if user.Type != gjson.String {
			return nil, nil, badField("user must be a string")
		}
		rr.endUser = truncUTF8(user.String(), 128)
	}
	if rr.prefs != nil && rr.prefs.RequireParameters {
		rr.needs = needs(body, endpoint)
	}

	if body, err = sjson.SetBytes(body, "model", rr.models[0].id); err != nil {
		return nil, nil, badField("cannot rewrite model")
	}
	for _, k := range routedFields {
		if body, err = sjson.DeleteBytes(body, k); err != nil {
			return nil, nil, badField("cannot strip %s", k)
		}
	}
	return body, rr, nil
}

func truncUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// applyPresetParams sets each preset parameter the request does not set itself. "system" is
// prepended as a system message (chat) or to instructions (Responses); completions ignore it.
func applyPresetParams(body []byte, params map[string]any, endpoint string) ([]byte, error) {
	var err error
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range params {
			if !yield(k) {
				return
			}
		}
	}) {
		v := params[k]
		switch {
		case k == "system":
			body, err = prependSystem(body, v.(string), endpoint)
		case k == "max_tokens" && endpoint == "responses":
			if !gjson.GetBytes(body, "max_output_tokens").Exists() {
				body, err = sjson.SetBytes(body, "max_output_tokens", v)
			}
		default:
			if !gjson.GetBytes(body, k).Exists() {
				body, err = sjson.SetBytes(body, k, v)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func prependSystem(body []byte, system, endpoint string) ([]byte, error) {
	switch endpoint {
	case "chat":
		msg, _ := json.Marshal(map[string]string{"role": "system", "content": system})
		old := strings.TrimSpace(gjson.GetBytes(body, "messages").Raw)
		if !strings.HasPrefix(old, "[") {
			return body, nil // the handler rejects the missing messages
		}
		rest := strings.TrimSpace(old[1:])
		if rest != "]" {
			rest = "," + rest
		}
		return sjson.SetRawBytes(body, "messages", append(append([]byte{'['}, msg...), rest...))
	case "responses":
		if cur := gjson.GetBytes(body, "instructions").String(); cur != "" {
			system += "\n\n" + cur
		}
		return sjson.SetBytes(body, "instructions", system)
	}
	return body, nil
}

// needs lists the modalities a request uses that a model must support under require_parameters:
// requested output `modalities` and "image" for image inputs.
func needs(body []byte, endpoint string) []string {
	var out []string
	for _, m := range gjson.GetBytes(body, "modalities").Array() {
		out = append(out, m.String())
	}
	path := "messages.#.content.#.type"
	if endpoint == "responses" {
		path = "input.#.content.#.type"
	}
	for _, msg := range gjson.GetBytes(body, path).Array() {
		for _, t := range msg.Array() {
			if s := t.String(); (s == "image_url" || s == "input_image") && !slices.Contains(out, "image") {
				out = append(out, "image")
			}
		}
	}
	return out
}

// withRoute returns a per-request copy of p carrying rr (cached principals are shared).
func (p *principal) withRoute(rr *reqRoute) *principal {
	if rr == nil {
		return p
	}
	cp := *p
	cp.rr = rr
	return &cp
}

// resolveRoute is resolve for a routed request: the requested model, then each `models` fallback
// (skipped when missing, not allowed, of another kind, or left without targets by the provider
// preferences), each planned by its own strategy and then the preferences. The targets of all
// models form one fallback chain; fallback models' targets carry Via. The returned model is the
// first usable one.
func (g *Gateway) resolveRoute(p *principal, id, kind string, affinity func() string) (store.Model, []store.Target, *apiError) {
	rr := p.rr
	if rr == nil {
		return g.resolve(p, id, kind, affinity)
	}
	if rr.endUser != "" { // sticky routing for cache-affinity models without a prompt key or prefix
		inner := affinity
		affinity = func() string {
			if inner != nil {
				if k := inner(); k != "" {
					return k
				}
			}
			return rr.endUser
		}
	}
	snap := g.snap.Load()
	var first store.Model
	var out []store.Target
	var firstErr *apiError
	seen := map[string]bool{}
	for _, rm := range rr.models {
		m, ts, e := g.resolve(p, rm.id, kind, affinity)
		if e == nil && rr.prefs != nil && rr.prefs.RequireParameters && !supports(m, rr.needs) {
			e = errf(400, "invalid_request_error", "unsupported_parameters", "model %q does not support the requested modalities", rm.id)
		}
		if e == nil {
			if ts = g.preferTargets(snap, m, ts, rr.prefs, rm.sort); len(ts) == 0 {
				e = errf(503, "api_error", "no_provider", "no provider of model %q matches the request's provider preferences", rm.id)
			}
		}
		if e != nil {
			if firstErr == nil {
				firstErr = e
			}
			continue
		}
		var via *store.Model
		if len(out) == 0 {
			first = m
		} else if m.ID != first.ID {
			via = &m
		}
		for _, t := range ts {
			if !seen[targetID(t)] { // Bifrost would retry the same provider and model
				seen[targetID(t)] = true
				t.Via = via
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		return first, nil, firstErr
	}
	if s := rr.prefs; s != nil && s.Sort != nil && s.Sort.Partition == "none" && len(rr.models) > 1 {
		out = g.preferTargets(snap, first, out, &store.ProviderPrefs{Sort: s.Sort}, "")
	}
	return first, out, nil
}

// supports reports whether m's declared modalities cover needs (unknown when it declares none).
func supports(m store.Model, needs []string) bool {
	if len(m.Modalities) == 0 {
		return true
	}
	for _, n := range needs {
		if !slices.Contains(m.Modalities, n) {
			return false
		}
	}
	return true
}

// ownerOf is the gateway model a planned target belongs to.
func ownerOf(m store.Model, t store.Target) store.Model {
	if t.Via != nil {
		return *t.Via
	}
	return m
}

// usedModel is the model whose target served the request (provider, and upstream model when
// known): billing, the response `model` and X-GW-Route use it.
func usedModel(snap *Snapshot, m store.Model, ts []store.Target, served schemas.ModelProvider, upstream string) store.Model {
	id := snap.byBifrost[served]
	byProv := -1
	for i, t := range ts {
		if t.ProviderID != id {
			continue
		}
		if upstream == "" || t.Model == upstream {
			return ownerOf(m, t)
		}
		if byProv < 0 {
			byProv = i
		}
	}
	if byProv >= 0 {
		return ownerOf(m, ts[byProv])
	}
	return m
}

func providerIn(list []string, p store.Provider) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, p.Name) || strings.EqualFold(s, p.Type) })
}

// targetPrices are a target's prices in micro-USD per 1M tokens: its override, else the model's.
func targetPrices(m store.Model, t store.Target) (in, out int64) {
	in, out = t.InputPrice, t.OutputPrice
	if in == 0 {
		in = m.InputPrice
	}
	if out == 0 {
		out = m.OutputPrice
	}
	return in, out
}

// preferTargets applies provider preferences to targets planned by the model's strategy:
// filters (only, ignore, zdr, data_collection, max_price) always; `sort` (or the :nitro / :floor
// shortcut) and `order` replace the strategy's order; preferred_max_latency and
// preferred_min_throughput move slower known targets back; open-circuit targets stay last;
// allow_fallbacks false keeps the first target.
func (g *Gateway) preferTargets(snap *Snapshot, m store.Model, ts []store.Target, pp *store.ProviderPrefs, shortcut string) []store.Target {
	if pp == nil && shortcut == "" {
		return ts
	}
	if pp == nil {
		pp = &store.ProviderPrefs{}
	}
	var healthy, sick []store.Target
	for _, t := range ts {
		prov := snap.Providers[t.ProviderID]
		pv := prov.Privacy // nil: undeclared, which zdr and data_collection deny do not accept (as policies)
		om := ownerOf(m, t)
		in, out := targetPrices(om, t)
		mp := pp.MaxPrice
		switch {
		case len(pp.Only) > 0 && !providerIn(pp.Only, prov),
			providerIn(pp.Ignore, prov),
			pp.ZDR != nil && *pp.ZDR && (pv == nil || !pv.ZDR),
			pp.DataCollection == "deny" && (pv == nil || (pv.TrainsOnData || pv.RetentionDays > 0) && !pv.ZDR),
			mp != nil && mp.Prompt != nil && float64(in) > *mp.Prompt*1e6,
			mp != nil && mp.Completion != nil && float64(out) > *mp.Completion*1e6,
			mp != nil && mp.Request != nil && float64(om.RequestPrice) > *mp.Request*1e6,
			mp != nil && mp.Image != nil && float64(om.ImagePrice) > *mp.Image*1e6:
			continue
		}
		if g.rt.br.ok(targetID(t)) {
			healthy = append(healthy, t)
		} else {
			sick = append(sick, t)
		}
	}
	by := shortcut
	if pp.Sort != nil {
		by = pp.Sort.By
	}
	stats := make(map[string]targetStats, len(healthy))
	for _, t := range healthy {
		stats[targetID(t)] = g.rt.snapshot(targetID(t))
	}
	// rank is (bucket, value), ascending; unknown stats sort after known ones
	rank := func(t store.Target) (int, float64) {
		s := stats[targetID(t)]
		switch by {
		case "price":
			in, out := targetPrices(ownerOf(m, t), t)
			return 0, float64(in + out)
		case "throughput":
			if s.tps > 0 {
				return 0, -s.tps
			}
			if s.latencyMs > 0 {
				return 1, s.latencyMs
			}
			return 2, 0
		case "latency":
			if s.latencyMs > 0 {
				return 0, s.latencyMs + s.errRate*1000
			}
			return 1, 0
		}
		return 0, 0
	}
	slow := func(t store.Target) int {
		s := stats[targetID(t)]
		if v := pp.PreferredMaxLatency; v != nil && *v > 0 && s.latencyMs > float64(*v)*1000 {
			return 1
		}
		if v := pp.PreferredMinThroughput; v != nil && *v > 0 && s.tps > 0 && s.tps < float64(*v) {
			return 1
		}
		return 0
	}
	orderIdx := func(t store.Target) int {
		prov := snap.Providers[t.ProviderID]
		for i, s := range pp.Order {
			if strings.EqualFold(s, prov.Name) || strings.EqualFold(s, prov.Type) {
				return i
			}
		}
		return len(pp.Order)
	}
	sort.SliceStable(healthy, func(i, j int) bool {
		a, b := healthy[i], healthy[j]
		if oa, ob := orderIdx(a), orderIdx(b); oa != ob {
			return oa < ob
		}
		if sa, sb := slow(a), slow(b); sa != sb {
			return sa < sb
		}
		ba, va := rank(a)
		bb, vb := rank(b)
		if ba != bb {
			return ba < bb
		}
		return va < vb
	})
	out := append(healthy, sick...)
	if pp.AllowFallbacks != nil && !*pp.AllowFallbacks && len(out) > 1 {
		out = out[:1]
	}
	return out
}

// observeTPS records output tokens per second of a successful attempt (EWMA, alpha 0.2).
// shortcut: whole-request duration (streams include time to first token).
func (rt *router) observeTPS(t store.Target, outTokens int, d time.Duration) {
	if outTokens <= 0 || d <= 0 {
		return
	}
	v := float64(outTokens) / d.Seconds()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	s := rt.stat(targetID(t))
	if s.tps == 0 {
		s.tps = v
	} else {
		s.tps += 0.2 * (v - s.tps)
	}
}

// setModel sets the response model of a stream payload the pump writes (Bifrost types only).
func setModel(payload any, id string) {
	switch v := payload.(type) {
	case *schemas.BifrostTextCompletionResponse:
		v.Model = id
	case *schemas.BifrostResponsesStreamResponse:
		if v.Response != nil {
			v.Response.Model = id
		}
	}
}

// billed is what record() bills for a successful request with this usage.
func billed(m store.Model, l store.RequestLog) int64 { return logCost(m, l) + m.RequestPrice }

// withCost adds OpenRouter usage accounting to an encoded response that has usage (at
// "usage", or "response.usage" for Responses stream events) when the request asked for it:
// usage.cost (USD), usage.cost_micros, and OpenRouter's cached_tokens / cache_write_tokens next to
// Bifrost's cached_read_tokens / cached_write_tokens.
func (p *principal) withCost(data []byte, micros int64) []byte {
	if p.rr == nil || !p.rr.usage {
		return data
	}
	path := "usage"
	if !gjson.GetBytes(data, path).IsObject() {
		if path = "response.usage"; !gjson.GetBytes(data, path).IsObject() {
			return data
		}
	}
	out, err := sjson.SetRawBytes(data, path+".cost", strconv.AppendFloat(nil, float64(micros)/1e6, 'f', -1, 64))
	if err == nil {
		out, err = sjson.SetBytes(out, path+".cost_micros", micros)
	}
	for _, d := range []string{".prompt_tokens_details", ".input_tokens_details"} {
		for _, f := range [][2]string{{"cached_read_tokens", "cached_tokens"}, {"cached_write_tokens", "cache_write_tokens"}} {
			if c := gjson.GetBytes(out, path+d+"."+f[0]); err == nil && c.Exists() && !gjson.GetBytes(out, path+d+"."+f[1]).Exists() {
				out, err = sjson.SetBytes(out, path+d+"."+f[1], c.Int())
			}
		}
	}
	if err != nil {
		return data
	}
	return out
}

// writeRouted is writeJSONHidden with usage accounting (see withCost).
func writeRouted(w http.ResponseWriter, p *principal, provName string, v any, micros int64) {
	out, err := hideExtra(v)
	if err != nil {
		writeError(w, errf(500, "api_error", "encode", "cannot encode response"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-gateway-provider", provName)
	_, _ = w.Write(p.withCost(out, micros))
}

// privateRoute reports whether any planned target belongs to an org-private model: upstream
// error text is then withheld (fromBifrost), whichever target produced it.
func privateRoute(m store.Model, ts []store.Target) bool {
	return m.OrgID != "" || slices.ContainsFunc(ts, func(t store.Target) bool { return t.Via != nil && t.Via.OrgID != "" })
}
