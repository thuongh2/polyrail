// Package store holds the control plane, request logs and usage counters on an ordered key-value
// space. Entity code is written once against KV; backends (Pebble, Postgres) implement KV only.
package store

import (
	"context"
	"encoding/binary"
	"errors"
)

var ErrNotFound = errors.New("not found")

// Item is one key/value pair returned by Scan.
type Item struct {
	Key   string
	Value []byte
}

// Tx is the view inside KV.Update. Reads see the transaction's own writes.
type Tx interface {
	Get(key string) ([]byte, error)
	Put(key string, value []byte) error
	Delete(key string) error
}

// KV is the storage seam (see docs/ARCHITECTURE.md). Implementations: Pebble, Postgres.
type KV interface {
	Get(ctx context.Context, key string) ([]byte, error)
	// Scan returns up to limit (0 = all) items whose key has prefix, in key order, starting after
	// `after` (exclusive, "" = from the start).
	Scan(ctx context.Context, prefix, after string, limit int) ([]Item, error)
	Update(ctx context.Context, fn func(Tx) error) error
	// AddAgg atomically adds each Agg to the counter stored at its key.
	AddAgg(ctx context.Context, adds map[string]Agg) error
	GetAgg(ctx context.Context, key string) (Agg, error)
	ScanAgg(ctx context.Context, prefix string) (map[string]Agg, error)
	// DeleteRange removes keys in [start, end) from both KV and counters.
	DeleteRange(ctx context.Context, start, end string) error
	Ping(ctx context.Context) error
	Close() error
}

// Agg is a fixed vector of usage counters.
type Agg [AggFields]int64

const (
	AggRequests = iota
	AggErrors
	AggInTokens
	AggOutTokens
	AggCostMicros
	AggLatencyMs
	AggCacheHits
	AggFields
)

func (a *Agg) Add(b Agg) {
	for i := range a {
		a[i] += b[i]
	}
}

func (a Agg) encode() []byte {
	b := make([]byte, 8*AggFields)
	for i, v := range a {
		binary.LittleEndian.PutUint64(b[i*8:], uint64(v))
	}
	return b
}

func decodeAgg(b []byte) Agg {
	var a Agg
	for i := range a {
		if len(b) >= (i+1)*8 {
			a[i] = int64(binary.LittleEndian.Uint64(b[i*8:]))
		}
	}
	return a
}

// prefixEnd returns the smallest key greater than every key with the given prefix.
func prefixEnd(prefix string) string {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	return "" // no upper bound
}
