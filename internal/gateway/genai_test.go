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

	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

// genaiUpstream is an OpenAI-compatible provider answering Responses (JSON and SSE), input token
// counting and embeddings; the Gemini routes are served from it through Bifrost's converters.
func genaiUpstream(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch r.URL.Path {
		case "/v1/responses/input_tokens":
			fmt.Fprint(w, `{"object":"response.input_tokens","input_tokens":42}`)
		case "/v1/embeddings":
			fmt.Fprint(w, `{"object":"list","model":"emb","data":[{"object":"embedding","index":0,"embedding":[0.5,0.25]}],"usage":{"prompt_tokens":4,"total_tokens":4}}`)
		case "/v1/responses":
			resp := func(status, text string) string {
				out := "[]"
				if text != "" {
					out = `[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}]`
				}
				return `{"id":"r","object":"response","created_at":1,"model":"up","status":"` + status + `","output":` + out + `,"usage":{"input_tokens":1000000,"output_tokens":2,"total_tokens":1000002}}`
			}
			if req["stream"] != true {
				fmt.Fprint(w, resp("completed", "hi from gemini route"))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for i, ev := range []string{
				`{"type":"response.created","response":` + resp("in_progress", "") + `}`,
				`{"type":"response.output_item.added","output_index":0,"item":{"id":"m","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
				`{"type":"response.content_part.added","item_id":"m","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
				`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"hel"}`,
				`{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"lo"}`,
				`{"type":"response.output_text.done","item_id":"m","output_index":0,"content_index":0,"text":"hello"}`,
				`{"type":"response.completed","response":` + resp("completed", "hello") + `}`,
			} {
				var e struct{ Type string }
				_ = json.Unmarshal([]byte(ev), &e)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, strings.Replace(ev, "{", fmt.Sprintf(`{"sequence_number":%d,`, i), 1))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGenAI(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	up := genaiUpstream(t)
	p := &store.Provider{Name: "gm", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.Model{
		{ID: "vendor/gem", Name: "Gem", Kind: "chat", Enabled: true, ContextLength: 1000, InputPrice: 1_000_000, Targets: []store.Target{{ProviderID: p.ID, Model: "up"}}},
		{ID: "vendor/emb", Name: "Emb", Kind: "embedding", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "emb"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /genai/v1beta/models/{rest...}", f.g.GenAI)
	mux.HandleFunc("GET /genai/v1beta/models", f.g.GenAIModels)
	mux.HandleFunc("GET /genai/v1beta/models/{rest...}", f.g.GenAIModel)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	call := func(method, path string, hdr map[string]string, body string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(raw)
	}
	goog := map[string]string{"x-goog-api-key": f.key}
	const content = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

	// non-streaming, raw slash in the path (what the SDKs send for "vendor/gem")
	res, body := call("POST", "/genai/v1beta/models/vendor/gem:generateContent", goog, content)
	var gr struct {
		Candidates []struct {
			Content struct{ Parts []struct{ Text string } }
		}
		ModelVersion  string
		UsageMetadata struct{ PromptTokenCount int }
	}
	_ = json.Unmarshal([]byte(body), &gr)
	if res.StatusCode != 200 || len(gr.Candidates) != 1 || gr.Candidates[0].Content.Parts[0].Text != "hi from gemini route" ||
		gr.ModelVersion != "vendor/gem" || gr.UsageMetadata.PromptTokenCount != 1000000 || strings.Contains(body, "extra_fields") {
		t.Fatalf("generateContent %d: %s", res.StatusCode, body)
	}
	if res.Header.Get("x-gateway-provider") != "gm" {
		t.Fatalf("provider header %q", res.Header.Get("x-gateway-provider"))
	}

	// %2F-encoded id, "models/" prefix, ?key= auth
	res, body = call("POST", "/genai/v1beta/models/models%2Fvendor%2Fgem:generateContent?key="+f.key, nil, content)
	if res.StatusCode != 200 || !strings.Contains(body, "hi from gemini route") {
		t.Fatalf("encoded id %d: %s", res.StatusCode, body)
	}

	// streaming SSE: Gemini chunks with text, no [DONE]
	res, body = call("POST", "/genai/v1beta/models/vendor/gem:streamGenerateContent?alt=sse", goog, content)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(body, `"text":"hel"`) || !strings.Contains(body, `"text":"lo"`) || strings.Contains(body, "[DONE]") || strings.Contains(body, "event:") {
		t.Fatalf("stream %d: %s", res.StatusCode, body)
	}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if !strings.HasPrefix(line, "data: {") || !json.Valid([]byte(strings.TrimPrefix(line, "data: "))) {
			t.Fatalf("bad SSE frame %q", line)
		}
	}
	if res, _ := call("POST", "/genai/v1beta/models/vendor/gem:streamGenerateContent", goog, content); res.StatusCode != 400 {
		t.Fatalf("stream without alt=sse: %d", res.StatusCode)
	}

	// count tokens (not billed), embeddings
	res, body = call("POST", "/genai/v1beta/models/vendor/gem:countTokens", goog, content)
	if res.StatusCode != 200 || !strings.Contains(body, `"totalTokens":42`) {
		t.Fatalf("countTokens %d: %s", res.StatusCode, body)
	}
	res, body = call("POST", "/genai/v1beta/models/vendor/emb:embedContent", goog, `{"content":{"parts":[{"text":"x"}]}}`)
	if res.StatusCode != 200 || !strings.Contains(body, `"embedding":{"values":[0.5,0.25]`) {
		t.Fatalf("embedContent %d: %s", res.StatusCode, body)
	}
	res, body = call("POST", "/genai/v1beta/models/vendor/emb:batchEmbedContents", goog, `{"requests":[{"model":"models/vendor/emb","content":{"parts":[{"text":"x"}]}}]}`)
	if res.StatusCode != 200 || !strings.Contains(body, `"embeddings":[{"values":[0.5,0.25]`) {
		t.Fatalf("batchEmbedContents %d: %s", res.StatusCode, body)
	}

	// Google error shape: bad key, unknown model, wrong kind
	for _, c := range []struct {
		path string
		hdr  map[string]string
		code int
		st   string
	}{
		{"/genai/v1beta/models/vendor/gem:generateContent", map[string]string{"x-goog-api-key": "sk-gw-nope"}, 401, "UNAUTHENTICATED"},
		{"/genai/v1beta/models/vendor/none:generateContent", goog, 404, "NOT_FOUND"},
		{"/genai/v1beta/models/vendor/emb:generateContent", goog, 400, "INVALID_ARGUMENT"},
	} {
		res, body := call("POST", c.path, c.hdr, content)
		var e struct {
			Error struct {
				Code    int
				Message string
				Status  string
			}
		}
		_ = json.Unmarshal([]byte(body), &e)
		if res.StatusCode != c.code || e.Error.Code != c.code || e.Error.Status != c.st || e.Error.Message == "" {
			t.Fatalf("%s: %d %s", c.path, res.StatusCode, body)
		}
	}

	// list and get
	res, body = call("GET", "/genai/v1beta/models", goog, "")
	if res.StatusCode != 200 || !strings.Contains(body, `"name":"models/vendor/gem"`) || !strings.Contains(body, `"name":"models/vendor/emb"`) ||
		!strings.Contains(body, `"inputTokenLimit":1000`) || !strings.Contains(body, "embedContent") {
		t.Fatalf("list %d: %s", res.StatusCode, body)
	}
	if res, body := call("GET", "/genai/v1beta/models/models/vendor/gem", goog, ""); res.StatusCode != 200 || !strings.Contains(body, `"displayName":"Gem"`) {
		t.Fatalf("get %d: %s", res.StatusCode, body)
	}

	// spend: two generateContent calls and one stream, 1M input tokens each at $1/1M
	f.g.Usage.Close()
	time.Sleep(10 * time.Millisecond)
	a, _ := f.g.S.KV.GetAgg(ctx, usage.OrgSpendKey(f.org.ID))
	if want := int64(3_000_000); a[store.AggCostMicros] != want {
		t.Fatalf("spend %d, want %d", a[store.AggCostMicros], want)
	}
}
