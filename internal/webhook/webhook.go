// Package webhook delivers signed org events (request.failed, budget.exceeded, key.created,
// key.revoked, test.ping) to the org's HTTPS endpoints from a bounded in-memory queue.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Event types. "*" in a webhook's events subscribes to all of them.
const (
	RequestFailed  = "request.failed"
	BudgetExceeded = "budget.exceeded"
	KeyCreated     = "key.created"
	KeyRevoked     = "key.revoked"
	TestPing       = "test.ping"
)

// Types are the event types a webhook may subscribe to.
var Types = []string{RequestFailed, BudgetExceeded, KeyCreated, KeyRevoked, TestPing}

const (
	timeout        = 10 * time.Second
	retries        = 3  // after the first attempt, on network errors and 5xx
	disableAfter   = 10 // consecutive failed deliveries
	dedupeWindow   = time.Minute
	maxDedupeKeys  = 10_000
	queueShare     = 10 // an org may hold 1/queueShare of the queue (queued or being delivered); 16 minimum
	MaxPerOrg      = 20
	maxURLLen      = 2048
	responseDrain  = 64 << 10
	defaultBackoff = time.Second
)

// dedupeOn names the data fields that, with org and type, limit an event to one per minute.
var dedupeOn = map[string][]string{RequestFailed: {"model"}, BudgetExceeded: {"code", "key_id"}}

var mDeliveries = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_webhook_deliveries_total", Help: "Webhook deliveries by result (ok, failed, dropped = queue full).",
}, []string{"result"})

func init() { prometheus.MustRegister(mDeliveries) }

// Event is the JSON body of every delivery.
type Event struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Created int64          `json:"created"`
	OrgID   string         `json:"org_id"`
	Data    map[string]any `json:"data"`
}

func NewEvent(orgID, typ string, data map[string]any) Event {
	if data == nil {
		data = map[string]any{}
	}
	return Event{ID: "evt_" + store.NewID(), Type: typ, Created: time.Now().Unix(), OrgID: orgID, Data: data}
}

// NewSecret returns a random signing secret.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// Sign is the X-GW-Signature value: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>.
func Sign(secret string, t int64, body []byte) string {
	ts := strconv.FormatInt(t, 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// CheckURL validates a webhook URL: https on a public host, or any http(s) URL when the
// operator allows private URLs (ALLOW_PRIVATE_PROVIDER_URLS, dev setups).
func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxURLLen || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("must be an http(s) URL without credentials")
	}
	if allowPrivate {
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("must use https")
	}
	return gateway.CheckPublicURL(raw)
}

// Subscribed reports whether events (a webhook's list) includes typ.
func Subscribed(events []string, typ string) bool {
	return slices.Contains(events, "*") || slices.Contains(events, typ)
}

// Dispatcher queues events and delivers them to the org's enabled webhooks.
type Dispatcher struct {
	S            *store.Store
	AllowPrivate bool
	Backoff      time.Duration // delay before the first retry, doubled per retry

	client  *http.Client
	q       chan Event
	mu      sync.Mutex
	seen    map[string]time.Time
	pending map[string]int // per org: events queued or being delivered
}

// New makes a dispatcher with a queue of size queue; call Start to deliver.
func New(s *store.Store, allowPrivate bool, queue int) *Dispatcher {
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
	return &Dispatcher{S: s, AllowPrivate: allowPrivate, Backoff: defaultBackoff,
		client: &http.Client{Transport: tr, Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		q: make(chan Event, queue), seen: map[string]time.Time{}, pending: map[string]int{}}
}

// Start runs workers until ctx ends.
// shortcut: a worker waits out retries of one endpoint; move retries to a delay queue if slow
// endpoints start dropping events.
func (d *Dispatcher) Start(ctx context.Context, workers int) {
	for range workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-d.q:
					d.dispatch(ctx, ev)
					d.release(ev.OrgID)
				}
			}
		}()
	}
}

// Emit queues an event without blocking; it is dropped (and counted) when the queue is full or
// the org already holds its share of it.
// request.failed goes out at most once per minute per org and model, budget.exceeded once per
// org, code and key.
func (d *Dispatcher) Emit(orgID, typ string, data map[string]any) {
	if fs, ok := dedupeOn[typ]; ok {
		key := orgID + "\x00" + typ
		for _, f := range fs {
			key += "\x00" + fmt.Sprint(data[f])
		}
		if !d.first(key) {
			return
		}
	}
	if !d.reserve(orgID) {
		mDeliveries.WithLabelValues("dropped").Inc()
		return
	}
	select {
	case d.q <- NewEvent(orgID, typ, data):
	default:
		d.release(orgID)
		mDeliveries.WithLabelValues("dropped").Inc()
	}
}

// reserve claims one of the org's queue slots, so an org with many or slow endpoints cannot
// fill the shared queue and make other orgs' events drop.
func (d *Dispatcher) reserve(orgID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending[orgID] >= max(cap(d.q)/queueShare, min(cap(d.q), 16)) { // small queues: no share limit
		return false
	}
	d.pending[orgID]++
	return true
}

func (d *Dispatcher) release(orgID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending[orgID]--; d.pending[orgID] <= 0 {
		delete(d.pending, orgID)
	}
}

func (d *Dispatcher) first(key string) bool {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.seen[key]; ok && now.Sub(t) < dedupeWindow {
		return false
	}
	if len(d.seen) >= maxDedupeKeys {
		for k, t := range d.seen {
			if now.Sub(t) >= dedupeWindow {
				delete(d.seen, k)
			}
		}
		if len(d.seen) >= maxDedupeKeys {
			clear(d.seen)
		}
	}
	d.seen[key] = now
	return true
}

func (d *Dispatcher) dispatch(ctx context.Context, ev Event) {
	hooks, err := d.S.ListWebhooks(ctx, ev.OrgID)
	if err != nil {
		slog.Error("webhook list failed", "org", ev.OrgID, "err", err)
		return
	}
	for _, h := range hooks {
		if h.Enabled && Subscribed(h.Events, ev.Type) {
			d.deliver(ctx, h, ev)
		}
	}
}

// deliver sends ev with retries and updates the webhook's consecutive failure count.
func (d *Dispatcher) deliver(ctx context.Context, h store.Webhook, ev Event) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ok := false
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.Backoff << (attempt - 1)):
			}
		}
		status, err := d.post(ctx, h, ev, body)
		if err == nil && status >= 200 && status < 300 {
			ok = true
			break
		}
		if err == nil && status < 500 {
			break // the receiver rejected it; retrying will not help
		}
	}
	if ok {
		mDeliveries.WithLabelValues("ok").Inc()
	} else {
		mDeliveries.WithLabelValues("failed").Inc()
	}
	if ok && h.Failures == 0 {
		return
	}
	_, err = d.S.UpdateWebhook(ctx, h.OrgID, h.ID, func(w *store.Webhook) error {
		if ok {
			w.Failures = 0
			return nil
		}
		w.Failures++
		if w.Failures >= disableAfter && w.Enabled {
			w.Enabled = false
			slog.Warn("webhook disabled after consecutive failures", "org", h.OrgID, "webhook", h.ID, "failures", w.Failures)
		}
		return nil
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Error("webhook state update failed", "webhook", h.ID, "err", err)
	}
}

// Send makes one signed delivery attempt of ev to h and returns the HTTP status. It does not
// change the failure count (used for test pings).
func (d *Dispatcher) Send(ctx context.Context, h store.Webhook, ev Event) (int, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	return d.post(ctx, h, ev, body)
}

// post re-validates the URL (DNS may have changed since save) and makes one request. The
// response body is discarded: receivers' answers are never shown to tenants.
func (d *Dispatcher) post(ctx context.Context, h store.Webhook, ev Event, body []byte) (int, error) {
	if err := CheckURL(h.URL, d.AllowPrivate); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Polyrail-Webhook/1")
	req.Header.Set("X-GW-Event", ev.Type)
	req.Header.Set("X-GW-Delivery", ev.ID) // same across retries, so receivers can dedupe
	req.Header.Set("X-GW-Signature", Sign(h.Secret, time.Now().Unix(), body))
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, responseDrain))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
