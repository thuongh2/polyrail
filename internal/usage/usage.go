// Package usage turns request results into rollup counters, spend counters and request logs,
// written asynchronously in batches so the request path never waits on storage.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

// LogSink receives batches of request logs (the store, ClickHouse, ...).
type LogSink interface {
	WriteLogs(ctx context.Context, logs []store.RequestLog) error
}

type Pipeline struct {
	kv       store.KV
	sinks    []LogSink
	logs     chan store.RequestLog
	interval time.Duration

	mu       sync.Mutex
	pending  map[string]store.Agg // counters not yet flushed; billing is never dropped
	inflight map[string]store.Agg // counters being written by the current flush
	gen      int64                // bumped when a flush commits; guards Spent against double reads

	DroppedLogs atomic.Int64

	spendCache sync.Map // key -> cachedAgg
	stop       chan struct{}
	done       chan struct{}
	closeOnce  sync.Once
}

type cachedAgg struct {
	a   store.Agg
	gen int64
	exp time.Time
}

func New(kv store.KV, queue int, interval time.Duration, sinks ...LogSink) *Pipeline {
	p := &Pipeline{kv: kv, sinks: sinks, logs: make(chan store.RequestLog, queue), interval: interval,
		pending: map[string]store.Agg{}, stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

// Keys of the counters touched by one request.
func Month(t time.Time) string { return t.UTC().Format("200601") }
func Day(t time.Time) string   { return t.UTC().Format("20060102") }

func OrgSpendKey(org string) string              { return "spend/org/" + org }
func KeySpendKey(key string, t time.Time) string { return "spend/key/" + key + "/" + Month(t) }

// Record accounts one finished request. Never blocks.
func (p *Pipeline) Record(l store.RequestLog) {
	var a store.Agg
	a[store.AggRequests] = 1
	if l.Status >= 400 {
		a[store.AggErrors] = 1
	}
	a[store.AggInTokens] = int64(l.InTokens)
	a[store.AggOutTokens] = int64(l.OutTokens)
	a[store.AggCostMicros] = l.Cost
	a[store.AggLatencyMs] = l.LatencyMs
	if l.Cached {
		a[store.AggCacheHits] = 1
	}
	d := Day(l.At)
	p.mu.Lock()
	for _, k := range []string{
		"agg/" + l.OrgID + "/" + d + "/t",
		"agg/" + l.OrgID + "/" + d + "/m/" + l.Model,
		"agg/" + l.OrgID + "/" + d + "/k/" + l.KeyID,
		"agg/_/" + d + "/m/" + l.Model, // platform-wide
		OrgSpendKey(l.OrgID),
		KeySpendKey(l.KeyID, l.At),
	} {
		cur := p.pending[k]
		cur.Add(a)
		p.pending[k] = cur
	}
	p.mu.Unlock()
	select {
	case p.logs <- l:
	default:
		p.DroppedLogs.Add(1)
	}
}

// Spent returns flushed + pending cost for a spend counter. Flushed values are cached for one
// flush interval, which bounds overspend to about one interval of traffic per node (D7).
func (p *Pipeline) Spent(ctx context.Context, key string) (int64, error) {
	for {
		p.mu.Lock()
		gen := p.gen
		p.mu.Unlock()
		var stored store.Agg
		if c, ok := p.spendCache.Load(key); ok && c.(cachedAgg).gen == gen && time.Now().Before(c.(cachedAgg).exp) {
			stored = c.(cachedAgg).a
		} else {
			a, err := p.kv.GetAgg(ctx, key)
			if err != nil {
				return 0, err
			}
			stored = a
			p.spendCache.Store(key, cachedAgg{a, gen, time.Now().Add(p.interval)})
		}
		p.mu.Lock()
		if p.gen != gen { // a flush committed while we read: the stored value may miss or double count
			p.mu.Unlock()
			continue
		}
		pend, infl := p.pending[key], p.inflight[key]
		p.mu.Unlock()
		return stored[store.AggCostMicros] + pend[store.AggCostMicros] + infl[store.AggCostMicros], nil
	}
}

func (p *Pipeline) run() {
	defer close(p.done)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	batch := make([]store.RequestLog, 0, 512)
	for {
		select {
		case l := <-p.logs:
			batch = append(batch, l)
			if len(batch) < cap(batch) {
				continue
			}
		case <-t.C:
		case <-p.stop:
			for len(p.logs) > 0 {
				batch = append(batch, <-p.logs)
			}
			p.flush(batch)
			return
		}
		p.flush(batch)
		batch = batch[:0]
	}
}

func (p *Pipeline) flush(batch []store.RequestLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p.mu.Lock()
	adds := p.pending
	p.pending, p.inflight = map[string]store.Agg{}, adds
	p.mu.Unlock()
	if len(adds) > 0 {
		err := p.kv.AddAgg(ctx, adds)
		p.mu.Lock()
		if err != nil { // put the counters back so they are retried on the next flush
			for k, a := range adds {
				cur := p.pending[k]
				cur.Add(a)
				p.pending[k] = cur
			}
		} else {
			p.gen++ // cached stored values are stale now
		}
		p.inflight = nil
		p.mu.Unlock()
		if err != nil {
			slog.Error("usage: counter flush failed, will retry", "err", err, "keys", len(adds))
		}
	}
	if len(batch) == 0 {
		return
	}
	for _, s := range p.sinks {
		if err := s.WriteLogs(ctx, batch); err != nil {
			slog.Error("usage: log sink failed", "err", err, "logs", len(batch))
			p.DroppedLogs.Add(int64(len(batch)))
		}
	}
}

// Close flushes everything still pending. Safe to call more than once.
func (p *Pipeline) Close() {
	p.closeOnce.Do(func() { close(p.stop) })
	<-p.done
}

// StoreSink writes request logs into the KV store.
type StoreSink struct{ S *store.Store }

func (s StoreSink) WriteLogs(ctx context.Context, logs []store.RequestLog) error {
	return s.S.PutLogs(ctx, logs)
}
