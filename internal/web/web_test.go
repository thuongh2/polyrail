package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAFallbackAndAssets(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/", "/models", "/models/openai/gpt-4o", "/models/meta-llama/llama-3.3-70b", "/app/keys"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/assets/missing.js", "/logos/missing.svg"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 404 {
			t.Fatalf("missing %s: %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/logos/openai.svg", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("logo: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
