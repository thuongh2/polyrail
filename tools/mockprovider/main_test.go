package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddingsHashedBagOfWords(t *testing.T) {
	rec := httptest.NewRecorder()
	embeddings(rec, httptest.NewRequest("POST", "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":["refund policy days","Refund POLICY days!","travel portal"],"dimensions":32}`)))
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 3 || len(out.Data[0].Embedding) != 32 || out.Data[2].Index != 2 || out.Usage.PromptTokens != 8 {
		t.Fatalf("response %s", rec.Body)
	}
	dot := func(a, b []float64) (s float64) {
		for i := range a {
			s += a[i] * b[i]
		}
		return
	}
	if same := dot(out.Data[0].Embedding, out.Data[1].Embedding); same < 0.999 {
		t.Fatalf("same words: cos %f", same)
	}
	if diff := dot(out.Data[0].Embedding, out.Data[2].Embedding); diff > 0.9 {
		t.Fatalf("different words: cos %f", diff)
	}
	rec = httptest.NewRecorder()
	embeddings(rec, httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(`{"input":"hello"}`)))
	if !strings.Contains(rec.Body.String(), `"index":0`) || strings.Count(rec.Body.String(), ",")+1 < 64 {
		t.Fatalf("default dims: %s", rec.Body)
	}
}

func TestKeyFaults(t *testing.T) {
	h := keyFaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for key, want := range map[string]int{"bad-1": 401, "rl-1": 429, "slow-x": 429, "good": 200, "": 200} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		h.ServeHTTP(rec, req)
		if rec.Code != want || (want == 429 && rec.Header().Get("Retry-After") != "5") {
			t.Errorf("key %q: %d %v", key, rec.Code, rec.Header())
		}
	}
}

func TestChatUsageCachesEarlierTurns(t *testing.T) {
	var req chatReq
	_ = json.Unmarshal([]byte(`{"messages":[{"content":"0123456789abcdef"},{"content":"01234567"}]}`), &req)
	u := chatUsage(req, "abcd")
	if u["prompt_tokens"] != 5+4+2 || u["completion_tokens"] != 2 || u["prompt_tokens_details"].(map[string]int)["cached_tokens"] != 4 {
		t.Fatalf("usage %v", u)
	}
	req.Messages = req.Messages[1:] // a single turn has nothing cached
	if _, ok := chatUsage(req, "abcd")["prompt_tokens_details"]; ok {
		t.Fatal("single turn reported cached tokens")
	}
}

func TestChatReplyModes(t *testing.T) {
	parse := func(body string) chatReq {
		var req chatReq
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		return req
	}
	text, calls := chatReply(parse(`{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`))
	if calls != nil || !strings.Contains(text, "1 image(s)") || !strings.Contains(text, "```go") || !strings.Contains(text, "[1]") {
		t.Fatalf("markdown reply: %q", text)
	}
	text, _ = chatReply(parse(`{"messages":[{"role":"user","content":"x"}],"response_format":{"type":"json_schema","json_schema":{"name":"p","schema":{"type":"object","properties":{"n":{"type":"integer"},"tags":{"type":"array","items":{"type":"string"}}}}}}}`))
	if text != `{"n":42,"tags":["mock tags"]}` {
		t.Fatalf("json_schema reply: %s", text)
	}
	_, calls = chatReply(parse(`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`))
	if len(calls) != 1 || calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"city":"mock city"}` {
		t.Fatalf("tool calls: %v", calls)
	}
	text, calls = chatReply(parse(`{"messages":[{"role":"tool","tool_call_id":"call_mock_1","content":"{\"temp\":21}"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`))
	if calls != nil || !strings.Contains(text, `{"temp":21}`) {
		t.Fatalf("tool result reply: %q %v", text, calls)
	}
}
