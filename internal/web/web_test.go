package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSPAFallbackAndAssets(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/", "/models", "/models/openai/gpt-4o", "/app/keys"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/missing.js", nil))
	if rec.Code != 404 {
		t.Fatalf("missing asset: %d", rec.Code)
	}
}
