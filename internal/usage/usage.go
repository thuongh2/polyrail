// Package usage turns request results into rollup counters, spend counters and request logs,
// written asynchronously in batches so the request path never waits on storage.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/polyrail/polyrail/internal/store"
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
	flushMu  sync.RWMutex         // held by a flush while it writes; Spent's store read waits for it

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

func OrgSpendKey(org string) string { return "spend/org/" + org }

// EndUserPrefix is where an org's per-end-user rollups of one day live (day first, so log
// retention can prune them by range).
func EndUserPrefix(org, day string) string       { return "enduser/" + day + "/" + org + "/" }
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
	d := store.Day(l.At)
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
	if l.EndUser != "" {
		k := EndUserPrefix(l.OrgID, d) + l.EndUser
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

// AddSpend adds cost (micro-USD) to extra spend counters, read back with Spent (policy budgets,
// D26). Never blocks.
func (p *Pipeline) AddSpend(cost int64, keys ...string) {
	p.mu.Lock()
	for _, k := range keys {
		cur := p.pending[k]
		cur[store.AggCostMicros] += cost
		p.pending[k] = cur
	}
	p.mu.Unlock()
}

// HealthHour is the UTC hour bucket of a health counter.
func HealthHour(t time.Time) string { return t.UTC().Format("2006010215") }

// HealthKey is the hourly counter of one (model, provider) pair.
func HealthKey(model, providerID string, t time.Time) string {
	return "health/" + HealthHour(t) + "/" + model + "|" + providerID
}

// RecordHealth accounts one attempt of a provider for a model (public provider health). Never blocks.
func (p *Pipeline) RecordHealth(model, providerID string, ok bool, latencyMs int64, outTokens int, at time.Time) {
	var a store.Agg
	a[store.AggRequests] = 1
	if !ok {
		a[store.AggErrors] = 1
	}
	a[store.AggLatencyMs] = latencyMs
	a[store.AggOutTokens] = int64(outTokens)
	k := HealthKey(model, providerID, at)
	p.mu.Lock()
	cur := p.pending[k]
	cur.Add(a)
	p.pending[k] = cur
	p.mu.Unlock()
}

// KeyStatPrefix is the prefix of one hour's per-key counters of a provider.
func KeyStatPrefix(providerID string, t time.Time) string {
	return "keystat/" + HealthHour(t) + "/" + providerID + "|"
}

// RecordKey accounts one attempt on a provider key (docs/KEYS.md stats). Never blocks.
func (p *Pipeline) RecordKey(providerID, keyID string, ok bool, latencyMs int64, inTokens, outTokens int, at time.Time) {
	var a store.Agg
	a[store.AggRequests] = 1
	if !ok {
		a[store.AggErrors] = 1
	}
	a[store.AggLatencyMs] = latencyMs
	a[store.AggInTokens] = int64(inTokens)
	a[store.AggOutTokens] = int64(outTokens)
	k := KeyStatPrefix(providerID, at) + keyID
	p.mu.Lock()
	cur := p.pending[k]
	cur.Add(a)
	p.pending[k] = cur
	p.mu.Unlock()
}

// Spent returns flushed + pending cost for a spend counter. Flushed values are cached for one
// flush interval, which bounds overspend to about one interval of traffic per node (D7).
func (p *Pipeline) Spent(ctx context.Context, key string) (int64, error) {
	for {
		p.mu.Lock()
		gen := p.gen
		p.mu.Unlock()
		if c, ok := p.spendCache.Load(key); ok && c.(cachedAgg).gen == gen && time.Now().Before(c.(cachedAgg).exp) {
			p.mu.Lock()
			if p.gen != gen { // a flush committed since: the cached value misses it
				p.mu.Unlock()
				continue
			}
			pend, infl := p.pending[key], p.inflight[key]
			p.mu.Unlock()
			return c.(cachedAgg).a[store.AggCostMicros] + pend[store.AggCostMicros] + infl[store.AggCostMicros], nil
		}
		// Not during a flush: a read between its commit and gen++ would count inflight twice.
		p.flushMu.RLock()
		p.mu.Lock()
		gen = p.gen
		p.mu.Unlock()
		a, err := p.kv.GetAgg(ctx, key)
		if err != nil {
			p.flushMu.RUnlock()
			return 0, err
		}
		p.spendCache.Store(key, cachedAgg{a, gen, time.Now().Add(p.interval)})
		p.mu.Lock()
		pend := p.pending[key]
		p.mu.Unlock()
		p.flushMu.RUnlock()
		return a[store.AggCostMicros] + pend[store.AggCostMicros], nil
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
	p.flushMu.Lock()
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
		p.flushMu.Unlock()
		if err != nil {
			slog.Error("usage: counter flush failed, will retry", "err", err, "keys", len(adds))
		}
	} else {
		p.flushMu.Unlock()
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
