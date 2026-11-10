package limits

import (
	"context"
	"os"
	"testing"
)

func testLimiter(t *testing.T, l Limiter) {
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		n, err := l.Incr(ctx, "k1", 1)
		if err != nil || n != int64(i) {
			t.Fatalf("incr %d: %d %v", i, n, err)
		}
	}
	if n, _ := l.Incr(ctx, "k1", 100); n != 103 {
		t.Fatalf("incr by n: %d", n)
	}
	if n, _ := l.Get(ctx, "k1"); n != 103 {
		t.Fatalf("get: %d", n)
	}
	if n, _ := l.Get(ctx, "other"); n != 0 {
		t.Fatalf("other key: %d", n)
	}
}

func TestLocal(t *testing.T) { testLimiter(t, NewLocal()) }

func TestRedis(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	r, err := NewRedis(url)
	if err != nil {
		t.Fatal(err)
	}
	r.c.FlushDB(context.Background())
	testLimiter(t, r)
}
