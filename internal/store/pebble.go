package store

import (
	"context"
	"errors"
	"sync"

	"github.com/cockroachdb/pebble/v2"
)

// Pebble keys are namespaced: 'k' + key for values, 'c' + key for Agg counters.
const (
	nsKV  = "k"
	nsAgg = "c"
)

type pebbleKV struct {
	db *pebble.DB
	mu sync.Mutex // serializes read-modify-write (Update, AddAgg); writes are rare or batched
}

// OpenPebble opens (or creates) a Pebble database in dir. cacheMB bounds the block cache.
func OpenPebble(dir string, cacheMB int) (KV, error) {
	cache := pebble.NewCache(int64(cacheMB) << 20)
	defer cache.Unref()
	db, err := pebble.Open(dir, &pebble.Options{
		Cache:        cache,
		MemTableSize: 16 << 20,
		Logger:       quietLogger{},
	})
	if err != nil {
		return nil, err
	}
	return &pebbleKV{db: db}, nil
}

func (p *pebbleKV) get(key string) ([]byte, error) {
	v, closer, err := p.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), v...), nil
}

func (p *pebbleKV) Get(_ context.Context, key string) ([]byte, error) { return p.get(nsKV + key) }

func (p *pebbleKV) scan(prefix, after string, limit int) ([]Item, error) {
	opts := &pebble.IterOptions{LowerBound: []byte(prefix)}
	if end := prefixEnd(prefix); end != "" {
		opts.UpperBound = []byte(end)
	}
	it, err := p.db.NewIter(opts)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	ok := it.First()
	if after != "" {
		ok = it.SeekGE([]byte(after))
		if ok && string(it.Key()) == after {
			ok = it.Next()
		}
	}
	var out []Item
	for ; ok && (limit <= 0 || len(out) < limit); ok = it.Next() {
		out = append(out, Item{Key: string(it.Key()), Value: append([]byte(nil), it.Value()...)})
	}
	return out, it.Error()
}

func (p *pebbleKV) Scan(_ context.Context, prefix, after string, limit int) ([]Item, error) {
	if after != "" {
		after = nsKV + after
	}
	items, err := p.scan(nsKV+prefix, after, limit)
	for i := range items {
		items[i].Key = items[i].Key[len(nsKV):]
	}
	return items, err
}

type pebbleTx struct{ b *pebble.Batch }

func (t pebbleTx) Get(key string) ([]byte, error) {
	v, closer, err := t.b.Get([]byte(nsKV + key))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), v...), nil
}
func (t pebbleTx) Put(key string, v []byte) error { return t.b.Set([]byte(nsKV+key), v, nil) }
func (t pebbleTx) Delete(key string) error        { return t.b.Delete([]byte(nsKV+key), nil) }

func (p *pebbleKV) Update(_ context.Context, fn func(Tx) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.db.NewIndexedBatch()
	defer b.Close()
	if err := fn(pebbleTx{b}); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

func (p *pebbleKV) AddAgg(_ context.Context, adds map[string]Agg) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.db.NewBatch()
	defer b.Close()
	for k, a := range adds {
		cur, err := p.get(nsAgg + k)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		sum := decodeAgg(cur)
		sum.Add(a)
		if err := b.Set([]byte(nsAgg+k), sum.encode(), nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *pebbleKV) GetAgg(_ context.Context, key string) (Agg, error) {
	v, err := p.get(nsAgg + key)
	if errors.Is(err, ErrNotFound) {
		return Agg{}, nil
	}
	return decodeAgg(v), err
}

func (p *pebbleKV) ScanAgg(_ context.Context, prefix string) (map[string]Agg, error) {
	items, err := p.scan(nsAgg+prefix, "", 0)
	out := make(map[string]Agg, len(items))
	for _, it := range items {
		out[it.Key[len(nsAgg):]] = decodeAgg(it.Value)
	}
	return out, err
}

func (p *pebbleKV) DeleteRange(_ context.Context, start, end string) error {
	for _, ns := range []string{nsKV, nsAgg} {
		if err := p.db.DeleteRange([]byte(ns+start), []byte(ns+end), pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}

func (p *pebbleKV) Ping(context.Context) error { return nil }
func (p *pebbleKV) Close() error               { return p.db.Close() }

// quietLogger drops Pebble's info chatter but keeps errors visible.
type quietLogger struct{}

func (quietLogger) Infof(string, ...interface{}) {}
func (quietLogger) Errorf(format string, args ...interface{}) {
	pebble.DefaultLogger.Errorf(format, args...)
}
func (quietLogger) Fatalf(format string, args ...interface{}) {
	pebble.DefaultLogger.Fatalf(format, args...)
}
