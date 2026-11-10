package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

// testKV exercises the KV contract; every backend must pass it.
func testKV(t *testing.T, kv KV) {
	ctx := context.Background()
	if _, err := kv.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: want ErrNotFound, got %v", err)
	}
	err := kv.Update(ctx, func(tx Tx) error {
		for i := 0; i < 5; i++ {
			if err := tx.Put(fmt.Sprintf("a/%d", i), []byte{byte(i)}); err != nil {
				return err
			}
		}
		v, err := tx.Get("a/3") // read-your-writes
		if err != nil || v[0] != 3 {
			return fmt.Errorf("read own write: %v %v", v, err)
		}
		return tx.Put("b/x", []byte("x"))
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := kv.Scan(ctx, "a/", "", 0)
	if err != nil || len(items) != 5 || items[0].Key != "a/0" {
		t.Fatalf("scan: %v %v", items, err)
	}
	items, _ = kv.Scan(ctx, "a/", "a/1", 2)
	if len(items) != 2 || items[0].Key != "a/2" {
		t.Fatalf("scan after: %v", items)
	}

	// failed Update must not apply anything
	boom := errors.New("boom")
	if err := kv.Update(ctx, func(tx Tx) error { _ = tx.Put("a/9", nil); return boom }); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if _, err := kv.Get(ctx, "a/9"); !errors.Is(err, ErrNotFound) {
		t.Fatal("rolled back write is visible")
	}

	// concurrent AddAgg must not lose increments
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := kv.AddAgg(ctx, map[string]Agg{"agg/x": {1, 0, 10}, "agg/y": {2}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if a, _ := kv.GetAgg(ctx, "agg/x"); a[AggRequests] != 20 || a[AggInTokens] != 200 {
		t.Fatalf("agg x: %v", a)
	}
	m, err := kv.ScanAgg(ctx, "agg/")
	if err != nil || len(m) != 2 || m["agg/y"][0] != 40 {
		t.Fatalf("scan agg: %v %v", m, err)
	}

	if err := kv.DeleteRange(ctx, "a/", "a0"); err != nil {
		t.Fatal(err)
	}
	if items, _ := kv.Scan(ctx, "a/", "", 0); len(items) != 0 {
		t.Fatalf("delete range left %v", items)
	}
	if _, err := kv.Get(ctx, "b/x"); err != nil {
		t.Fatal("delete range removed key outside range")
	}
}

func TestPebbleKV(t *testing.T) {
	kv, err := OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	testKV(t, kv)
}

func TestPostgresKV(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	kv, err := OpenPostgres(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	_ = kv.DeleteRange(context.Background(), "", "\x7f")
	testKV(t, kv)
}
