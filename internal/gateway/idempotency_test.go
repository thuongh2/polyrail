package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
)

func (f *fixture) idemServer(t *testing.T, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", f.g.Idempotent(f.g.ChatCompletions))
	mux.HandleFunc("POST /v1/embeddings", f.g.Idempotent(f.g.Embeddings))
	if extra != nil {
		mux.HandleFunc("POST /slow", f.g.Idempotent(extra))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestIdempotencyReplayNoDoubleBilling(t *testing.T) {
	f := setup(t)
	f.srv = f.idemServer(t, nil)
	body := `{"model":"test/ok","messages":[{"role":"user","content":"hi"}]}`
	r1 := f.post(t, "/v1/chat/completions", f.key, body, "Idempotency-Key", "abc")
	m1 := readJSON(t, r1)
	if r1.StatusCode != 200 || r1.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("first: %d %v", r1.StatusCode, m1)
	}
	r2 := f.post(t, "/v1/chat/completions", f.key, body, "Idempotency-Key", "abc")
	m2 := readJSON(t, r2)
	if r2.StatusCode != 200 || r2.Header.Get("Idempotent-Replayed") != "true" || m2["id"] != m1["id"] {
		t.Fatalf("replay: %d %v %v", r2.StatusCode, r2.Header, m2)
	}
	if f.okHits.Load() != 1 {
		t.Fatalf("upstream hit %d times", f.okHits.Load())
	}
	f.g.Usage.Close() // flush
	a, _ := f.g.S.KV.GetAgg(context.Background(), usage.OrgSpendKey(f.org.ID))
	if a[store.AggRequests] != 1 {
		t.Fatalf("billed requests: %v", a)
	}
}

func TestIdempotencyReuseAndStream(t *testing.T) {
	f := setup(t)
	f.srv = f.idemServer(t, nil)
	f.post(t, "/v1/embeddings", f.key, `{"model":"test/emb","input":"a"}`, "Idempotency-Key", "k1").Body.Close()
	r := f.post(t, "/v1/embeddings", f.key, `{"model":"test/emb","input":"b"}`, "Idempotency-Key", "k1")
	if m := readJSON(t, r); r.StatusCode != 422 || errCode(m) != "idempotency_key_reuse" {
		t.Fatalf("reuse: %d %v", r.StatusCode, m)
	}
	// same key on another path is a different scope
	r = f.post(t, "/v1/chat/completions", f.key, `{"model":"test/ok","messages":[{"role":"user","content":"x"}]}`, "Idempotency-Key", "k1")
	if r.Body.Close(); r.StatusCode != 200 {
		t.Fatalf("other path: %d", r.StatusCode)
	}
	// streams ignore the header
	s := `{"model":"test/ok","stream":true,"messages":[{"role":"user","content":"x"}]}`
	for range 2 {
		r = f.post(t, "/v1/chat/completions", f.key, s, "Idempotency-Key", "s1")
		if r.Body.Close(); r.StatusCode != 200 || r.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("stream: %d", r.StatusCode)
		}
	}
	r = f.post(t, "/v1/embeddings", f.key, `{}`, "Idempotency-Key", strings.Repeat("x", 256))
	if r.Body.Close(); r.StatusCode != 400 {
		t.Fatalf("long key: %d", r.StatusCode)
	}
	// errors are not stored: a retry after a 4xx runs again
	bad := `{"model":"nope","input":"a"}`
	for range 2 {
		r = f.post(t, "/v1/embeddings", f.key, bad, "Idempotency-Key", "e1")
		if r.Body.Close(); r.StatusCode != 404 || r.Header.Get("Idempotent-Replayed") != "" {
			t.Fatalf("error replayed: %d", r.StatusCode)
		}
	}
}

func TestIdempotencyInFlight(t *testing.T) {
	f := setup(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.srv = f.idemServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Write([]byte(`{"ok":true}`))
	})
	done := make(chan int)
	go func() {
		r := f.post(t, "/slow", f.key, `{}`, "Idempotency-Key", "slow")
		r.Body.Close()
		done <- r.StatusCode
	}()
	<-entered
	r := f.post(t, "/slow", f.key, `{}`, "Idempotency-Key", "slow")
	if m := readJSON(t, r); r.StatusCode != 409 || errCode(m) != "idempotency_key_in_progress" {
		t.Fatalf("in flight: %d %v", r.StatusCode, m)
	}
	close(release)
	if c := <-done; c != 200 {
		t.Fatalf("first: %d", c)
	}
}

func TestCostHeader(t *testing.T) {
	f := setup(t)
	f.srv = f.idemServer(t, nil)
	r := f.post(t, "/v1/chat/completions", f.key, chatBody)
	r.Body.Close()
	// 1M in at $1/M + 0.5M out at $2/M = 2_000_000 micros
	if got := r.Header.Get("X-GW-Cost-Micros"); got != "2000000" {
		t.Fatalf("cost header %q", got)
	}
	n, _ := f.g.Usage.Spent(context.Background(), usage.OrgSpendKey(f.org.ID))
	if strconv.FormatInt(n, 10) != r.Header.Get("X-GW-Cost-Micros") {
		t.Fatalf("billed %d vs header %s", n, r.Header.Get("X-GW-Cost-Micros"))
	}
	r = f.post(t, "/v1/embeddings", f.key, `{"model":"test/emb","input":"a"}`)
	r.Body.Close()
	if r.Header.Get("X-GW-Cost-Micros") != "0" {
		t.Fatalf("embeddings cost header %q", r.Header.Get("X-GW-Cost-Micros"))
	}
}
