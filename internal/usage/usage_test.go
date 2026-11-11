package usage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

type failingKV struct {
	store.KV
	fail atomic.Bool
}

func (f *failingKV) AddAgg(ctx context.Context, a map[string]store.Agg) error {
	if f.fail.Load() {
		return errors.New("down")
	}
	return f.KV.AddAgg(ctx, a)
}

func TestPipelineCountsSpendAndRetriesFailedFlush(t *testing.T) {
	ctx := context.Background()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	fkv := &failingKV{KV: kv}
	fkv.fail.Store(true)
	p := New(fkv, 10, 20*time.Millisecond)
	now := time.Now()
	p.Record(store.RequestLog{OrgID: "o", KeyID: "k", Model: "m", Status: 200, InTokens: 10, OutTokens: 5, Cost: 700, At: now})
	p.Record(store.RequestLog{OrgID: "o", KeyID: "k", Model: "m", Status: 502, At: now})

	if s, _ := p.Spent(ctx, OrgSpendKey("o")); s != 700 {
		t.Fatalf("pending spend: %d", s)
	}
	time.Sleep(60 * time.Millisecond) // flushes fail; counters must survive
	if s, _ := p.Spent(ctx, OrgSpendKey("o")); s != 700 {
		t.Fatalf("spend after failed flush: %d", s)
	}
	fkv.fail.Store(false)
	p.Close()

	a, _ := kv.GetAgg(ctx, "agg/o/"+store.Day(now)+"/m/m")
	if a[store.AggRequests] != 2 || a[store.AggErrors] != 1 || a[store.AggInTokens] != 10 || a[store.AggCostMicros] != 700 {
		t.Fatalf("rollup: %v", a)
	}
	if a, _ := kv.GetAgg(ctx, KeySpendKey("k", now)); a[store.AggCostMicros] != 700 {
		t.Fatalf("key spend: %v", a)
	}
}

func TestRecordHealthKeys(t *testing.T) {
	ctx := context.Background()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	p := New(kv, 10, time.Hour)
	at := time.Date(2026, 11, 11, 9, 59, 0, 0, time.FixedZone("x", 3600)) // 08:59 UTC
	p.RecordHealth("v/m", "prov1", true, 200, 50, at)
	p.RecordHealth("v/m", "prov1", false, 0, 0, at)
	p.Close()
	if HealthKey("v/m", "prov1", at) != "health/2026111108/v/m|prov1" {
		t.Fatalf("key: %s", HealthKey("v/m", "prov1", at))
	}
	a, _ := kv.GetAgg(ctx, "health/2026111108/v/m|prov1")
	if a[store.AggRequests] != 2 || a[store.AggErrors] != 1 || a[store.AggLatencyMs] != 200 || a[store.AggOutTokens] != 50 {
		t.Fatalf("health: %v", a)
	}
}
