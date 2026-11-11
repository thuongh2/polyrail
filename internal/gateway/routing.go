package gateway

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// breaker skips targets that failed repeatedly, for a cool-down period.
type breaker struct {
	mu        sync.Mutex
	fails     map[string]int
	openUntil map[string]time.Time
	threshold int
	cooldown  time.Duration
}

func newBreaker() *breaker {
	return &breaker{fails: map[string]int{}, openUntil: map[string]time.Time{}, threshold: 3, cooldown: 30 * time.Second}
}

func (b *breaker) ok(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().After(b.openUntil[id])
}

func (b *breaker) result(id string, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		delete(b.fails, id)
		return
	}
	b.fails[id]++
	if b.fails[id] >= b.threshold {
		b.openUntil[id] = time.Now().Add(b.cooldown)
		b.fails[id] = 0
	}
}

// openFor opens a target's circuit for d (an upstream Retry-After), capped at 5 minutes.
func (b *breaker) openFor(id string, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if until := time.Now().Add(min(d, 5*time.Minute)); until.After(b.openUntil[id]) {
		b.openUntil[id] = until
	}
}

// targetFault reports whether an upstream status counts against the target: server errors,
// timeouts, rate limits and rejected provider credentials (the gateway already authenticated the
// caller, so an upstream 401/403 means the provider key is wrong). Other 4xx are request errors.
func targetFault(status int) bool {
	return status >= 500 || status == 408 || status == 429 || status == 401 || status == 403
}

func targetID(t store.Target) string { return t.ProviderID + "|" + t.Model }

// targetStats are this node's live observations of one target (EWMA, alpha 0.2).
type targetStats struct {
	inflight  int64
	latencyMs float64 // 0 = unknown
	errRate   float64
}

// router orders a model's targets by its strategy, from the breaker and per-target stats.
// shortcut: stats are per node and in memory; keys are bounded by the catalog's targets.
type router struct {
	br    *breaker
	mu    sync.Mutex
	stats map[string]*targetStats
	rr    map[string]*atomic.Uint64 // round-robin counter per model
	f64   func() float64            // random source; tests make it deterministic
}

func newRouter() *router {
	return &router{br: newBreaker(), stats: map[string]*targetStats{}, rr: map[string]*atomic.Uint64{}, f64: rand.Float64}
}

func (rt *router) stat(id string) *targetStats {
	s := rt.stats[id]
	if s == nil {
		s = &targetStats{}
		rt.stats[id] = s
	}
	return s
}

// begin counts a request in flight to the target; call the returned func when it ends.
func (rt *router) begin(t store.Target) func() {
	id := targetID(t)
	rt.mu.Lock()
	rt.stat(id).inflight++
	rt.mu.Unlock()
	return func() {
		rt.mu.Lock()
		rt.stat(id).inflight--
		rt.mu.Unlock()
	}
}

// observe records one attempt's outcome; latency is used for successes only.
func (rt *router) observe(t store.Target, ok bool, latency time.Duration) {
	const a = 0.2
	rt.mu.Lock()
	defer rt.mu.Unlock()
	s := rt.stat(targetID(t))
	if !ok {
		s.errRate += a * (1 - s.errRate)
		return
	}
	s.errRate -= a * s.errRate
	ms := float64(latency.Milliseconds())
	if s.latencyMs == 0 {
		s.latencyMs = ms
	} else {
		s.latencyMs += a * (ms - s.latencyMs)
	}
}

func (rt *router) snapshot(id string) targetStats {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if s := rt.stats[id]; s != nil {
		return *s
	}
	return targetStats{}
}

func (rt *router) counter(key string) *atomic.Uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	c := rt.rr[key]
	if c == nil {
		c = &atomic.Uint64{}
		rt.rr[key] = c
	}
	return c
}

func strategyName(m store.Model) string {
	if m.Strategy == "" {
		return "weighted"
	}
	return m.Strategy
}

// plan orders a model's targets: healthy targets by the model's strategy, then open-circuit
// targets as a last resort. affinity yields the cache-affinity key ("" or nil when the request
// has none); it is a func so the prefix is hashed only for cache-affinity models.
func plan(m store.Model, snap *Snapshot, rt *router, affinity func() string) []store.Target {
	var healthy, sick []store.Target
	for _, t := range m.Targets {
		if _, ok := snap.Providers[t.ProviderID]; !ok {
			continue
		}
		if rt.br.ok(targetID(t)) {
			healthy = append(healthy, t)
		} else {
			sick = append(sick, t)
		}
	}
	if len(healthy) > 1 {
		rt.order(m, healthy, affinity)
	}
	return append(healthy, sick...)
}

func (rt *router) order(m store.Model, ts []store.Target, affinityFn func() string) {
	by := func(score func(store.Target) float64) { // ascending, stable on declared order
		sc := make(map[string]float64, len(ts))
		for _, t := range ts {
			sc[targetID(t)] = score(t)
		}
		sort.SliceStable(ts, func(i, j int) bool { return sc[targetID(ts[i])] < sc[targetID(ts[j])] })
	}
	switch m.Strategy {
	case "priority":
	case "round-robin":
		n := int(rt.counter(m.OrgID+"|"+m.ID).Add(1)-1) % len(ts)
		slices.Reverse(ts[:n]) // rotate left by n
		slices.Reverse(ts[n:])
		slices.Reverse(ts)
	case "random":
		for i := len(ts) - 1; i > 0; i-- {
			j := rt.intn(i + 1)
			ts[i], ts[j] = ts[j], ts[i]
		}
	case "p2c":
		i, j := rt.intn(len(ts)), rt.intn(len(ts)-1)
		if j >= i {
			j++
		}
		if rt.p2cScore(ts[j]) > rt.p2cScore(ts[i]) {
			i, j = j, i
		}
		out := []store.Target{ts[i], ts[j]}
		for k, t := range ts {
			if k != i && k != j {
				out = append(out, t)
			}
		}
		sort.SliceStable(out[2:], func(x, y int) bool { return out[2+x].Weight > out[2+y].Weight })
		copy(ts, out)
	case "least-used":
		by(func(t store.Target) float64 { return float64(rt.snapshot(targetID(t)).inflight) })
	case "latency":
		by(func(t store.Target) float64 { s := rt.snapshot(targetID(t)); return s.latencyMs + s.errRate*1000 })
	case "cost":
		by(func(t store.Target) float64 {
			in, out := t.InputPrice, t.OutputPrice
			if in == 0 {
				in = m.InputPrice
			}
			if out == 0 {
				out = m.OutputPrice
			}
			return float64(in + out)
		})
	case "cache-affinity":
		var affinity string
		if affinityFn != nil {
			affinity = affinityFn()
		}
		if affinity != "" { // rendezvous hashing: the same key keeps the same primary
			by(func(t store.Target) float64 {
				h := sha256.Sum256([]byte(affinity + "\x00" + targetID(t)))
				return -float64(binary.BigEndian.Uint64(h[:8]))
			})
			return
		}
		rt.weighted(ts)
	default:
		rt.weighted(ts)
	}
}

func (rt *router) intn(n int) int { return min(int(rt.f64()*float64(n)), n-1) }

// p2cScore is higher for targets that succeed more and answer faster.
func (rt *router) p2cScore(t store.Target) float64 {
	s := rt.snapshot(targetID(t))
	return 1 - s.errRate + 1/math.Log10(s.latencyMs+10)
}

// weighted sorts by weight, then moves a weighted-random pick to the front.
func (rt *router) weighted(ts []store.Target) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Weight > ts[j].Weight })
	var total float64
	for _, t := range ts {
		total += weight(t)
	}
	r := rt.f64() * total
	for i, t := range ts {
		if r -= weight(t); r < 0 {
			ts[0], ts[i] = ts[i], ts[0]
			break
		}
	}
}

func weight(t store.Target) float64 {
	if t.Weight <= 0 {
		return 1
	}
	return t.Weight
}

// affinityKey is the cache-affinity key: the caller's prompt_cache_key, else a hash of the
// request prefix that providers cache (system/developer messages before the first user message,
// and tools). "" when there is neither.
func affinityKey(cacheKey *string, prefix ...any) string {
	if cacheKey != nil && *cacheKey != "" {
		return *cacheKey
	}
	h := sha256.New()
	empty := true
	for _, p := range prefix {
		b, err := json.Marshal(p)
		if err != nil || string(b) == "null" || string(b) == "[]" || string(b) == `""` {
			continue
		}
		empty = false
		h.Write(b)
	}
	if empty {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// settleErr updates breakers and stats after a request failed at every target. The top-level
// error is the primary's; fallbacks' statuses come from Bifrost's FallbackAttempts. A 429 with a
// Retry-After hint opens the primary until then.
func (g *Gateway) settleErr(snap *Snapshot, m store.Model, ts []store.Target, be *schemas.BifrostError, start time.Time) {
	attempts := map[string]schemas.FallbackAttempt{}
	for _, a := range be.ExtraFields.FallbackAttempts {
		attempts[string(a.Provider)+"|"+a.Model] = a
	}
	for i, t := range ts {
		st := statusOf(be.StatusCode)
		if i > 0 && len(attempts) > 0 {
			a, ok := attempts[string(BifrostKey(snap.Providers[t.ProviderID]))+"|"+t.Model]
			if !ok || a.Skipped {
				continue
			}
			st = statusOf(a.StatusCode)
		}
		if !targetFault(st) {
			continue
		}
		g.rt.br.result(targetID(t), false)
		g.rt.observe(t, false, 0)
		if m.OrgID == "" {
			g.Usage.RecordHealth(m.ID, t.ProviderID, false, 0, 0, start)
		}
	}
	if len(ts) > 0 && statusOf(be.StatusCode) == http.StatusTooManyRequests && be.ExtraFields.RetryAfter > 0 {
		g.rt.br.openFor(targetID(ts[0]), time.Duration(be.ExtraFields.RetryAfter)*time.Millisecond)
	}
}

// statusOf is the upstream status, 502 when there was none (network error, timeout).
func statusOf(code *int) int {
	if code != nil && *code >= 400 && *code < 600 {
		return *code
	}
	return http.StatusBadGateway
}
