package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// logLevel parses LOG_LEVEL: debug, info, warn (warning) or error.
func logLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("unknown level %q (debug, info, warn, error)", s)
}

// withAccessLog logs one debug line per HTTP request: method, path (no query string, which may
// carry tokens), status, bytes, duration and client address. Headers and bodies are never logged.
// Off (no wrapper, no cost) unless LOG_LEVEL=debug.
func withAccessLog(h http.Handler) http.Handler {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		if sw.status == 0 && !sw.hijacked {
			sw.status = http.StatusOK
		}
		slog.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "bytes", sw.bytes,
			"ms", time.Since(t).Milliseconds(), "remote", r.RemoteAddr)
	})
}

// statusWriter records the status and size. Unwrap keeps http.ResponseController (streaming
// flushes, deadlines) working; Hijack keeps the proxy-pool CONNECT handler working.
type statusWriter struct {
	http.ResponseWriter
	status   int
	bytes    int64
	hijacked bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	w.hijacked = true
	return hj.Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
