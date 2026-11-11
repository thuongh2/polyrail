package gateway

// Semantic cache: an org opts in (store.Org.SemanticCache) to answer a non-stream chat request
// from a previous response whose prompt embeds close enough (cosine >= threshold). Only
// deterministic requests qualify: temperature explicitly 0, no tools, no json_schema output.
// The prompt (system text + last user message) is embedded through the org's embedding model
// on the normal Bifrost path and billed like any embeddings call. Embedding errors fail open.

import (
	"math"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

const (
	semMaxPerOrg     = 1000
	semMaxChars      = 8000
	SemDefaultThresh = 0.92
	SemDefaultTTL    = 3600
	SemMaxTTL        = 86400
)

type semEntry struct {
	model, emb string // chat model id, embedding model id
	vec        []float32
	resp       []byte
	exp, used  time.Time
}

func (e *semEntry) size() int { return len(e.resp) + 4*len(e.vec) + 128 }

type semOrg struct {
	mu      sync.Mutex
	entries []*semEntry
}

// semCache holds normalized prompt vectors per org: brute-force cosine, LRU within an org,
// and a byte budget across all orgs.
type semCache struct {
	mu    sync.Mutex
	orgs  map[string]*semOrg
	bytes atomic.Int64
	limit int64
}

func newSemCache(limitBytes int) *semCache {
	return &semCache{orgs: map[string]*semOrg{}, limit: int64(limitBytes)}
}

func (c *semCache) org(id string, create bool) *semOrg {
	c.mu.Lock()
	defer c.mu.Unlock()
	o := c.orgs[id]
	if o == nil && create {
		o = &semOrg{}
		c.orgs[id] = o
	}
	return o
}

// lookup returns the best unexpired response for (org, model, emb) and its score, if >= threshold.
func (c *semCache) lookup(org, model, emb string, vec []float32, threshold float64, now time.Time) ([]byte, float64, bool) {
	o := c.org(org, false)
	if o == nil {
		return nil, 0, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var best *semEntry
	bestScore := -1.0
	o.entries = slices.DeleteFunc(o.entries, func(e *semEntry) bool {
		if now.After(e.exp) {
			c.bytes.Add(-int64(e.size()))
			return true
		}
		return false
	})
	for _, e := range o.entries {
		if e.model != model || e.emb != emb || len(e.vec) != len(vec) {
			continue
		}
		if s := dot(e.vec, vec); s > bestScore {
			best, bestScore = e, s
		}
	}
	if best == nil || bestScore < threshold {
		return nil, bestScore, false
	}
	best.used = now
	return best.resp, bestScore, true
}

// add stores e, evicting the org's least recently used entries for the per-org cap and the
// global byte budget; e is skipped if it still does not fit.
func (c *semCache) add(org string, e *semEntry) {
	o := c.org(org, true)
	o.mu.Lock()
	defer o.mu.Unlock()
	for len(o.entries) > 0 && (len(o.entries) >= semMaxPerOrg || c.bytes.Load()+int64(e.size()) > c.limit) {
		i := 0
		for j, x := range o.entries {
			if x.used.Before(o.entries[i].used) {
				i = j
			}
		}
		c.bytes.Add(-int64(o.entries[i].size()))
		o.entries = slices.Delete(o.entries, i, i+1)
	}
	if c.bytes.Load()+int64(e.size()) > c.limit {
		return
	}
	c.bytes.Add(int64(e.size()))
	o.entries = append(o.entries, e)
}

func dot(a, b []float32) float64 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return float64(s)
}

func normalize(v []float64) []float32 {
	var n float64
	for _, x := range v {
		n += x * x
	}
	if n == 0 {
		return nil
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x / n)
	}
	return out
}

// semEligible reports whether a chat request is deterministic enough to share answers.
func semEligible(breq *schemas.BifrostChatRequest) bool {
	p := breq.Params
	if p == nil || p.Temperature == nil || *p.Temperature != 0 || len(p.Tools) > 0 {
		return false
	}
	if p.ResponseFormat != nil {
		if rf, ok := (*p.ResponseFormat).(map[string]any); !ok || rf["type"] == "json_schema" {
			return false
		}
	}
	return true
}

// semText is the system prompt text plus the last user message, truncated to semMaxChars.
func semText(msgs []schemas.ChatMessage) string {
	var sys, last []schemas.ChatMessage
	for i, m := range msgs {
		switch m.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			sys = append(sys, m)
		case schemas.ChatMessageRoleUser:
			last = msgs[i : i+1]
		}
	}
	var text string
	for _, s := range texts(append(sys, last...)) {
		text += *s + "\n"
	}
	if r := []rune(text); len(r) > semMaxChars {
		text = string(r[:semMaxChars])
	}
	return text
}

// semPending is an eligible request that missed; store adds the response once it succeeds.
type semPending struct {
	org, model, emb string
	vec             []float32
	ttl             time.Duration
}

// semanticCache serves a hit (and returns hit=true) or returns the pending entry to fill after
// the upstream call; nil when the request is not eligible or embedding failed. kbSig (the
// grounding KBs' id@rev list) separates answers grounded on different knowledge.
func (g *Gateway) semanticCache(w http.ResponseWriter, r *http.Request, p *principal, m store.Model, breq *schemas.BifrostChatRequest, kbSig string, start time.Time) (*semPending, bool) {
	sc := p.Org.SemanticCache
	if sc == nil || sc.Model == "" || g.cfg.CacheMB <= 0 || !semEligible(breq) {
		return nil, false
	}
	text := semText(breq.Input)
	if text == "" {
		return nil, false
	}
	vs, err := g.embed(r.Context(), p, sc.Model, 0, []string{text})
	if err != nil {
		return nil, false // fail open
	}
	vec := normalize32(vs[0])
	if vec == nil {
		return nil, false
	}
	emb := sc.Model
	if ws := p.ws(); ws != "" { // answers stay in their workspace (D31)
		emb += "|ws:" + ws
	}
	if kbSig != "" {
		emb += "|" + kbSig
	}
	thresh := sc.Threshold
	if thresh == 0 {
		thresh = SemDefaultThresh
	}
	ttl := sc.TTLSeconds
	if ttl <= 0 {
		ttl = SemDefaultTTL
	}
	if out, score, ok := g.sem.lookup(p.Org.ID, m.ID, emb, vec, thresh, time.Now()); ok {
		mCacheHits.Inc()
		setCost(w, g.record(p, m, store.RequestLog{Status: 200, Cached: true, Provider: "cache"}, start))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-GW-Cache", "semantic")
		w.Header().Set("X-GW-Cache-Score", strconv.FormatFloat(score, 'f', 4, 64))
		_, _ = w.Write(out)
		return nil, true
	}
	return &semPending{org: p.Org.ID, model: m.ID, emb: emb, vec: vec, ttl: time.Duration(min(ttl, SemMaxTTL)) * time.Second}, false
}

func (g *Gateway) semStore(sp *semPending, out []byte) {
	if sp == nil {
		return
	}
	now := time.Now()
	g.sem.add(sp.org, &semEntry{model: sp.model, emb: sp.emb, vec: sp.vec, resp: out, exp: now.Add(sp.ttl), used: now})
}

// normalize32 L2-normalizes a float32 vector (nil for the zero vector).
func normalize32(v []float32) []float32 {
	f := make([]float64, len(v))
	for i, x := range v {
		f[i] = float64(x)
	}
	return normalize(f)
}
