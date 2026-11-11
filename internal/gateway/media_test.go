package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// mediaUpstream answers images, rerank, input token counting (OpenAI) and OCR (Mistral) and
// remembers the last request body it saw.
func mediaUpstream(t *testing.T, last *atomic.Value) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/images/generations":
			b, _ := io.ReadAll(r.Body)
			last.Store(string(b))
			fmt.Fprintf(w, `{"created":1,"data":[{"b64_json":%q},{"b64_json":%q}],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}`, tinyPNG, tinyPNG)
		case "/v1/images/edits", "/v1/images/variations":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("model") != "img-up" {
				http.Error(w, `{"error":{"message":"bad form"}}`, 400)
				return
			}
			last.Store(r.FormValue("prompt"))
			fmt.Fprint(w, `{"created":1,"data":[{"url":"https://img.example/1.png"}]}`)
		case "/v1/rerank":
			b, _ := io.ReadAll(r.Body)
			last.Store(string(b))
			fmt.Fprint(w, `{"id":"rr","results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}],"usage":{"prompt_tokens":1000000,"total_tokens":1000000}}`)
		case "/v1/responses/input_tokens":
			fmt.Fprint(w, `{"object":"response.input_tokens","input_tokens":42}`)
		case "/v1/ocr":
			fmt.Fprint(w, `{"model":"ocr-up","pages":[{"index":0,"markdown":"# Invoice"}],"usage_info":{"pages_processed":1,"doc_size_bytes":10}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func multipartPost(t *testing.T, url, key string, fields map[string]string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("image", "in.png")
	_, _ = fw.Write([]byte("\x89PNG fake"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", url, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestImagesRerankOCRCountTokens(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var last atomic.Value
	up := mediaUpstream(t, &last)
	p := &store.Provider{Name: "media", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	pm := &store.Provider{Name: "mistral", Type: "mistral", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	for _, pr := range []*store.Provider{p, pm} {
		if err := f.g.S.PutProvider(ctx, pr); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []*store.Model{
		{ID: "test/img", Kind: "image", Enabled: true, ImagePrice: 40_000, InputPrice: 5_000_000, Targets: []store.Target{{ProviderID: p.ID, Model: "img-up"}}},
		{ID: "test/rerank", Kind: "rerank", Enabled: true, RequestPrice: 2_000, InputPrice: 50_000, Targets: []store.Target{{ProviderID: p.ID, Model: "rr-up"}}},
		{ID: "test/ocr", Kind: "ocr", Enabled: true, RequestPrice: 1_000, Targets: []store.Target{{ProviderID: pm.ID, Model: "ocr-up"}}},
		{ID: "test/text", Kind: "chat", Enabled: true, InputPrice: 2_000_000, RequestPrice: 7, Targets: []store.Target{{ProviderID: p.ID, Model: "up"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/images/generations", f.g.ImageGenerations)
	mux.HandleFunc("POST /v1/images/edits", f.g.ImageEdits)
	mux.HandleFunc("POST /v1/images/variations", f.g.ImageVariations)
	mux.HandleFunc("POST /v1/rerank", f.g.Rerank)
	mux.HandleFunc("POST /v1/ocr", f.g.OCR)
	mux.HandleFunc("POST /v1/responses/input_tokens", f.g.ResponsesInputTokens)
	mux.HandleFunc("POST /anthropic/v1/messages/count_tokens", f.g.AnthropicCountTokens)
	mux.HandleFunc("GET /v1/models", f.g.Models)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f.srv = srv

	// image generation: b64 images, no extra_fields; 2 images × $0.04 + 10 tokens × $5/1M = 80_050
	r := f.post(t, "/v1/images/generations", f.key, `{"model":"test/img","prompt":"a red fox","n":2}`)
	m := readJSON(t, r)
	data, _ := m["data"].([]any)
	if r.StatusCode != 200 || len(data) != 2 || data[0].(map[string]any)["b64_json"] != tinyPNG || m["extra_fields"] != nil {
		t.Fatalf("generation %d: %v", r.StatusCode, m)
	}
	if body, _ := last.Load().(string); !strings.Contains(body, `"model":"img-up"`) || !strings.Contains(body, "a red fox") {
		t.Fatalf("upstream got %s", body)
	}
	// edit and variation: multipart in, url out; 1 image each × $0.04
	res := multipartPost(t, srv.URL+"/v1/images/edits", f.key, map[string]string{"model": "test/img", "prompt": "add a hat"})
	if m := readJSON(t, res); res.StatusCode != 200 || last.Load() != "add a hat" || !strings.Contains(fmt.Sprint(m["data"]), "https://img.example/1.png") {
		t.Fatalf("edit %d: %v", res.StatusCode, m)
	}
	res = multipartPost(t, srv.URL+"/v1/images/variations", f.key, map[string]string{"model": "test/img"})
	if m := readJSON(t, res); res.StatusCode != 200 || len(m["data"].([]any)) != 1 {
		t.Fatalf("variation %d: %v", res.StatusCode, m)
	}

	// rerank: Cohere-style body, results by score; $0.002 per request + 1M tokens × $0.05/1M
	r = f.post(t, "/v1/rerank", f.key, `{"model":"test/rerank","query":"capital of France","documents":["Berlin","Paris"],"top_n":2}`)
	m = readJSON(t, r)
	results, _ := m["results"].([]any)
	if r.StatusCode != 200 || len(results) != 2 || results[0].(map[string]any)["index"] != float64(1) || m["model"] != "test/rerank" {
		t.Fatalf("rerank %d: %v", r.StatusCode, m)
	}
	if body, _ := last.Load().(string); !strings.Contains(body, "capital of France") || !strings.Contains(body, `"model":"rr-up"`) {
		t.Fatalf("rerank upstream got %s", body)
	}

	// OCR through Bifrost's Mistral provider: $0.001 per request
	r = f.post(t, "/v1/ocr", f.key, `{"model":"test/ocr","document":{"type":"document_url","document_url":"https://example.com/a.pdf"}}`)
	if m := readJSON(t, r); r.StatusCode != 200 || !strings.Contains(fmt.Sprint(m["pages"]), "# Invoice") {
		t.Fatalf("ocr %d: %v", r.StatusCode, m)
	}
	if r := f.post(t, "/v1/ocr", f.key, `{"model":"test/ocr","document":{"type":"document_url"}}`); r.StatusCode != 400 {
		t.Fatalf("ocr without url: %d", r.StatusCode)
	}

	// count tokens, OpenAI and Anthropic formats; not billed (chat model has a request price)
	r = f.post(t, "/v1/responses/input_tokens", f.key, `{"model":"test/text","input":"hello there"}`)
	if m := readJSON(t, r); r.StatusCode != 200 || m["input_tokens"] != float64(42) || m["object"] != "response.input_tokens" {
		t.Fatalf("input_tokens %d: %v", r.StatusCode, m)
	}
	r = f.post(t, "/anthropic/v1/messages/count_tokens", f.key, `{"model":"test/text","messages":[{"role":"user","content":"hello"}]}`, "anthropic-version", "2023-06-01")
	if m := readJSON(t, r); r.StatusCode != 200 || m["input_tokens"] != float64(42) {
		t.Fatalf("count_tokens %d: %v", r.StatusCode, m)
	}

	// wrong kind on every endpoint
	for path, body := range map[string]string{
		"/v1/images/generations":     `{"model":"test/text","prompt":"x"}`,
		"/v1/rerank":                 `{"model":"test/img","query":"q","documents":["a"]}`,
		"/v1/ocr":                    `{"model":"test/rerank","document":{"type":"image_url","image_url":"https://example.com/a.png"}}`,
		"/v1/responses/input_tokens": `{"model":"test/rerank","input":"x"}`,
	} {
		if r := f.post(t, path, f.key, body); errCode(readJSON(t, r)) != "wrong_model_kind" {
			t.Errorf("%s accepted a wrong-kind model", path)
		}
	}
	res = multipartPost(t, srv.URL+"/v1/images/edits", f.key, map[string]string{"model": "test/text", "prompt": "x"})
	if errCode(readJSON(t, res)) != "wrong_model_kind" {
		t.Error("image edit accepted a chat model")
	}

	// catalog pricing
	r, _ = http.DefaultClient.Do(func() *http.Request {
		q, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
		q.Header.Set("Authorization", "Bearer "+f.key)
		return q
	}())
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	var list struct{ Data []ModelInfo }
	_ = json.Unmarshal(raw, &list)
	for _, mi := range list.Data {
		if mi.ID == "test/img" && mi.Pricing.Image != "0.04" || mi.ID == "test/rerank" && mi.Pricing.Request != "0.002" {
			t.Fatalf("pricing %s: %+v", mi.ID, mi.Pricing)
		}
	}

	// guardrails apply to the image prompt and the rerank query/documents (nothing billed)
	if _, err := f.g.S.UpdateOrg(ctx, f.org.ID, func(o *store.Org) error {
		o.Guardrails = store.Guardrails{BlockedPatterns: []string{`(?i)secret plan`}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"/v1/images/generations": `{"model":"test/img","prompt":"draw the SECRET PLAN"}`,
		"/v1/rerank":             `{"model":"test/rerank","query":"q","documents":["a","our secret plan"]}`,
	} {
		if r := f.post(t, path, f.key, body); errCode(readJSON(t, r)) != "guardrail_blocked" {
			t.Errorf("%s: guardrail not applied", path)
		}
	}
	res = multipartPost(t, srv.URL+"/v1/images/edits", f.key, map[string]string{"model": "test/img", "prompt": "secret plan"})
	if errCode(readJSON(t, res)) != "guardrail_blocked" {
		t.Error("image edit: guardrail not applied")
	}

	// spend: images 80_050 + 40_000 + 40_000, rerank 2_000 + 50_000, ocr 1_000, count tokens 0
	f.g.Usage.Close()
	time.Sleep(10 * time.Millisecond)
	a, _ := f.g.S.KV.GetAgg(ctx, usage.OrgSpendKey(f.org.ID))
	if want := int64(80_050 + 40_000 + 40_000 + 2_000 + 50_000 + 1_000); a[store.AggCostMicros] != want {
		t.Fatalf("spend %d, want %d", a[store.AggCostMicros], want)
	}
}
