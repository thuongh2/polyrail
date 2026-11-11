// Package broadcast exports one OpenTelemetry span per gateway request to org and platform trace
// exports (store.TraceExport, D30): OTLP/HTTP JSON to any collector (Tempo, Jaeger, Datadog,
// Honeycomb, ...) or to Langfuse's OTLP endpoint. Spans wait in bounded per-export buffers and go
// out in batches of up to 100 or every 2 seconds, off the request path.
package broadcast

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/webhook"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	BatchMax       = 100 // spans per request to the endpoint
	BatchWait      = 2 * time.Second
	MaxPerScope    = 5 // exports per org, and on the platform
	retries        = 3 // after the first attempt, on network errors, 429 and 5xx
	disableAfter   = 10
	timeout        = 10 * time.Second
	reloadEvery    = 10 * time.Second
	queueShare     = 10 // an org's exports may hold 1/queueShare of the span capacity
	responseDrain  = 64 << 10
	defaultBackoff = time.Second
	langfusePath   = "/api/public/otel/v1/traces"
)

var mSpans = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_trace_spans_total", Help: "Trace export spans by result (ok, failed, dropped = buffer full).",
}, []string{"result"})

func init() { prometheus.MustRegister(mSpans) }

// URL is where an export's spans are posted.
func URL(t store.TraceExport) string {
	if t.Type == "langfuse" {
		return strings.TrimRight(t.Endpoint, "/") + langfusePath
	}
	return t.Endpoint
}

// LangfuseAuth is the Authorization header for a Langfuse project's key pair.
func LangfuseAuth(publicKey, secretKey string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(publicKey+":"+secretKey))
}

// CheckURL validates an export URL like a webhook's: https on a public host, or any http(s)
// URL when the operator allows private URLs.
func CheckURL(t store.TraceExport, allowPrivate bool) error {
	return webhook.CheckURL(URL(t), allowPrivate)
}

// sampled reports whether request id falls in the sampled fraction rate. The decision is a hash
// of the id, so every export with the same rate keeps the same requests.
func sampled(id string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	h := fnv.New64a()
	h.Write([]byte(id))
	return float64(h.Sum64()>>11)/(1<<53) < rate
}

type sink struct {
	cfg      store.TraceExport
	buf      []json.RawMessage
	first    time.Time // when the oldest buffered span arrived
	inflight bool
}

type batch struct {
	s     *sink
	cfg   store.TraceExport
	spans []json.RawMessage
}

// Exporter buffers spans per export and posts them from a few workers.
type Exporter struct {
	S            *store.Store
	AllowPrivate bool
	Backoff      time.Duration // delay before the first retry, doubled per retry

	client   *http.Client
	capacity int // spans buffered or in flight, across all exports
	active   atomic.Pointer[map[string]bool]
	kick     chan struct{}
	jobs     chan batch

	mu      sync.Mutex
	sinks   map[string]*sink   // by export id
	byOrg   map[string][]*sink // "" = platform
	pending map[string]int     // spans held per export owner (org id, "" = platform)
	total   int
}

// New makes an exporter holding at most capacity spans; call Start to send.
func New(s *store.Store, allowPrivate bool, capacity int) *Exporter {
	dialer := gateway.PublicDialer(timeout)
	if allowPrivate {
		dialer = &net.Dialer{Timeout: timeout}
	}
	tr := &http.Transport{
		Proxy:               nil, // an egress proxy would hide the destination from the dial-time check
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: timeout,
		MaxIdleConns:        32,
		IdleConnTimeout:     90 * time.Second,
	}
	e := &Exporter{S: s, AllowPrivate: allowPrivate, Backoff: defaultBackoff, capacity: max(capacity, BatchMax),
		client: &http.Client{Transport: tr, Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		kick: make(chan struct{}, 1), sinks: map[string]*sink{}, byOrg: map[string][]*sink{}, pending: map[string]int{}}
	e.active.Store(&map[string]bool{})
	return e
}

// Start loads the exports and runs the flusher, workers and the periodic reload until ctx ends.
// Exports changed on another node are picked up within reloadEvery.
// shortcut: a worker waits out one export's retries; move retries to a delay queue if slow
// endpoints start holding up other exports.
func (e *Exporter) Start(ctx context.Context, workers int) {
	e.jobs = make(chan batch, workers)
	if err := e.Reload(ctx); err != nil {
		slog.Error("trace exports load failed", "err", err)
	}
	for range workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case b := <-e.jobs:
					e.deliver(ctx, b)
				}
			}
		}()
	}
	go func() {
		tick := time.NewTicker(BatchWait / 8)
		defer tick.Stop()
		reload := time.NewTicker(reloadEvery)
		defer reload.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reload.C:
				if err := e.Reload(ctx); err != nil {
					slog.Error("trace exports reload failed", "err", err)
				}
			case <-tick.C:
			case <-e.kick:
			}
			e.flush(time.Now())
		}
	}()
}

// Reload reads every export from the store; buffered spans of exports that remain are kept,
// those of deleted or disabled exports are dropped. Admin writes call it on this node.
func (e *Exporter) Reload(ctx context.Context) error {
	all, err := e.S.ListAllTraceExports(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	sinks, byOrg := make(map[string]*sink, len(all)), map[string][]*sink{}
	for _, t := range all {
		s := e.sinks[t.ID]
		if s == nil {
			s = &sink{}
		}
		s.cfg = t
		if !t.Enabled {
			e.drop(s)
		}
		sinks[t.ID] = s
		byOrg[t.OrgID] = append(byOrg[t.OrgID], s)
	}
	for id, s := range e.sinks {
		if sinks[id] == nil {
			e.drop(s)
		}
	}
	e.sinks, e.byOrg = sinks, byOrg
	e.publish()
	return nil
}

// publish rebuilds the set of owners with an enabled export (Wants); e.mu held.
func (e *Exporter) publish() {
	act := map[string]bool{}
	for org, ss := range e.byOrg {
		if slices.ContainsFunc(ss, func(s *sink) bool { return s.cfg.Enabled }) {
			act[org] = true
		}
	}
	e.active.Store(&act)
}

// drop discards s's buffered spans; e.mu held.
func (e *Exporter) drop(s *sink) {
	if n := len(s.buf); n > 0 {
		e.release(s.cfg.OrgID, n)
		mSpans.WithLabelValues("dropped").Add(float64(n))
		s.buf = nil
	}
}

func (e *Exporter) release(owner string, n int) {
	e.total -= n
	if e.pending[owner] -= n; e.pending[owner] <= 0 {
		delete(e.pending, owner)
	}
}

// Wants reports whether org or the platform has an enabled export (gateway.Tracer).
func (e *Exporter) Wants(orgID string) bool {
	act := *e.active.Load()
	return act[orgID] || act[""]
}

// Export buffers tr's span for each enabled export of its org and of the platform whose sample
// and key filter it passes; it never blocks. A span is dropped (and counted) when the exporter is
// full or the export's owner already holds its share, so one org cannot crowd out the others.
func (e *Exporter) Export(tr gateway.Trace) {
	l := tr.Log
	var plain, full json.RawMessage
	e.mu.Lock()
	defer e.mu.Unlock()
	share := max(e.capacity/queueShare, min(e.capacity, 1000))
	kicked := false
	for _, s := range append(slices.Clip(e.byOrg[l.OrgID]), e.byOrg[""]...) {
		c := s.cfg
		if !c.Enabled || (len(c.KeyIDs) > 0 && !slices.Contains(c.KeyIDs, l.KeyID)) || !sampled(l.ID, c.SampleRate) ||
			(store.WSID(c.WorkspaceID) != "" && store.WSID(c.WorkspaceID) != l.Workspace) { // workspace exports see their keys only (D31)
			continue
		}
		if e.total >= e.capacity || e.pending[c.OrgID] >= share {
			mSpans.WithLabelValues("dropped").Inc()
			continue
		}
		var sp json.RawMessage
		if c.IncludeContent && (tr.Request != "" || tr.Response != "") { // set only when the org logs content
			if full == nil {
				full = spanOf(tr, true)
			}
			sp = full
		} else {
			if plain == nil {
				plain = spanOf(tr, false)
			}
			sp = plain
		}
		if len(s.buf) == 0 {
			s.first = time.Now()
		}
		s.buf = append(s.buf, sp)
		e.total++
		e.pending[c.OrgID]++
		if len(s.buf) >= BatchMax && !kicked {
			kicked = true
			select {
			case e.kick <- struct{}{}:
			default:
			}
		}
	}
}

// flush hands each export whose buffer is full or old enough one batch, at most one in flight
// per export.
func (e *Exporter) flush(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.sinks {
		if s.inflight || len(s.buf) == 0 || (len(s.buf) < BatchMax && now.Sub(s.first) < BatchWait) {
			continue
		}
		n := min(len(s.buf), BatchMax)
		b := batch{s: s, cfg: s.cfg, spans: s.buf[:n:n]}
		select {
		case e.jobs <- b:
			s.inflight = true
			s.buf = s.buf[n:]
			s.first = now
		default:
			return // workers busy; the next tick tries again
		}
	}
}

// deliver posts a batch with retries, then updates the export's consecutive failure count;
// the 10th consecutive failure disables it.
func (e *Exporter) deliver(ctx context.Context, b batch) {
	body := payload(b.spans)
	ok := false
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.Backoff << (attempt - 1)):
			}
		}
		status, err := e.post(ctx, b.cfg, body)
		if err == nil && status >= 200 && status < 300 {
			ok = true
			break
		}
		if err == nil && status < 500 && status != http.StatusTooManyRequests {
			break // the collector rejected it; retrying will not help
		}
	}
	result := "ok"
	if !ok {
		result = "failed"
	}
	mSpans.WithLabelValues(result).Add(float64(len(b.spans)))

	var disabled bool
	if !ok || b.cfg.Failures > 0 {
		t, err := e.S.UpdateTraceExport(ctx, b.cfg.OrgID, b.cfg.ID, func(t *store.TraceExport) error {
			if ok {
				t.Failures = 0
				return nil
			}
			t.Failures++
			if t.Failures >= disableAfter && t.Enabled {
				t.Enabled = false
				slog.Warn("trace export disabled after consecutive failures", "org", t.OrgID, "export", t.ID, "failures", t.Failures)
			}
			return nil
		})
		switch {
		case err == nil:
			b.cfg.Failures, disabled = t.Failures, !t.Enabled
		case !errors.Is(err, store.ErrNotFound):
			slog.Error("trace export state update failed", "export", b.cfg.ID, "err", err)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.release(b.cfg.OrgID, len(b.spans))
	b.s.inflight = false
	if e.sinks[b.cfg.ID] == b.s { // not reloaded away meanwhile
		b.s.cfg.Failures = b.cfg.Failures
		if disabled {
			b.s.cfg.Enabled = false
			e.drop(b.s)
			e.publish()
		}
	}
}

// Send posts one test span to t now and returns the HTTP status; the failure count is not
// changed. The collector's response body is never returned.
func (e *Exporter) Send(ctx context.Context, t store.TraceExport, orgID string) (int, error) {
	now := time.Now().UTC()
	sp := spanOf(gateway.Trace{Kind: "chat", System: "polyrail", Log: store.RequestLog{ID: store.NewID(), OrgID: orgID,
		Model: "polyrail/test", Provider: "polyrail", Status: 200, At: now}}, false)
	return e.post(ctx, t, payload([]json.RawMessage{sp}))
}

// post re-validates the URL (DNS may have changed since save) and makes one request.
func (e *Exporter) post(ctx context.Context, t store.TraceExport, body []byte) (int, error) {
	if err := CheckURL(t, e.AllowPrivate); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, URL(t), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Polyrail-Trace/1")
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responseDrain))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
