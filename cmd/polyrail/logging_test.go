package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "INFO": slog.LevelInfo, "": slog.LevelInfo, "warning": slog.LevelWarn, "error": slog.LevelError} {
		if got, err := logLevel(in); err != nil || got != want {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	if _, err := logLevel("verbose"); err == nil {
		t.Fatal("unknown level must be reported")
	}
}

// Debug access log: one line per request with status and path, never the query string; streaming
// flushes still reach the client; no wrapper at info level.
func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h := withAccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through the wrapper: %v", err)
		}
		w.Write([]byte("tea"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models?token=secret", nil))
	line := buf.String()
	if !strings.Contains(line, `"status":418`) || !strings.Contains(line, `"path":"/v1/models"`) || !strings.Contains(line, `"bytes":3`) || strings.Contains(line, "secret") {
		t.Fatalf("log line: %s", line)
	}
	if !rec.Flushed {
		t.Fatal("flush not forwarded")
	}
	buf.Reset()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	withAccessLog(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if buf.Len() != 0 {
		t.Fatalf("info level logs no requests: %s", buf.String())
	}
}
