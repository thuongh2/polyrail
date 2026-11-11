package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
)

// anthropicUpstream answers POST /v1/messages like the Anthropic API (JSON or SSE).
func anthropicUpstream(t *testing.T, gotModel *string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		*gotModel = fmt.Sprint(req["model"])
		if req["stream"] != true {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-up","content":[{"type":"text","text":"hello from claude"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-up","content":[],"usage":{"input_tokens":20,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		} {
			var e struct{ Type string }
			_ = json.Unmarshal([]byte(ev), &e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnthropicMessages(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var gotModel string
	up := anthropicUpstream(t, &gotModel)
	p := &store.Provider{Name: "claude", Type: "anthropic", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	m := &store.Model{ID: "test/claude", Name: "Claude Test", Kind: "chat", Enabled: true, InputPrice: 1_000_000, OutputPrice: 2_000_000,
		Targets: []store.Target{{ProviderID: p.ID, Model: "claude-up"}}}
	if err := f.g.S.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /anthropic/v1/messages", f.g.AnthropicMessages)
	mux.HandleFunc("GET /anthropic/v1/models", f.g.AnthropicModels)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path, key, body string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("x-api-key", key) // what the Anthropic SDKs send
		req.Header.Set("anthropic-version", "2023-06-01")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(raw)
	}

	// non-streaming: Anthropic message shape, catalog model id, upstream model id sent
	res, body := call("POST", "/anthropic/v1/messages", f.key, `{"model":"test/claude","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	var msg struct {
		Content []struct{ Type, Text string }
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		}
	}
	var top map[string]any
	_ = json.Unmarshal([]byte(body), &msg)
	_ = json.Unmarshal([]byte(body), &top)
	if res.StatusCode != 200 || top["type"] != "message" || top["role"] != "assistant" || top["model"] != "test/claude" || top["stop_reason"] != "end_turn" ||
		len(msg.Content) != 1 || msg.Content[0].Text != "hello from claude" || msg.Usage.InputTokens != 10 || msg.Usage.OutputTokens != 5 || top["extra_fields"] != nil {
		t.Fatalf("messages %d: %s", res.StatusCode, body)
	}
	if gotModel != "claude-up" || res.Header.Get("x-gateway-provider") != "claude" {
		t.Fatalf("upstream model %q provider %q", gotModel, res.Header.Get("x-gateway-provider"))
	}

	// streaming: Anthropic SSE events, no [DONE]
	res, body = call("POST", "/anthropic/v1/messages", f.key, `{"model":"test/claude","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream %d: %s", res.StatusCode, body)
	}
	for _, want := range []string{"event: message_start\ndata: ", `"model":"test/claude"`, "event: content_block_start\n", "event: content_block_delta\n", `"text":"hel"`, `"text":"lo"`, "event: message_delta\n", "event: message_stop\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") || strings.Index(body, "message_start") > strings.Index(body, "message_stop") {
		t.Fatalf("stream framing:\n%s", body)
	}

	// errors in Anthropic shape with the matching status
	for _, c := range []struct {
		key, body, typ string
		status         int
	}{
		{"sk-gw-wrong", `{"model":"test/claude","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "authentication_error", 401},
		{f.key, `{"model":"nope/x","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "not_found_error", 404},
		{f.key, `{"model":"test/emb","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "invalid_request_error", 400},
		{f.key, `{"model":"test/claude"}`, "invalid_request_error", 400},
	} {
		res, body := call("POST", "/anthropic/v1/messages", c.key, c.body)
		var e struct {
			Type  string
			Error struct{ Type, Message string }
		}
		_ = json.Unmarshal([]byte(body), &e)
		if res.StatusCode != c.status || e.Type != "error" || e.Error.Type != c.typ || e.Error.Message == "" {
			t.Fatalf("%s: %d %s", c.body, res.StatusCode, body)
		}
	}

	// model list in Anthropic format: chat models only
	res, body = call("GET", "/anthropic/v1/models", f.key, "")
	var raw struct {
		Data    []map[string]any
		HasMore *bool `json:"has_more"`
	}
	_ = json.Unmarshal([]byte(body), &raw)
	ids := map[string]map[string]any{}
	for _, d := range raw.Data {
		ids[fmt.Sprint(d["id"])] = d
	}
	if res.StatusCode != 200 || raw.HasMore == nil || *raw.HasMore || ids["test/claude"]["type"] != "model" || ids["test/claude"]["display_name"] != "Claude Test" || ids["test/claude"]["created_at"] == nil || ids["test/emb"] != nil {
		t.Fatalf("models %d: %s", res.StatusCode, body)
	}

	// spend from Bifrost usage: (10 in + 2×5 out) + (20 in + 2×7 out) micro-USD at $1/$2 per 1M
	f.g.Usage.Close()
	time.Sleep(10 * time.Millisecond)
	a, _ := f.g.S.KV.GetAgg(ctx, usage.OrgSpendKey(f.org.ID))
	if want := int64(20 + 34); a[store.AggCostMicros] != want {
		t.Fatalf("spend %d, want %d", a[store.AggCostMicros], want)
	}
}
