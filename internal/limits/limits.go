// Package limits implements per-minute request and token windows, in-process or on Redis/Valkey.
package limits

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter counts events in fixed one-minute windows.
type Limiter interface {
	// Incr adds n to the current window of key and returns the new total.
	Incr(ctx context.Context, key string, n int64) (int64, error)
	// Get returns the current window total of key.
	Get(ctx context.Context, key string) (int64, error)
}

func window(now time.Time) int64 { return now.Unix() / 60 }

// RetryAfter is the time left in the current window.
func RetryAfter(now time.Time) time.Duration {
	return time.Duration(60-now.Unix()%60) * time.Second
}

// Local is an in-process limiter (single node, or per-node limits in a cluster).
type Local struct {
	mu  sync.Mutex
	win int64
	m   map[string]int64
}

func NewLocal() *Local { return &Local{m: map[string]int64{}} }

func (l *Local) roll() {
	if w := window(time.Now()); w != l.win {
		l.win, l.m = w, map[string]int64{} // drop the previous window: memory stays bounded by active keys
	}
}

func (l *Local) Incr(_ context.Context, key string, n int64) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll()
	l.m[key] += n
	return l.m[key], nil
}

func (l *Local) Get(_ context.Context, key string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.roll()
	return l.m[key], nil
}

// Redis shares windows across gateway nodes.
type Redis struct{ c *redis.Client }

func NewRedis(url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	return &Redis{c: redis.NewClient(opt)}, nil
}

func rkey(key string) string { return "gw:l:" + key + ":" + strconv.FormatInt(window(time.Now()), 10) }

func (r *Redis) Incr(ctx context.Context, key string, n int64) (int64, error) {
	k := rkey(key)
	pipe := r.c.TxPipeline()
	incr := pipe.IncrBy(ctx, k, n)
	pipe.Expire(ctx, k, 2*time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

func (r *Redis) Get(ctx context.Context, key string) (int64, error) {
	v, err := r.c.Get(ctx, rkey(key)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}

func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }
