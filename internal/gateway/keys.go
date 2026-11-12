package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// Provider key status (docs/KEYS.md). Cooldown lives in memory per node; disabled and
// quarantined are persisted on store.ProviderKey.
const (
	KeyAvailable   = "available"
	KeyCooldown    = "cooldown"
	KeyQuarantined = "quarantined"
	KeyDisabled    = "disabled"

	keyCooldownBase    = 30 * time.Second
	keyCooldownMax     = 5 * time.Minute
	keyQuarantineAfter = 3 // consecutive upstream 401/403
)

// Key events, emitted to webhooks and alert channels (gateway.Events) with the provider's org.
const (
	EventKeyQuarantined      = "key.quarantined"
	EventKeyRecovered        = "key.recovered"
	EventProviderUnavailable = "provider.unavailable"
)

type keyState struct {
	until  time.Time // cooling down until
	streak int       // consecutive failures, for the doubling cooldown
	auth   int       // consecutive auth failures
	last   time.Time // last attempt on this node
}

// keyHealth is this node's view of each provider key's recent upstream answers.
// shortcut: one entry per key ever used on this node (bounded by configured keys); entries of
// deleted keys stay until restart.
type keyHealth struct {
	mu   sync.Mutex
	st   map[string]*keyState
	busy map[string]bool // keys whose persisted status is being written
	now  func() time.Time
}

func newKeyHealth() *keyHealth {
	return &keyHealth{st: map[string]*keyState{}, busy: map[string]bool{}, now: time.Now}
}

// cooling reports whether key id is cooling down, and until when.
func (k *keyHealth) cooling(id string) (time.Time, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s := k.st[id]; s != nil && k.now().Before(s.until) {
		return s.until, true
	}
	return time.Time{}, false
}

func (k *keyHealth) lastUsed(id string) time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s := k.st[id]; s != nil {
		return s.last
	}
	return time.Time{}
}

// result applies one upstream answer on key id (status < 400 = success) and reports whether the
// key has now failed authentication keyQuarantineAfter times in a row. retryAfter is the
// upstream's Retry-After hint (0 = none).
func (k *keyHealth) result(id string, status int, retryAfter time.Duration) (quarantine bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	s := k.st[id]
	if s == nil {
		s = &keyState{}
		k.st[id] = s
	}
	now := k.now()
	s.last = now
	switch {
	case status < 400:
		s.until, s.streak, s.auth = time.Time{}, 0, 0
		return false
	case !targetFault(status):
		return false // the request was wrong, not the key
	}
	s.streak++
	d := min(keyCooldownBase<<min(s.streak-1, 10), keyCooldownMax)
	if retryAfter > 0 {
		d = min(retryAfter, keyCooldownMax)
	}
	s.until = now.Add(d)
	if status == 401 || status == 403 {
		s.auth++
		return s.auth >= keyQuarantineAfter
	}
	return false
}

// clear forgets key id's cooldown and failure counts (its last use stays).
func (k *keyHealth) clear(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s := k.st[id]; s != nil {
		k.st[id] = &keyState{last: s.last}
	}
}

func (k *keyHealth) claim(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.busy[id] {
		return false
	}
	k.busy[id] = true
	return true
}

func (k *keyHealth) release(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.busy, id)
}

// MaskKey shows a key value as its first 4 and last 4 characters around "****"; values shorter
// than 12 characters show nothing.
func MaskKey(v string) string {
	if len(v) < 12 {
		return "****"
	}
	return v[:4] + "****" + v[len(v)-4:]
}

// KeyStatus is key k's status on this node, with the cooldown end when cooling down.
func (g *Gateway) KeyStatus(k store.ProviderKey) (string, *time.Time) {
	switch {
	case k.Disabled:
		return KeyDisabled, nil
	case k.Quarantined:
		return KeyQuarantined, nil
	}
	if until, ok := g.keys.cooling(k.ID); ok {
		return KeyCooldown, &until
	}
	return KeyAvailable, nil
}

// KeyLastUsed is when this node last sent a request with key id (zero: not since start).
func (g *Gateway) KeyLastUsed(id string) time.Time { return g.keys.lastUsed(id) }

// ClearKey resets key id's cooldown on this node.
func (g *Gateway) ClearKey(id string) { g.keys.clear(id) }

var errUnchanged = errors.New("unchanged")

// SetKeyQuarantine persists key kid's quarantine flag on provider pid of org ("" = platform),
// reloads the catalog and emits key.quarantined / key.recovered when the flag changed. Lifting a
// quarantine also clears the key's cooldown on this node.
func (g *Gateway) SetKeyQuarantine(ctx context.Context, org, pid, kid string, q bool, reason string) error {
	_, err := g.S.UpdateProviderKeys(ctx, org, pid, func(p *store.Provider) error {
		i := slices.IndexFunc(p.Keys, func(k store.ProviderKey) bool { return k.ID == kid })
		if i < 0 {
			return store.ErrNotFound
		}
		if p.Keys[i].Quarantined == q {
			return errUnchanged
		}
		p.Keys[i].Quarantined, p.Keys[i].StatusReason = q, ""
		if q {
			p.Keys[i].StatusReason = reason
		}
		return nil
	})
	if !q {
		g.keys.clear(kid)
	}
	if errors.Is(err, errUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := g.Reload(ctx); err != nil {
		slog.Error("catalog reload after key status change failed", "err", err)
	}
	typ := EventKeyRecovered
	if q {
		typ = EventKeyQuarantined
	}
	g.emit(org, typ, g.keyEventData(pid, kid, reason))
	return nil
}

// keyEventData describes a key for events: ids, the provider name and the key's name or masked
// value. No upstream text.
func (g *Gateway) keyEventData(pid, kid, reason string) map[string]any {
	d := map[string]any{"provider_id": pid, "key_id": kid}
	if reason != "" {
		d["reason"] = reason
	}
	p, ok := g.snap.Load().Providers[pid]
	if !ok {
		return d
	}
	d["provider"] = p.Name
	if i := slices.IndexFunc(p.Keys, func(k store.ProviderKey) bool { return k.ID == kid }); i >= 0 {
		d["key_name"] = p.Keys[i].Name
		if p.Keys[i].Name == "" {
			d["key_name"] = MaskKey(p.Keys[i].Value)
		}
	}
	return d
}

// keyResult records one attempt on provider key kid: per-key stats, cooldown, and quarantine
// (or its lifting) when that changes the persisted status.
func (g *Gateway) keyResult(snap *Snapshot, kid string, status int, retryAfter, latency time.Duration, in, out int) {
	pid, ok := snap.keyOwner[kid]
	if !ok {
		return
	}
	p := snap.Providers[pid]
	i := slices.IndexFunc(p.Keys, func(k store.ProviderKey) bool { return k.ID == kid })
	if i < 0 {
		return
	}
	pk := p.Keys[i]
	success := status < 400
	if success || targetFault(status) {
		g.Usage.RecordKey(pid, kid, success, latency.Milliseconds(), in, out, time.Now())
	}
	quarantine := g.keys.result(kid, status, retryAfter)
	switch {
	case quarantine && !pk.Quarantined:
		g.persistKey(p.OrgID, pid, kid, true, fmt.Sprintf("%d consecutive authentication failures (status %d)", keyQuarantineAfter, status))
	case success && pk.Quarantined: // served while every key was out: it works again
		g.persistKey(p.OrgID, pid, kid, false, "")
	}
}

// persistKey writes a quarantine change in the background, once at a time per key.
func (g *Gateway) persistKey(org, pid, kid string, q bool, reason string) {
	if !g.keys.claim(kid) {
		return
	}
	go func() {
		defer g.keys.release(kid)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := g.SetKeyQuarantine(ctx, org, pid, kid, q, reason); err != nil {
			slog.Error("key status write failed", "provider", pid, "key", kid, "err", err)
		}
	}()
}

// settleKeys records each provider key attempt of a finished request from Bifrost's attempt trail
// (key id, status, Retry-After per attempt) on bctx: failed attempts count against their key; the
// last attempt counts as a success when the request succeeded (ok). be is the final error, whose
// RoutingInfo.Key names the primary's key when the trail holds a fallback's attempts.
func (g *Gateway) settleKeys(bctx *schemas.BifrostContext, snap *Snapshot, ok bool, be *schemas.BifrostError, start time.Time, in, out int) {
	if bctx == nil {
		return
	}
	trail, _ := bctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	seen := map[string]bool{}
	for i, a := range trail {
		seen[a.KeyID] = true
		failed := a.FailureClass != "" || (a.StatusCode != nil && *a.StatusCode >= 400)
		switch {
		case a.FailureClass == schemas.FailureClassCancelled:
		case failed:
			g.keyResult(snap, a.KeyID, statusOf(a.StatusCode), time.Duration(a.RetryAfter)*time.Millisecond, 0, 0, 0)
		case ok && i == len(trail)-1:
			g.keyResult(snap, a.KeyID, 200, 0, time.Since(start), in, out)
		}
	}
	if be != nil && be.ExtraFields.RoutingInfo.Key != "" && !seen[be.ExtraFields.RoutingInfo.Key] {
		g.keyResult(snap, be.ExtraFields.RoutingInfo.Key, statusOf(be.StatusCode), time.Duration(be.ExtraFields.RetryAfter)*time.Millisecond, 0, 0, 0)
	}
}

// configOnlyList are the types whose Bifrost v1.15.2 adapter lists models from key config alone
// (no upstream request; replicate only calls out for deployment keys, which this schema lacks).
var configOnlyList = map[string]bool{"fireworks": true, "byteplus": true, "replicate": true}

// TestKeyResult is the outcome of a pinned key test.
type TestKeyResult struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// TestKey sends one request pinned to key kid of provider pid (Bifrost's direct-key context
// value, so status filtering does not apply): a list-models call, or a 1-token chat on model.
// Upstream error text is shown only for platform providers (sanitized otherwise). Success lifts a
// quarantine and clears the cooldown; failures count like traffic does.
func (g *Gateway) TestKey(ctx context.Context, pid, kid, model string) (TestKeyResult, error) {
	snap := g.snap.Load()
	p, ok := snap.Providers[pid]
	if !ok {
		return TestKeyResult{}, fmt.Errorf("%w: the provider is disabled", store.ErrInvalid)
	}
	i := slices.IndexFunc(p.Keys, func(k store.ProviderKey) bool { return k.ID == kid })
	if i < 0 {
		return TestKeyResult{}, store.ErrNotFound
	}
	if p.OrgID != "" && !tenantReachable(p, g.cfg.AllowPrivateURLs, true) {
		return TestKeyResult{Status: 502, Error: "provider host is not reachable from this gateway"}, nil
	}
	if model == "" && configOnlyList[p.Type] && !orgCompatible(p) { // org fireworks lists live on the openai base
		// Bifrost answers these types' model list from the key's config without calling the
		// vendor, so it would report a wrong key or URL as working.
		return TestKeyResult{Error: p.Type + " has no model list to call: test this key with a model"}, nil
	}
	w := p.Keys[i].Weight
	if w <= 0 {
		w = 1
	}
	key := withServerURL(p, bifrostKey(p.Type, p.Keys[i], w))
	bctx := schemas.NewBifrostContext(ctx, time.Now().Add(30*time.Second))
	bctx.SetValue(schemas.BifrostContextKeyDirectKey, key)
	start := time.Now()
	var be *schemas.BifrostError
	if model != "" {
		one := 1
		_, be = g.BF.ChatCompletionRequest(bctx, &schemas.BifrostChatRequest{Provider: BifrostKey(p), Model: model,
			Input:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("ping")}}},
			Params: &schemas.ChatParameters{MaxCompletionTokens: &one}})
	} else {
		_, be = g.BF.ListModelsRequest(bctx, &schemas.BifrostListModelsRequest{Provider: BifrostKey(p)})
	}
	res := TestKeyResult{OK: be == nil, Status: 200, LatencyMs: time.Since(start).Milliseconds()}
	if be != nil {
		res.Status = statusOf(be.StatusCode)
		res.Error = fromBifrost(be, p.OrgID != "").msg
		if v := p.Keys[i].Value; len(v) >= 8 { // an upstream may echo the key in its message
			res.Error = strings.ReplaceAll(res.Error, v, MaskKey(v))
		}
		g.keyResult(snap, kid, res.Status, time.Duration(be.ExtraFields.RetryAfter)*time.Millisecond, 0, 0, 0)
		return res, nil
	}
	g.Usage.RecordKey(pid, kid, true, res.LatencyMs, 0, 0, time.Now())
	g.keys.result(kid, 200, 0) // clears the cooldown, sets last used
	if p.Keys[i].Quarantined {
		if err := g.SetKeyQuarantine(ctx, p.OrgID, pid, kid, false, ""); err != nil {
			return res, err
		}
	}
	return res, nil
}

// explainKeys adds what the provider said to Bifrost's "all configured keys returned permanent
// per-key errors": core collapses every key failing with an account-level error (401/402/403)
// into that 502 and keeps the reasons only in the attempt trail, which left operators guessing
// (a wrong key, or a plan without the model). Tenants on private routes still get only the status
// (fromBifrost), and secrets in the reason are redacted there too.
func explainKeys(bctx *schemas.BifrostContext, be *schemas.BifrostError) {
	if be == nil || be.ExtraFields.ErrorType != schemas.ErrorTypeProviderCredentialsExhausted || be.Error == nil || bctx == nil {
		return
	}
	trail, _ := bctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	for i := len(trail) - 1; i >= 0; i-- {
		a := trail[i]
		if a.StatusCode == nil {
			continue
		}
		reason := ""
		if a.FailReason != nil {
			reason = ": " + *a.FailReason
		}
		be.Error.Message = fmt.Sprintf("the provider refused every key; last refusal was HTTP %d%s (check the API key and that the account's plan includes this model)", *a.StatusCode, reason)
		return
	}
}
