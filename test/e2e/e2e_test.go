// Package e2e builds the real gateway binary and drives it over HTTP against a mock
// OpenAI-compatible upstream. No real provider keys are needed.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	adminEmail    = "ops@example.com"
	adminPassword = "e2e-password-123"
	upstreamKey   = "sk-upstream-e2e"
)

func repoRoot(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return root
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "polyrail")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/polyrail")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// mockUpstream is a minimal OpenAI-compatible provider that checks the decrypted provider key.
func mockUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+upstreamKey {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad upstream key","type":"auth"}}`)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			fmt.Fprint(w, `{"object":"list","model":"emb","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5,0.75]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`)
			return
		}
		if req["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, part := range []string{"str", "eam"} {
				fmt.Fprintf(w, "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
			}
			fmt.Fprint(w, "data: {\"id\":\"c2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"up\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		if req["model"] == "up-cache" { // D32: 800 of 1000 prompt tokens read from the provider's prompt cache
			fmt.Fprint(w, `{"id":"c3","object":"chat.completion","created":1,"model":"up-cache","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"prompt_tokens_details":{"cached_tokens":800},"completion_tokens_details":{"reasoning_tokens":4}}}`)
			return
		}
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) do(method, path, body string, hdr ...string) (int, http.Header, []byte) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-GW-CSRF", "1")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, raw
}

func (c *client) must(want int, method, path, body string, out any, hdr ...string) http.Header {
	c.t.Helper()
	code, h, raw := c.do(method, path, body, hdr...)
	if code != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: not json: %s", method, path, raw)
		}
	}
	return h
}

func eventually(t *testing.T, d time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// freshPostgresDB creates a throwaway database on the TEST_POSTGRES_URL server, so the e2e run
// does not race with store tests that share gw_test.
func freshPostgresDB(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	name := fmt.Sprintf("gw_e2e_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		conn.Close(context.Background())
	})
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil || cfg.Database == "" {
		t.Fatalf("TEST_POSTGRES_URL must name a database: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

func TestE2EPebble(t *testing.T) { runFlow(t, nil) }

func TestE2EPostgresRedis(t *testing.T) {
	pg := os.Getenv("TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	env := []string{"STORE=postgres", "POSTGRES_URL=" + freshPostgresDB(t, pg)}
	if r := os.Getenv("TEST_REDIS_URL"); r != "" {
		env = append(env, "REDIS_URL="+r)
	}
	runFlow(t, env)
}

func runFlow(t *testing.T, extraEnv []string) {
	if testing.Short() {
		t.Skip("e2e skipped in -short mode")
	}
	bin := buildBinary(t)
	var hits atomic.Int64
	up := mockUpstream(t, &hits)
	addr := freeAddr(t)

	var logs bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"MASTER_KEY=e2e-master-key-0123456789abcdef",
		"ADMIN_EMAIL=" + adminEmail,
		"ADMIN_PASSWORD=" + adminPassword,
		"DATA_DIR=" + t.TempDir(),
		"LISTEN_ADDR=" + addr,
	}, extraEnv...)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("gateway output:\n%s", logs.String())
		}
	})

	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: "http://" + addr, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	eventually(t, 20*time.Second, "/healthz", func() bool {
		select {
		case err := <-exited:
			stopped = true
			t.Fatalf("gateway exited early: %v\n%s", err, logs.String())
		default:
		}
		res, err := http.Get(c.base + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	c.must(200, "GET", "/readyz", "", nil)

	// F9-AC3: operator account from env; login sets the session cookie
	if code, _, _ := c.do("POST", "/api/auth/login", `{"email":"`+adminEmail+`","password":"wrong-password-0"}`); code != 401 {
		t.Fatalf("wrong password: %d", code)
	}
	var me struct {
		User struct {
			Operator bool `json:"operator"`
		} `json:"user"`
		Orgs []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"orgs"`
	}
	c.must(200, "POST", "/api/auth/login", `{"email":"`+adminEmail+`","password":"`+adminPassword+`"}`, &me)
	if !me.User.Operator || len(me.Orgs) != 1 || me.Orgs[0].Role != "owner" {
		t.Fatalf("bootstrap operator: %+v", me)
	}
	org := me.Orgs[0].ID

	// F2-AC1/AC3: public OpenAI-compatible provider; key never echoed
	var prov struct {
		ID string `json:"id"`
	}
	code, _, raw := c.do("POST", "/api/admin/providers", `{"name":"mock","type":"openai","base_url":"`+up.URL+`","enabled":true,"keys":[{"name":"k","value":"`+upstreamKey+`","weight":1}]}`)
	if code != 200 || bytes.Contains(raw, []byte(upstreamKey)) {
		t.Fatalf("create provider: %d %s", code, raw)
	}
	_ = json.Unmarshal(raw, &prov)
	c.must(200, "POST", "/api/admin/models", `{"id":"mock/chat","name":"Mock Chat","context_length":8192,"input_price_micros":1000000,"output_price_micros":2000000,"enabled":true,"targets":[{"provider_id":"`+prov.ID+`","model":"up"}]}`, nil)
	c.must(200, "POST", "/api/admin/models", `{"id":"mock/emb","name":"Mock Embed","kind":"embedding","input_price_micros":100000,"enabled":true,"targets":[{"provider_id":"`+prov.ID+`","model":"emb"}]}`, nil)

	// F6-AC1: key shown once
	var key struct {
		Key struct {
			ID string `json:"id"`
		} `json:"key"`
		Secret string `json:"secret"`
	}
	c.must(201, "POST", "/api/orgs/"+org+"/keys", `{"name":"e2e"}`, &key)
	if !strings.HasPrefix(key.Secret, "sk-gw-") {
		t.Fatalf("secret: %q", key.Secret)
	}
	if _, _, raw := c.do("GET", "/api/orgs/"+org+"/keys", ""); bytes.Contains(raw, []byte(key.Secret)) {
		t.Fatal("key list returns the secret")
	}
	bearer := []string{"Authorization", "Bearer " + key.Secret}

	// F1-AC4: catalog reload (2 s poll) makes the model visible
	eventually(t, 6*time.Second, "mock/chat in /v1/models", func() bool {
		_, _, raw := c.do("GET", "/v1/models", "", bearer...)
		return bytes.Contains(raw, []byte(`"mock/chat"`))
	})

	// F1-AC1 + F4-AC4: non-stream chat
	var chat struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Total int `json:"total_tokens"`
		} `json:"usage"`
	}
	h := c.must(200, "POST", "/v1/chat/completions", `{"model":"mock/chat","messages":[{"role":"user","content":"ping"}]}`, &chat, bearer...)
	if chat.Object != "chat.completion" || chat.Model != "mock/chat" || len(chat.Choices) != 1 || chat.Choices[0].Message.Content != "pong" || chat.Usage.Total != 15 {
		t.Fatalf("chat response: %+v", chat)
	}
	if h.Get("x-gateway-provider") != "mock" {
		t.Fatalf("x-gateway-provider = %q", h.Get("x-gateway-provider"))
	}

	// F1-AC2: streaming
	req, _ := http.NewRequest("POST", c.base+"/v1/chat/completions", strings.NewReader(`{"model":"mock/chat","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
	req.Header.Set("Authorization", "Bearer "+key.Secret)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("stream: %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), b)
	}
	var text strings.Builder
	var lines []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("non-data SSE line: %q", line)
		}
		if line == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("chunk %q: %v", line, err)
		}
		for _, ch := range chunk.Choices {
			text.WriteString(ch.Delta.Content)
		}
	}
	res.Body.Close()
	if text.String() != "stream" || len(lines) == 0 || lines[len(lines)-1] != "data: [DONE]" {
		t.Fatalf("stream text %q lines %q", text.String(), lines)
	}

	// F1-AC3: embeddings
	var emb struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	c.must(200, "POST", "/v1/embeddings", `{"model":"mock/emb","input":"hello"}`, &emb, bearer...)
	if len(emb.Data) != 1 || len(emb.Data[0].Embedding) != 3 {
		t.Fatalf("embeddings: %+v", emb)
	}

	// F1-AC5: OpenAI error shape
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	c.must(401, "POST", "/v1/chat/completions", `{"model":"mock/chat","messages":[{"role":"user","content":"x"}]}`, &e, "Authorization", "Bearer sk-gw-wrong")
	if e.Error.Message == "" {
		t.Fatalf("401 error shape: %+v", e)
	}
	c.must(404, "POST", "/v1/chat/completions", `{"model":"nope/missing","messages":[{"role":"user","content":"x"}]}`, &e, bearer...)
	c.must(400, "POST", "/v1/chat/completions", `{"model":`, &e, bearer...)

	// F3-AC3: public catalog without auth
	anon := &client{t: t, base: c.base, http: &http.Client{Timeout: 10 * time.Second}}
	var cat struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Input  string `json:"input"`
				Output string `json:"output"`
			} `json:"pricing"`
		} `json:"data"`
	}
	anon.must(200, "GET", "/api/public/models", "", &cat)
	found := false
	for _, m := range cat.Data {
		if m.ID == "mock/chat" {
			found = m.Pricing.Input == "1" && m.Pricing.Output == "2" && m.ContextLength == 8192
		}
	}
	if !found {
		t.Fatalf("public catalog: %+v", cat)
	}

	// D32: prompt-cache pricing. 200 uncached × $1/M + 800 cache reads × $0.10/M + 10 out × $2/M = 300 micros
	c.must(200, "POST", "/api/admin/models", `{"id":"mock/cached","name":"Mock Cached","input_price_micros":1000000,"output_price_micros":2000000,"cache_read_price_micros":100000,"enabled":true,"targets":[{"provider_id":"`+prov.ID+`","model":"up-cache"}]}`, nil)
	var cachedCat struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				InputCacheRead  string `json:"input_cache_read"`
				InputCacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
		} `json:"data"`
	}
	eventually(t, 6*time.Second, "mock/cached in the public catalog with its cache price", func() bool {
		anon.must(200, "GET", "/api/public/models", "", &cachedCat)
		for _, m := range cachedCat.Data {
			if m.ID == "mock/cached" {
				return m.Pricing.InputCacheRead == "0.1" && m.Pricing.InputCacheWrite == ""
			}
		}
		return false
	})
	var cachedChat struct {
		Usage struct {
			Details struct {
				Cached int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	h = c.must(200, "POST", "/v1/chat/completions", `{"model":"mock/cached","usage":{"include":true},"messages":[{"role":"user","content":"long prompt"}]}`, &cachedChat, bearer...)
	if h.Get("X-GW-Cost-Micros") != "300" || cachedChat.Usage.Details.Cached != 800 || cachedChat.Usage.Cost != 0.0003 {
		t.Fatalf("cached chat: cost header %q, usage %+v", h.Get("X-GW-Cost-Micros"), cachedChat.Usage)
	}

	// F6-AC4/AC5 + F3-AC2: usage rollups and request logs after the async flush
	// chat: 10 in × $1/M + 5 out × $2/M = 20 micros; stream: 5×1 + 2×2 = 9 micros; embed: 4 × 0.1 = 0.4 micros
	var usage struct {
		Total struct {
			Requests  int64   `json:"requests"`
			InTokens  int64   `json:"in_tokens"`
			OutTokens int64   `json:"out_tokens"`
			CostUSD   float64 `json:"cost_usd"`
			CacheRead int64   `json:"cache_read_tokens"`
		} `json:"total"`
		Models []struct {
			Key string `json:"key"`
		} `json:"models"`
	}
	eventually(t, 6*time.Second, "usage rollup", func() bool {
		c.must(200, "GET", "/api/orgs/"+org+"/usage?days=1", "", &usage)
		return usage.Total.Requests >= 4
	})
	if usage.Total.InTokens < 19 || usage.Total.OutTokens < 7 || usage.Total.CostUSD < 0.000329 || usage.Total.CacheRead != 800 || len(usage.Models) == 0 {
		t.Fatalf("usage: %+v", usage)
	}
	var logRows []struct {
		Model     string `json:"model"`
		Provider  string `json:"provider"`
		Status    int    `json:"status"`
		Cost      int64  `json:"cost_micros"`
		CacheRead int    `json:"cache_read_tokens"`
		Reasoning int    `json:"reasoning_tokens"`
	}
	// counters are flushed before log rows in the same batch, so wait for the rows too
	eventually(t, 6*time.Second, "request logs", func() bool {
		c.must(200, "GET", "/api/orgs/"+org+"/logs", "", &logRows)
		return len(logRows) >= 4
	})
	if logRows[0].Model == "" {
		t.Fatalf("request logs: %+v", logRows)
	}
	cachedLog := false
	for _, l := range logRows {
		cachedLog = cachedLog || l.Model == "mock/cached" && l.CacheRead == 800 && l.Reasoning == 4 && l.Cost == 300
	}
	if !cachedLog {
		t.Fatalf("request logs lack the cached request's tokens: %+v", logRows)
	}

	// F8-AC2
	_, _, metrics := anon.do("GET", "/metrics", "")
	for _, m := range []string{"gateway_requests_total", "gateway_request_seconds", "gateway_usage_logs_dropped_total"} {
		if !bytes.Contains(metrics, []byte(m)) {
			t.Fatalf("/metrics missing %s", m)
		}
	}

	// F6-AC1: revocation is immediate on the same node
	c.must(200, "PATCH", "/api/orgs/"+org+"/keys/"+key.Key.ID, `{"revoked":true}`, nil)
	eventually(t, 6*time.Second, "revoked key rejected", func() bool {
		code, _, _ := c.do("POST", "/v1/chat/completions", `{"model":"mock/chat","messages":[{"role":"user","content":"x"}]}`, bearer...)
		return code == 401
	})

	if hits.Load() < 3 {
		t.Fatalf("upstream hits: %d", hits.Load())
	}

	// graceful shutdown
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			t.Fatalf("exit after SIGTERM: %v\n%s", err, logs.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("gateway did not exit within 20s of SIGTERM")
	}
	if !strings.Contains(logs.String(), `"shutting down"`) {
		t.Fatalf("no shutdown log:\n%s", logs.String())
	}
}
