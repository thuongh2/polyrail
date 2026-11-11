// Package gateway serves the OpenAI-compatible API on top of Bifrost core.
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/limits"
	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const KeyPrefix = "sk-gw-"

type Config struct {
	MaxBodyBytes int64
	Concurrency  int // per provider
	Buffer       int // per provider queue
	CacheMB      int // response cache
	PollInterval time.Duration
	// AllowPrivateURLs lets tenant providers reach private networks (else re-checked per request).
	AllowPrivateURLs bool
}

type Gateway struct {
	S     *store.Store
	Usage *usage.Pipeline
	Lim   limits.Limiter
	BF    *bifrost.Bifrost
	cfg   Config

	snap atomic.Pointer[Snapshot]
	auth *ttlCache[*principal]
	resp *ttlCache[[]byte]
	rt   *router
	mcp  mcpState
	idem idemStore
}

type principal struct {
	Key store.APIKey
	Org store.Org
}

// HashKey is how API key secrets are stored and looked up.
func HashKey(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func New(ctx context.Context, s *store.Store, lim limits.Limiter, up *usage.Pipeline, cfg Config) (*Gateway, error) {
	g := &Gateway{S: s, Usage: up, Lim: lim, cfg: cfg,
		auth: newTTLCache[*principal](50_000, 0),
		resp: newTTLCache[[]byte](100_000, cfg.CacheMB<<20),
		rt:   newRouter(),
	}
	snap, err := loadSnapshot(ctx, s)
	if err != nil {
		return nil, err
	}
	g.snap.Store(snap)
	mcpCfg, err := g.mcpConfig(ctx)
	if err != nil {
		return nil, err
	}
	g.BF, err = bifrost.Init(ctx, schemas.BifrostConfig{
		Account:   &account{snap: g.snap.Load, concurrency: cfg.Concurrency, buffer: cfg.Buffer, allowPrivate: cfg.AllowPrivateURLs},
		Logger:    bifrost.NewDefaultLogger(schemas.LogLevelWarn),
		MCPConfig: mcpCfg,
	})
	if err != nil {
		return nil, fmt.Errorf("bifrost init: %w", err)
	}
	go g.watch(ctx)
	return g, nil
}

// Snapshot returns the current catalog.
func (g *Gateway) Snapshot() *Snapshot { return g.snap.Load() }

// watch reloads the catalog when the control-plane version changes (any node may write it).
func (g *Gateway) watch(ctx context.Context) {
	t := time.NewTicker(g.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := g.Reload(ctx); err != nil {
			slog.Error("catalog reload failed", "err", err)
		}
	}
}

// Reload rebuilds the catalog if the store version changed.
func (g *Gateway) Reload(ctx context.Context) error {
	ver, err := g.S.Version(ctx)
	if err != nil || ver == g.snap.Load().Version {
		return err
	}
	next, err := loadSnapshot(ctx, g.S)
	if err != nil {
		return err
	}
	prev := g.snap.Swap(next)
	g.auth.Clear() // key revocations and org changes take effect now
	for id, p := range next.Providers {
		old, ok := prev.Providers[id]
		if ok && old.BaseURL == p.BaseURL && old.TimeoutSeconds == p.TimeoutSeconds {
			continue // keys are read per request; only network config needs a provider restart
		}
		if ok {
			if err := g.BF.UpdateProvider(BifrostKey(p)); err != nil {
				slog.Error("provider update failed", "provider", id, "err", err)
			}
		}
	}
	return g.syncMCP(ctx)
}

// ---- errors ----

type apiError struct {
	status int
	typ    string
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func errf(status int, typ, code, format string, args ...any) *apiError {
	return &apiError{status, typ, code, fmt.Sprintf(format, args...)}
}

func writeError(w http.ResponseWriter, e *apiError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.status)
	_, body := errorBody(w, e)
	_ = json.NewEncoder(w).Encode(body)
}

// errorBody is the error payload (and SSE event name) in the API format the handler speaks.
func errorBody(w http.ResponseWriter, e *apiError) (string, any) {
	if _, ok := w.(anthropicWriter); ok {
		return "error", anthropicError(e)
	}
	if _, ok := w.(genaiWriter); ok {
		return "", genaiError(e)
	}
	return "", map[string]any{"error": map[string]any{"message": e.msg, "type": e.typ, "code": e.code}}
}

var reSecretish = regexp.MustCompile(`(sk|key|tok)[-_][A-Za-z0-9*_\-.]{6,}`)

// fromBifrost maps a provider error. Upstream text is passed through for public models (useful
// errors like context-length) with key-like strings redacted; for org-private models it is
// replaced, so a tenant cannot read responses of hosts it points a provider at (SSRF).
func fromBifrost(be *schemas.BifrostError, private bool) *apiError {
	status := http.StatusBadGateway
	if be.StatusCode != nil && *be.StatusCode >= 400 && *be.StatusCode < 600 {
		status = *be.StatusCode
	}
	msg := "upstream error"
	if be.Error != nil && be.Error.Message != "" {
		msg = reSecretish.ReplaceAllString(be.Error.Message, "[redacted]")
	}
	if private {
		msg = fmt.Sprintf("upstream provider returned an error (status %d)", status)
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &apiError{status, "upstream_error", "provider_error", msg}
}

// newBifrostCtx is the per-request Bifrost context; it ends with the client request.
func newBifrostCtx(r *http.Request) *schemas.BifrostContext {
	return schemas.NewBifrostContext(r.Context(), schemas.NoDeadline)
}

// InvalidateAuth drops cached API key principals so key and org changes apply on this node now.
func (g *Gateway) InvalidateAuth() { g.auth.Clear() }

// ---- auth, limits, budget ----

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.Header.Get("x-api-key")
}

func (g *Gateway) authenticate(ctx context.Context, r *http.Request) (*principal, *apiError) {
	secret := bearer(r)
	if !strings.HasPrefix(secret, KeyPrefix) {
		return nil, errf(401, "authentication_error", "invalid_api_key", "missing or malformed API key")
	}
	hash := HashKey(secret)
	p, ok := g.auth.Get(hash)
	if !ok {
		k, err := g.S.KeyByHash(ctx, hash)
		switch {
		case errors.Is(err, store.ErrNotFound):
			p = nil
		case err != nil:
			return nil, errf(503, "api_error", "store_unavailable", "temporarily unavailable")
		default:
			o, err := g.S.GetOrg(ctx, k.OrgID)
			if err != nil {
				return nil, errf(401, "authentication_error", "invalid_api_key", "invalid API key")
			}
			p = &principal{Key: *k, Org: *o}
		}
		g.auth.Set(hash, p, 0, 30*time.Second) // negative results are cached too
	}
	if p == nil || p.Key.Revoked {
		return nil, errf(401, "authentication_error", "invalid_api_key", "invalid API key")
	}
	if p.Key.ExpiresAt != nil && time.Now().After(*p.Key.ExpiresAt) {
		return nil, errf(401, "authentication_error", "expired_api_key", "API key expired")
	}
	return p, nil
}

func (g *Gateway) admit(ctx context.Context, p *principal) *apiError {
	if p.Org.Prepaid {
		spent, err := g.Usage.Spent(ctx, usage.OrgSpendKey(p.Org.ID))
		if err != nil {
			return errf(503, "api_error", "store_unavailable", "temporarily unavailable")
		}
		if spent >= p.Org.CreditsMicros {
			return errf(402, "insufficient_quota", "insufficient_credits", "organization is out of credits")
		}
	}
	if p.Key.MonthlyBudget > 0 {
		spent, err := g.Usage.Spent(ctx, usage.KeySpendKey(p.Key.ID, time.Now()))
		if err != nil {
			return errf(503, "api_error", "store_unavailable", "temporarily unavailable")
		}
		if spent >= p.Key.MonthlyBudget {
			return errf(402, "insufficient_quota", "key_budget_exceeded", "API key monthly budget exceeded")
		}
	}
	if p.Key.TPM > 0 {
		used, err := g.Lim.Get(ctx, "t:"+p.Key.ID)
		if err == nil && used >= int64(p.Key.TPM) {
			return errf(429, "rate_limit_error", "tpm_exceeded", "tokens per minute limit reached")
		}
	}
	if p.Key.RPM > 0 {
		n, err := g.Lim.Incr(ctx, "r:"+p.Key.ID, 1)
		if err == nil && n > int64(p.Key.RPM) {
			return errf(429, "rate_limit_error", "rpm_exceeded", "requests per minute limit reached")
		}
	}
	return nil
}

// resolve finds the model and plans its targets; affinity yields the cache-affinity key (may be nil).
func (g *Gateway) resolve(p *principal, id, kind string, affinity func() string) (store.Model, []store.Target, *apiError) {
	snap := g.snap.Load()
	m, ok := snap.Model(p.Org.ID, id)
	if !ok {
		return m, nil, errf(404, "invalid_request_error", "model_not_found", "model %q not found", id)
	}
	if len(p.Key.Models) > 0 && !slices.Contains(p.Key.Models, m.ID) {
		return m, nil, errf(403, "permission_error", "model_not_allowed", "this API key may not use model %q", id)
	}
	if m.Kind != kind {
		return m, nil, errf(400, "invalid_request_error", "wrong_model_kind", "model %q is a %s model", id, m.Kind)
	}
	ts := plan(m, snap, g.rt, affinity)
	if !g.cfg.AllowPrivateURLs {
		ts = slices.DeleteFunc(ts, func(t store.Target) bool {
			return !tenantReachable(snap.Providers[t.ProviderID], false, false)
		})
	}
	if len(ts) == 0 {
		return m, nil, errf(503, "api_error", "no_provider", "no provider available for model %q", id)
	}
	return m, ts, nil
}

func bifrostTargets(snap *Snapshot, ts []store.Target) (schemas.ModelProvider, string, []schemas.Fallback) {
	fb := make([]schemas.Fallback, 0, len(ts)-1)
	for _, t := range ts[1:] {
		fb = append(fb, schemas.Fallback{Provider: BifrostKey(snap.Providers[t.ProviderID]), Model: t.Model})
	}
	return BifrostKey(snap.Providers[ts[0].ProviderID]), ts[0].Model, fb
}

// settle updates circuit breakers and routing stats from which provider ended up serving the
// request and returns that provider's name. For public models it also records per-provider
// health: every target tried before the serving one is a failure (latency 0), that one a success.
// Requests that failed at every target go through settleErr, which knows each target's status.
func (g *Gateway) settle(snap *Snapshot, m store.Model, ts []store.Target, served schemas.ModelProvider, failed bool, start time.Time, outTokens int) string {
	servedID := snap.byBifrost[served]
	if !failed && servedID == "" {
		return "" // nothing was served (client left before the first chunk): no verdict on targets
	}
	health := func(t store.Target, ok bool) {
		if m.OrgID != "" { // shortcut: only public health is shown; record private too when orgs get a health view
			return
		}
		var lat int64
		var out int
		if ok {
			lat, out = time.Since(start).Milliseconds(), outTokens
		}
		g.Usage.RecordHealth(m.ID, t.ProviderID, ok, lat, out, start)
	}
	for _, t := range ts {
		if !failed && t.ProviderID == servedID {
			g.rt.br.result(targetID(t), true)
			g.rt.observe(t, true, time.Since(start)) // shortcut: whole-request latency (streams too), not time to first token
			health(t, true)
			break
		}
		g.rt.br.result(targetID(t), false)
		g.rt.observe(t, false, 0)
		health(t, false)
	}
	return snap.Providers[servedID].Name
}

// TargetOpen reports whether the circuit breaker currently skips this provider target.
func (g *Gateway) TargetOpen(providerID, model string) bool {
	return !g.rt.br.ok(targetID(store.Target{ProviderID: providerID, Model: model}))
}

func cost(m store.Model, in, out int) int64 {
	return (int64(in)*m.InputPrice + int64(out)*m.OutputPrice + 500_000) / 1_000_000 // nearest micro-USD: unbiased over many requests
}

// record bills and logs one request and returns the micros billed.
func (g *Gateway) record(p *principal, m store.Model, l store.RequestLog, start time.Time) int64 {
	l.ID, l.OrgID, l.KeyID, l.Model, l.At = store.NewID(), p.Org.ID, p.Key.ID, m.ID, start.UTC()
	l.LatencyMs = time.Since(start).Milliseconds()
	if !l.Cached && l.Cost == 0 { // callers may price by something other than tokens (audio minutes)
		l.Cost = cost(m, l.InTokens, l.OutTokens)
	}
	if !l.Cached && l.Status == 200 {
		l.Cost += m.RequestPrice
	}
	if p.Key.TPM > 0 && l.InTokens+l.OutTokens > 0 {
		_, _ = g.Lim.Incr(context.Background(), "t:"+p.Key.ID, int64(l.InTokens+l.OutTokens))
	}
	g.Usage.Record(l)
	label, prov := m.ID, l.Provider
	if m.OrgID != "" { // private model ids and provider names stay out of the shared metrics
		label, prov = "private", "private"
	}
	mRequests.WithLabelValues(label, prov, strconv.Itoa(l.Status)).Inc()
	mLatency.WithLabelValues(label).Observe(time.Since(start).Seconds())
	mTokens.WithLabelValues(label, "in").Add(float64(l.InTokens))
	mTokens.WithLabelValues(label, "out").Add(float64(l.OutTokens))
	return l.Cost
}

// setCost sets X-GW-Cost-Micros on a non-stream response (streams have sent headers already).
func setCost(w http.ResponseWriter, micros int64) {
	w.Header().Set("X-GW-Cost-Micros", strconv.FormatInt(micros, 10))
}

func (g *Gateway) readBody(w http.ResponseWriter, r *http.Request) ([]byte, *apiError) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.cfg.MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, errf(413, "invalid_request_error", "body_too_large", "request body exceeds %d bytes", g.cfg.MaxBodyBytes)
		}
		return nil, errf(400, "invalid_request_error", "bad_body", "cannot read body")
	}
	return body, nil
}

// ---- handlers ----

// chatOut adds the serving provider; hideExtra drops Bifrost's internal extra_fields.
type chatOut struct {
	*schemas.BifrostChatResponse
	Provider string `json:"provider,omitempty"`
}

func (g *Gateway) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()
	p, e := g.authenticate(ctx, r)
	if e != nil {
		writeError(w, e)
		return
	}
	body, e := g.readBody(w, r)
	if e != nil {
		writeError(w, e)
		return
	}
	var req openai.OpenAIChatRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || len(req.Messages) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a chat completion request with model and messages"))
		return
	}
	m, ts, e := g.resolve(p, req.Model, "chat", func() string {
		i := slices.IndexFunc(req.Messages, func(m openai.OpenAIMessage) bool { return m.Role == schemas.ChatMessageRoleUser })
		if i < 0 {
			i = 0
		}
		return affinityKey(req.PromptCacheKey, req.Messages[:i], req.Tools)
	})
	if e != nil {
		writeError(w, e)
		return
	}
	if e := g.admit(ctx, p); e != nil {
		writeError(w, e)
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostChatRequest(bctx)
	var gin guardInput
	breq.Input, gin = chatGuard(p.Org.Guardrails, breq.Input)
	if !g.guardIn(w, p, m, gin, start) {
		return
	}
	if req.MaxTokens != nil { // Bifrost reads max_completion_tokens
		breq.Params.MaxCompletionTokens = req.MaxTokens
	}
	if c := p.Org.MaxTokensCap; c > 0 && (breq.Params.MaxCompletionTokens == nil || *breq.Params.MaxCompletionTokens > c) {
		breq.Params.MaxCompletionTokens = &c
	}
	snap := g.snap.Load()
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	usesMCP := g.withMCP(bctx, r, p)
	w.Header().Set("x-gateway-request-id", store.NewID())

	if req.Stream != nil && *req.Stream {
		g.stream(w, bctx, p, m, ts, breq, start)
		return
	}

	cacheKey, ttl := "", cacheTTL(r)
	if ttl > 0 && !usesMCP && g.cfg.CacheMB > 0 {
		h := sha256.Sum256(append([]byte(p.Org.ID+"\x00"), body...))
		cacheKey = hex.EncodeToString(h[:])
		if out, ok := g.resp.Get(cacheKey); ok {
			mCacheHits.Inc()
			setCost(w, g.record(p, m, store.RequestLog{Status: 200, Cached: true, Provider: "cache"}, start))
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-gateway-cache-hit", "true")
			_, _ = w.Write(out)
			return
		}
	}

	done := g.rt.begin(ts[0])
	resp, berr := g.BF.ChatCompletionRequest(bctx, breq)
	done()
	if berr != nil {
		ae := fromBifrost(berr, m.OrgID != "")
		g.settleErr(snap, m, ts, berr, start)
		g.record(p, m, store.RequestLog{Status: ae.status, Error: ae.msg}, start)
		writeError(w, ae)
		return
	}
	l := store.RequestLog{Status: 200, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if resp.Usage != nil {
		l.InTokens, l.OutTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	provName := g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, l.OutTokens)
	l.Provider = provName
	resp.Model = m.ID
	out, err := hideExtra(chatOut{BifrostChatResponse: resp, Provider: provName})
	if err != nil {
		writeError(w, errf(500, "api_error", "encode", "cannot encode response"))
		return
	}
	if cacheKey != "" {
		g.resp.Set(cacheKey, out, len(out), ttl)
	}
	setCost(w, g.record(p, m, l, start))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-gateway-provider", provName)
	w.Header().Set("X-GW-Route", routeHeader(m, provName, resp.ExtraFields.RoutingInfo.Model))
	_, _ = w.Write(out)
}

// routeHeader is the X-GW-Route value: "<strategy>; <provider name>/<upstream model>".
func routeHeader(m store.Model, provName, upstream string) string {
	return strategyName(m) + "; " + provName + "/" + upstream
}

func cacheTTL(r *http.Request) time.Duration {
	n, err := strconv.Atoi(r.Header.Get("x-gateway-cache"))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(min(n, 86400)) * time.Second
}

func (g *Gateway) stream(w http.ResponseWriter, ctx *schemas.BifrostContext, p *principal, m store.Model, ts []store.Target, breq *schemas.BifrostChatRequest, start time.Time) {
	snap := g.snap.Load()
	defer g.rt.begin(ts[0])()
	ch, berr := g.BF.ChatCompletionStreamRequest(ctx, breq)
	if berr != nil {
		ae := fromBifrost(berr, m.OrgID != "")
		g.settleErr(snap, m, ts, berr, start)
		g.record(p, m, store.RequestLog{Status: ae.status, Error: ae.msg, Stream: true}, start)
		writeError(w, ae)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	l := store.RequestLog{Status: 200, Stream: true}
	var served schemas.ModelProvider
	var chars int
	clientGone := false
	for chunk := range ch { // always drain: Bifrost owns the channel
		if clientGone {
			continue
		}
		var data []byte
		if chunk.BifrostError != nil {
			ae := fromBifrost(chunk.BifrostError, m.OrgID != "")
			l.Status, l.Error = ae.status, ae.msg
			data, _ = json.Marshal(map[string]any{"error": map[string]any{"message": ae.msg, "type": ae.typ, "code": ae.code}})
		} else if c := chunk.BifrostChatResponse; c != nil {
			if served == "" { // headers are still unsent before the first chunk
				name := snap.Providers[snap.byBifrost[c.ExtraFields.RoutingInfo.Provider]].Name
				w.Header().Set("x-gateway-provider", name)
				w.Header().Set("X-GW-Route", routeHeader(m, name, c.ExtraFields.RoutingInfo.Model))
			}
			served = c.ExtraFields.RoutingInfo.Provider
			if c.Usage != nil {
				l.InTokens, l.OutTokens = c.Usage.PromptTokens, c.Usage.CompletionTokens
			}
			for _, ch := range c.Choices {
				if ch.ChatStreamResponseChoice != nil && ch.Delta != nil && ch.Delta.Content != nil {
					chars += len(*ch.Delta.Content)
				}
			}
			c.Model = m.ID
			data, _ = hideExtra(chatOut{BifrostChatResponse: c})
		} else {
			continue
		}
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) // a stalled reader cannot pin the stream
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil || rc.Flush() != nil {
			clientGone = true
			l.Status, l.Error = 499, "client closed connection"
			continue
		}
	}
	if !clientGone {
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = rc.Flush()
	}
	if l.OutTokens == 0 && chars > 0 {
		l.OutTokens = (chars + 3) / 4 // shortcut: estimate when the provider sends no usage
	}
	l.Provider = g.settle(snap, m, ts, served, targetFault(l.Status), start, l.OutTokens)
	g.record(p, m, l, start)
}

func (g *Gateway) Embeddings(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()
	p, e := g.authenticate(ctx, r)
	if e != nil {
		writeError(w, e)
		return
	}
	body, e := g.readBody(w, r)
	if e != nil {
		writeError(w, e)
		return
	}
	var req openai.OpenAIEmbeddingRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || req.Input == nil {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be an embeddings request with model and input"))
		return
	}
	m, ts, e := g.resolve(p, req.Model, "embedding", nil)
	if e != nil {
		writeError(w, e)
		return
	}
	if e := g.admit(ctx, p); e != nil {
		writeError(w, e)
		return
	}
	snap := g.snap.Load()
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostEmbeddingRequest(bctx)
	var segs []*string
	for _, item := range breq.Input {
		for _, part := range item.Content {
			if part.Text != nil {
				segs = append(segs, part.Text)
			}
		}
	}
	if !g.guard(w, p, m, segs, start) {
		return
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.EmbeddingRequest(bctx, breq)
	if berr != nil {
		ae := fromBifrost(berr, m.OrgID != "")
		g.settleErr(snap, m, ts, berr, start)
		g.record(p, m, store.RequestLog{Status: ae.status, Error: ae.msg}, start)
		writeError(w, ae)
		return
	}
	provName := g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0)
	l := store.RequestLog{Status: 200, Provider: provName}
	if resp.Usage != nil {
		l.InTokens = resp.Usage.PromptTokens
	}
	setCost(w, g.record(p, m, l, start))
	resp.Model = m.ID
	out, _ := hideExtra(resp)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-gateway-provider", provName)
	_, _ = w.Write(out)
}

// ModelInfo is the public description of a model (OpenAI list format plus pricing).
type ModelInfo struct {
	ID            string   `json:"id"`
	Object        string   `json:"object"`
	Created       int64    `json:"created"`
	OwnedBy       string   `json:"owned_by"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Kind          string   `json:"kind"`
	ContextLength int      `json:"context_length,omitempty"`
	MaxOutput     int      `json:"max_output_tokens,omitempty"`
	Modalities    []string `json:"modalities,omitempty"`
	Pricing       struct {
		Input       string `json:"input"`                  // USD per 1M tokens (speech: per 1M characters without token usage)
		Output      string `json:"output"`                 // USD per 1M tokens
		AudioMinute string `json:"audio_minute,omitempty"` // USD per minute of audio (transcription)
		Image       string `json:"image,omitempty"`        // USD per generated image
		Request     string `json:"request,omitempty"`      // USD per request
		VideoSecond string `json:"video_second,omitempty"` // USD per second of generated video
	} `json:"pricing"`
	Private   bool     `json:"private,omitempty"`
	Providers []string `json:"providers,omitempty"` // public models only: names of serving providers
}

func Info(m store.Model) ModelInfo {
	owner, _, _ := strings.Cut(m.ID, "/")
	mi := ModelInfo{ID: m.ID, Object: "model", Created: m.CreatedAt.Unix(), OwnedBy: owner, Name: m.Name,
		Description: m.Description, Kind: m.Kind, ContextLength: m.ContextLength, MaxOutput: m.MaxOutput, Modalities: m.Modalities, Private: m.OrgID != ""}
	mi.Pricing.Input = usd(m.InputPrice)
	mi.Pricing.Output = usd(m.OutputPrice)
	if m.AudioMinutePrice > 0 {
		mi.Pricing.AudioMinute = usd(m.AudioMinutePrice)
	}
	if m.ImagePrice > 0 {
		mi.Pricing.Image = usd(m.ImagePrice)
	}
	if m.RequestPrice > 0 {
		mi.Pricing.Request = usd(m.RequestPrice)
	}
	if m.VideoSecondPrice > 0 {
		mi.Pricing.VideoSecond = usd(m.VideoSecondPrice)
	}
	return mi
}

// Info is Info(m) plus, for public models, the names of its providers in this snapshot.
func (s *Snapshot) Info(m store.Model) ModelInfo {
	mi := Info(m)
	if m.OrgID != "" {
		return mi
	}
	for _, t := range m.Targets {
		if p, ok := s.Providers[t.ProviderID]; ok && !slices.Contains(mi.Providers, p.Name) {
			mi.Providers = append(mi.Providers, p.Name)
		}
	}
	return mi
}

func usd(micros int64) string { return strconv.FormatFloat(float64(micros)/1e6, 'f', -1, 64) }

func (g *Gateway) Models(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	data := []ModelInfo{}
	snap := g.snap.Load()
	for _, m := range snap.Visible(p.Org.ID) {
		if len(p.Key.Models) == 0 || slices.Contains(p.Key.Models, m.ID) {
			data = append(data, snap.Info(m))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
