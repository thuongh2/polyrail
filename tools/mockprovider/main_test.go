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
