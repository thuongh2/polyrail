// Package alert sends gateway events (key.quarantined, key.recovered, provider.unavailable,
// budget.exceeded, request.failed) to chat and push services through shoutrrr URLs
// (github.com/nicholas-fedor/shoutrrr), one URL per channel, from a bounded in-memory queue.
package alert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/discord"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/googlechat"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/matrix"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/mattermost"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/slack"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/teams"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/telegram"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/push/gotify"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/push/ntfy"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/push/pushover"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Event types a channel may subscribe to ("*" = all). test.ping is sent by the test route only.
const (
	KeyQuarantined      = gateway.EventKeyQuarantined
	KeyRecovered        = gateway.EventKeyRecovered
	ProviderUnavailable = gateway.EventProviderUnavailable
	BudgetExceeded      = "budget.exceeded"
	RequestFailed       = "request.failed"
	TestPing            = "test.ping"
)

var Types = []string{KeyQuarantined, KeyRecovered, ProviderUnavailable, BudgetExceeded, RequestFailed}

const (
	timeout       = 10 * time.Second
	disableAfter  = 10 // consecutive failed sends
	dedupeWindow  = 10 * time.Minute
	maxDedupeKeys = 10_000
	queueShare    = 10 // an org may hold 1/queueShare of the queue; 16 minimum
	MaxPerScope   = 20
	maxURLLen     = 2048
)

var mSends = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_alert_sends_total", Help: "Alert channel sends by result (ok, failed, dropped = queue full).",
}, []string{"result"})

func init() { prometheus.MustRegister(mSends) }

// services are the shoutrrr services Polyrail offers. The package's own router imports every
// service (XMPP and MQTT clients among them); this map keeps the binary to the ones used here.
// generic and smtp render payloads with text/template, whose reflection turns off the linker's
// dead-method elimination for the whole binary (measured: +16 MB stripped binary, +5 MB idle
// RSS); they are built only with -tags alertsfull (services_full.go).
var services = map[string]func() types.Service{
	"telegram":   func() types.Service { return &telegram.Service{} },
	"discord":    func() types.Service { return &discord.Service{} },
	"slack":      func() types.Service { return &slack.Service{} },
	"teams":      func() types.Service { return &teams.Service{} },
	"googlechat": func() types.Service { return &googlechat.Service{} },
	"pushover":   func() types.Service { return &pushover.Service{} },
	"ntfy":       func() types.Service { return &ntfy.Service{} },
	"gotify":     func() types.Service { return &gotify.Service{} },
	"matrix":     func() types.Service { return &matrix.Service{} },
	"mattermost": func() types.Service { return &mattermost.Service{} },
}

// Services lists the supported service names, sorted.
func Services() []string {
	out := make([]string, 0, len(services))
	for s := range services {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// vendorHosted are the services whose API host is the vendor's (tenants may use them freely);
// ntfy only on its default host. The rest take a host from the URL.
var vendorHosted = map[string]bool{"telegram": true, "discord": true, "slack": true, "teams": true,
	"googlechat": true, "pushover": true, "ntfy": true}

// onVendorHost reports whether a vendor-hosted service's URL keeps the vendor's API host: ntfy
// and googlechat take the host from the URL, telegram from its apiurl query parameter.
func onVendorHost(name string, u *url.URL) bool {
	switch name {
	case "ntfy":
		return u.Hostname() == "" || u.Hostname() == ntfy.DefaultHost
	case "googlechat":
		return u.Hostname() == "" || u.Hostname() == googlechat.DefaultHost
	case "telegram":
		for k := range u.Query() {
			if strings.EqualFold(k, "apiurl") {
				return false
			}
		}
	}
	return true
}

// TenantServices lists the services org channels may use: the vendor-hosted ones, or every
// supported service when allowPrivate (ALLOW_PRIVATE_PROVIDER_URLS) is set.
func TenantServices(allowPrivate bool) []string {
	if allowPrivate {
		return Services()
	}
	var out []string
	for _, s := range Services() {
		if vendorHosted[s] {
			out = append(out, s)
		}
	}
	return out
}

// Service is the service name of a channel URL ("generic+https://..." is "generic").
func Service(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(strings.ToLower(u.Scheme), "+")
	return name
}

// CheckURL validates a channel URL: a supported service whose shoutrrr config parses. tenant
// channels may use only vendor-hosted services, unless allowPrivate (ALLOW_PRIVATE_PROVIDER_URLS)
// is set, and then a custom host must be public. Errors never quote the URL.
func CheckURL(raw string, tenant, allowPrivate bool) error {
	if raw == "" || len(raw) > maxURLLen {
		return errors.New("url is required (at most 2048 characters)")
	}
	name := Service(raw)
	if services[name] == nil {
		return fmt.Errorf("unsupported service %q (supported: %s)", name, strings.Join(Services(), ", "))
	}
	_, u, err := initService(raw)
	if err != nil {
		return fmt.Errorf("not a valid %s URL", name)
	}
	if name == "teams" { // the workflow URL is only checked at send time by shoutrrr
		if err := teams.ValidateWebhookURL(u.Query().Get("host")); err != nil {
			return errors.New("teams needs host= a Power Automate workflow webhook URL")
		}
	}
	if !tenant {
		return nil
	}
	if vendorHosted[name] && onVendorHost(name, u) {
		return nil
	}
	if !allowPrivate {
		return fmt.Errorf("%s with a custom host is available to platform channels only", name)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%s URL needs a host", name)
	}
	if err := gateway.CheckPublicURL("https://" + u.Host); err != nil {
		return errors.New("host must be a public address")
	}
	return nil
}

// initService builds and configures the shoutrrr service for raw (custom "+scheme" URLs too).
func initService(raw string) (types.Service, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, errors.New("invalid URL")
	}
	name := Service(raw)
	mk := services[name]
	if mk == nil {
		return nil, nil, fmt.Errorf("unsupported service %q", name)
	}
	svc := mk()
	su := u
	if strings.Contains(u.Scheme, "+") {
		cs, ok := svc.(types.CustomURLService)
		if !ok {
			return nil, nil, fmt.Errorf("%s does not take custom URLs", name)
		}
		if su, err = cs.GetServiceURLFromCustom(u); err != nil || su == nil {
			return nil, nil, errors.New("invalid custom URL")
		}
	}
	if err := svc.Initialize(su, log.New(io.Discard, "", 0)); err != nil {
		return nil, nil, err
	}
	return svc, u, nil
}

// Sender delivers one message to a channel URL. public restricts connections to public addresses.
type Sender func(ctx context.Context, rawURL string, public bool, title, body string) error

// Send is the shoutrrr Sender. HTTP services get a client without redirects or proxy, and TCP
// services (SMTP) a dialer; both refuse private addresses when public is set.
func Send(ctx context.Context, rawURL string, public bool, title, body string) error {
	svc, _, err := initService(rawURL)
	if err != nil {
		return err
	}
	var dialer interface {
		DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	} = &net.Dialer{Timeout: timeout}
	if public {
		dialer = gateway.PublicDialer(timeout)
	}
	if s, ok := svc.(types.HTTPClientSetter); ok {
		s.SetHTTPClient(&http.Client{Timeout: timeout,
			Transport:     &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: timeout},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	}
	if s, ok := svc.(types.DialContextSetter); ok {
		s.SetDialContext(dialer.DialContext)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	params := types.Params{types.TitleKey: title}
	if cs, ok := svc.(types.ContextSender); ok {
		return cs.SendContext(ctx, body, &params)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Send(body, &params) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Message is the human-readable title and body of an event: names, ids and status codes only,
// never prompt or upstream text.
func Message(typ string, data map[string]any) (string, string) {
	s := func(k string) string {
		if v, ok := data[k]; ok && v != nil {
			return fmt.Sprint(v)
		}
		return ""
	}
	key := func() string {
		name := s("key_name")
		if name == "" {
			name = s("key_id")
		}
		return fmt.Sprintf("Key %s of provider %s", name, orDash(s("provider")))
	}
	switch typ {
	case KeyQuarantined:
		body := key() + " was quarantined"
		if r := s("reason"); r != "" {
			body += " after " + r
		}
		return "Polyrail: provider key quarantined", body + ". It is skipped until a successful test or a manual clear."
	case KeyRecovered:
		return "Polyrail: provider key recovered", key() + " is available again."
	case ProviderUnavailable:
		return "Polyrail: provider unavailable", fmt.Sprintf("Provider %s has no available key (all cooling down, quarantined or disabled). Requests fail over to other providers.", orDash(s("provider")))
	case BudgetExceeded:
		if s("code") == "key_budget_exceeded" {
			return "Polyrail: API key budget reached", fmt.Sprintf("API key %s reached its monthly budget; its requests are refused until next month or a budget change.", s("key_id"))
		}
		return "Polyrail: out of credits", "The organization is out of credits; requests are refused until credits are added."
	case RequestFailed:
		return "Polyrail: requests failing", fmt.Sprintf("Requests to model %s failed at every provider (status %s).", s("model"), s("status"))
	case TestPing:
		return "Polyrail: test notification", "This channel receives Polyrail alerts."
	}
	return "Polyrail: " + typ, ""
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// subject is what, with org and type, limits an event to one per dedupe window.
func subject(typ string, data map[string]any) string {
	var parts []string
	for _, k := range []string{"provider_id", "key_id", "model", "code"} {
		if v, ok := data[k]; ok {
			parts = append(parts, fmt.Sprint(v))
		}
	}
	return strings.Join(parts, "\x00")
}

// Subscribed reports whether events (a channel's list) includes typ.
func Subscribed(events []string, typ string) bool {
	return slices.Contains(events, "*") || slices.Contains(events, typ)
}

type event struct {
	org, typ string
	data     map[string]any
}

// Dispatcher queues events and sends them to the scope's enabled channels (org channels for org
// events, platform channels for org "").
type Dispatcher struct {
	S            *store.Store
	AllowPrivate bool
	Send         Sender // Send by default; tests inject a fake

	q       chan event
	mu      sync.Mutex
	seen    map[string]time.Time
	pending map[string]int
}

// New makes a dispatcher with a queue of size queue; call Start to deliver.
func New(s *store.Store, allowPrivate bool, queue int) *Dispatcher {
	return &Dispatcher{S: s, AllowPrivate: allowPrivate, Send: Send, q: make(chan event, queue),
		seen: map[string]time.Time{}, pending: map[string]int{}}
}

// Start runs workers until ctx ends.
func (d *Dispatcher) Start(ctx context.Context, workers int) {
	for range workers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-d.q:
					d.dispatch(ctx, ev)
					d.release(ev.org)
				}
			}
		}()
	}
}

// Emit queues an event without blocking. Unknown types are ignored; an event is sent at most once
// per 10 minutes per (org, type, subject), and dropped (counted) when the queue is full or the org
// holds its share of it.
func (d *Dispatcher) Emit(orgID, typ string, data map[string]any) {
	if !slices.Contains(Types, typ) || !d.first(orgID+"\x00"+typ+"\x00"+subject(typ, data)) {
		return
	}
	if !d.reserve(orgID) {
		mSends.WithLabelValues("dropped").Inc()
		return
	}
	select {
	case d.q <- event{orgID, typ, data}:
	default:
		d.release(orgID)
		mSends.WithLabelValues("dropped").Inc()
	}
}

func (d *Dispatcher) reserve(orgID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pending[orgID] >= max(cap(d.q)/queueShare, min(cap(d.q), 16)) {
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

func (d *Dispatcher) dispatch(ctx context.Context, ev event) {
	chs, err := d.S.ListAlertChannels(ctx, ev.org)
	if err != nil {
		slog.Error("alert channel list failed", "org", ev.org, "err", err)
		return
	}
	title, body := Message(ev.typ, ev.data)
	for _, c := range chs {
		if c.Enabled && Subscribed(c.Events, ev.typ) {
			d.deliver(ctx, c, title, body)
		}
	}
}

// public reports whether channel c may reach only public addresses: tenant channels always.
func public(c store.AlertChannel) bool { return c.OrgID != "" }

// deliver sends one message and updates the channel's consecutive failure count, disabling it
// after disableAfter failures.
func (d *Dispatcher) deliver(ctx context.Context, c store.AlertChannel, title, body string) {
	err := d.Send(ctx, c.URL, public(c), title, body)
	if err != nil {
		mSends.WithLabelValues("failed").Inc()
	} else {
		mSends.WithLabelValues("ok").Inc()
	}
	if err == nil && c.Failures == 0 {
		return
	}
	_, uerr := d.S.UpdateAlertChannel(ctx, c.OrgID, c.ID, func(ch *store.AlertChannel) error {
		if err == nil {
			ch.Failures = 0
			return nil
		}
		ch.Failures++
		if ch.Failures >= disableAfter && ch.Enabled {
			ch.Enabled = false
			slog.Warn("alert channel disabled after consecutive failures", "org", c.OrgID, "channel", c.ID, "failures", ch.Failures)
		}
		return nil
	})
	if uerr != nil && !errors.Is(uerr, store.ErrNotFound) {
		slog.Error("alert channel state update failed", "channel", c.ID, "err", uerr)
	}
}

// Test sends a test message to c now. It does not change the failure count. The error is
// returned as is; callers show tenants a generic message.
func (d *Dispatcher) Test(ctx context.Context, c store.AlertChannel) error {
	title, body := Message(TestPing, nil)
	return d.Send(ctx, c.URL, public(c), title, body)
}
