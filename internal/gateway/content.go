package gateway

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/polyrail/polyrail/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// Opt-in content logging (store.ContentLogging): the request body and the response (the JSON
// body, or a stream's assembled text) of /v1/chat/completions are stored sealed next to the
// request log. Writes run off the request path and are dropped when too many are pending.

// contentCapture collects one request's bodies; nil when the org has content logging off.
type contentCapture struct {
	req   string
	res   strings.Builder
	trunc bool
}

var (
	contentSlots    = make(chan struct{}, 64) // concurrent content writes
	mContentDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_content_log_dropped_total", Help: "Content-log writes skipped because 64 were already pending.",
	})
)

func init() { prometheus.MustRegister(mContentDropped) }

// withContent returns p with a capture for body when its org logs content; p itself is shared
// through the auth cache and never changed.
func withContent(p *principal, body []byte) *principal {
	if c := p.Org.ContentLogging; c == nil || !c.Enabled {
		return p
	}
	cp := *p
	cp.content = &contentCapture{}
	cp.content.req, cp.content.trunc = store.Truncate(string(body))
	return &cp
}

func (c *contentCapture) setResponse(b []byte) {
	if c != nil {
		c.res.Reset()
		c.addText(string(b))
	}
}

// addText appends streamed text, keeping at most one byte past the cap (store.Truncate cuts it).
func (c *contentCapture) addText(s string) {
	if c == nil {
		return
	}
	if room := store.ContentMaxBytes + 1 - c.res.Len(); room > 0 {
		c.res.WriteString(s[:min(len(s), room)])
	}
}

// saveContent stores the captured bodies of log l without blocking the caller.
func (g *Gateway) saveContent(p *principal, l store.RequestLog) {
	c := p.content
	if c == nil {
		return
	}
	lc := store.LogContent{LogID: l.ID, At: l.At, Request: c.req, ReqTruncated: c.trunc, Response: c.res.String()}
	select {
	case contentSlots <- struct{}{}:
	default:
		mContentDropped.Inc()
		return
	}
	go func() {
		defer func() { <-contentSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := g.S.PutContent(ctx, l.OrgID, lc); err != nil {
			slog.Error("content log write failed", "err", err, "org", l.OrgID)
		}
	}()
}
