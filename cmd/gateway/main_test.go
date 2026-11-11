package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAliasesAndVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "chat "+r.URL.Path) })
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "models") })
	aliases(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := func(method, path string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(b)
	}
	if c, b := get("POST", "/openai/v1/chat/completions"); c != 200 || b != "chat /v1/chat/completions" {
		t.Fatalf("openai alias: %d %q", c, b)
	}
	if c, b := get("GET", "/openai/v1/models"); c != 200 || b != "models" {
		t.Fatalf("openai alias models: %d %q", c, b)
	}
	if c, _ := get("GET", "/openai/v1/chat/completions"); c != 405 {
		t.Fatalf("alias must keep method rules: %d", c)
	}
	if c, b := get("GET", "/health"); c != 200 || b != "ok" {
		t.Fatalf("health: %d %q", c, b)
	}
	c, b := get("GET", "/api/version")
	var v map[string]string
	if err := json.Unmarshal([]byte(b), &v); c != 200 || err != nil || v["version"] != "dev" || v["bifrost_core"] == "" {
		t.Fatalf("version: %d %s", c, b)
	}
}
