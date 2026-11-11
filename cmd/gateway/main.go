// Command gateway runs agent-gateway: an OpenAI-compatible AI gateway with multi-tenancy,
// pricing, routing and analytics, built on Bifrost core. Configuration is by environment variable;
// see docs/CONFIG.md.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/admin"
	"github.com/agent-gateway/agent-gateway/internal/gateway"
	"github.com/agent-gateway/agent-gateway/internal/limits"
	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/agent-gateway/agent-gateway/internal/usage"
	"github.com/agent-gateway/agent-gateway/internal/web"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// version is set at build time: go build -ldflags "-X main.version=v1.2.3".
var version = "dev"

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	n, err := strconv.Atoi(os.Getenv(k))
	if err != nil {
		return def
	}
	return n
}

func envBool(k string) bool { b, _ := strconv.ParseBool(os.Getenv(k)); return b }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" { // for container HEALTHCHECK without curl
		resp, err := http.Get("http://127.0.0.1" + listenPort() + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	level := slog.LevelInfo
	if env("LOG_LEVEL", "info") == "debug" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func listenPort() string {
	addr := env("LISTEN_ADDR", ":8080")
	return addr[strings.LastIndex(addr, ":"):]
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	masterKey := os.Getenv("MASTER_KEY")
	box, err := store.NewBox(masterKey)
	if err != nil {
		return err
	}
	var kv store.KV
	switch env("STORE", "pebble") {
	case "pebble":
		kv, err = store.OpenPebble(env("DATA_DIR", "./data"), envInt("PEBBLE_CACHE_MB", 32))
	case "postgres":
		kv, err = store.OpenPostgres(ctx, os.Getenv("POSTGRES_URL"))
	default:
		err = fmt.Errorf("STORE must be pebble or postgres")
	}
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer kv.Close()
	s := store.New(kv, box)

	var lim limits.Limiter = limits.NewLocal()
	if u := os.Getenv("REDIS_URL"); u != "" {
		r, err := limits.NewRedis(u)
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		if err := r.Ping(ctx); err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		lim = r
	}

	sinks := []usage.LogSink{usage.StoreSink{S: s}}
	if u := os.Getenv("CLICKHOUSE_URL"); u != "" {
		ch, err := usage.NewClickHouse(ctx, u)
		if err != nil {
			return fmt.Errorf("clickhouse: %w", err)
		}
		sinks = append(sinks, ch)
	}
	up := usage.New(kv, envInt("USAGE_QUEUE", 10_000), time.Second, sinks...)
	prometheus.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "gateway_usage_logs_dropped_total", Help: "Request log rows dropped (billing counters are never dropped).",
	}, func() float64 { return float64(up.DroppedLogs.Load()) }))

	allowPrivate := envBool("ALLOW_PRIVATE_PROVIDER_URLS")
	g, err := gateway.New(ctx, s, lim, up, gateway.Config{
		MaxBodyBytes: int64(envInt("MAX_BODY_MB", 8)) << 20,
		Concurrency:  envInt("PROVIDER_CONCURRENCY", 64),
		Buffer:       envInt("PROVIDER_BUFFER", 256),
		CacheMB:      envInt("RESPONSE_CACHE_MB", 64),
		PollInterval: 2 * time.Second,

		AllowPrivateURLs: allowPrivate,
	})
	if err != nil {
		return err
	}

	publicURL := strings.TrimRight(env("PUBLIC_URL", "http://localhost"+listenPort()), "/")
	acfg := admin.Config{
		MasterKey:        masterKey,
		SecureCookies:    strings.HasPrefix(publicURL, "https://"),
		AllowSignup:      envBool("ALLOW_SIGNUP"),
		SignupPrepaid:    envBool("SIGNUP_PREPAID"),
		AllowPrivateURLs: allowPrivate,
		LogRetentionDays: envInt("LOG_RETENTION_DAYS", 14),
	}
	if iss := os.Getenv("OIDC_ISSUER"); iss != "" {
		var domains []string
		for _, d := range strings.Split(os.Getenv("OIDC_ALLOWED_DOMAINS"), ",") {
			if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
				domains = append(domains, d)
			}
		}
		acfg.OIDC = &admin.OIDCConfig{Issuer: iss, ClientID: os.Getenv("OIDC_CLIENT_ID"), ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
			RedirectURL: publicURL + "/api/auth/oidc/callback", AllowedDomains: domains,
			DefaultOrg: os.Getenv("OIDC_DEFAULT_ORG"), DefaultRole: store.Role(env("OIDC_DEFAULT_ROLE", "member")),
			TrustUnverifiedEmail: envBool("OIDC_TRUST_UNVERIFIED_EMAIL")}
	}
	api, err := admin.New(ctx, s, g, lim, acfg)
	if err != nil {
		return err
	}
	if err := bootstrap(ctx, s); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.ChatCompletions)
	mux.HandleFunc("POST /v1/embeddings", g.Embeddings)
	mux.HandleFunc("GET /v1/models", g.Models)
	mux.HandleFunc("POST /v1/completions", g.Completions)
	mux.HandleFunc("POST /v1/responses", g.Responses)
	mux.HandleFunc("POST /v1/audio/speech", g.Speech)
	mux.HandleFunc("POST /v1/audio/transcriptions", g.Transcriptions)
	mux.HandleFunc("POST /v1/mcp/tool/execute", g.MCPExecute)
	mux.HandleFunc("POST /anthropic/v1/messages", g.AnthropicMessages)
	mux.HandleFunc("GET /anthropic/v1/models", g.AnthropicModels)
	aliases(mux)
	api.Register(mux)
	mux.Handle("GET /metrics", metricsAuth(os.Getenv("METRICS_TOKEN"), promhttp.Handler()))
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := kv.Ping(r.Context()); err != nil {
			http.Error(w, "store unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", web.Handler())

	go retention(ctx, s, acfg.LogRetentionDays)

	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: withCORS(mux),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	// No global WriteTimeout: streams can run for minutes; each stream write sets its own deadline.
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("agent-gateway listening", "addr", srv.Addr, "store", env("STORE", "pebble"), "public_url", publicURL)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	up.Close()
	g.BF.Shutdown()
	return nil
}

func healthz(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }

// aliases registers SDK base-URL aliases and Bifrost-compatible meta endpoints:
// /openai/v1/* serves exactly what /v1/* serves (OpenAI SDK base_url ending in /openai/v1),
// GET /health is /healthz, and GET /api/version reports the build.
func aliases(mux *http.ServeMux) {
	mux.Handle("/openai/v1/", http.StripPrefix("/openai", mux))
	mux.HandleFunc("GET /health", healthz)
	core := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/maximhq/bifrost/core" {
				core = d.Version
			}
		}
	}
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version, "bifrost_core": core})
	})
}

// metricsAuth requires "Authorization: Bearer <METRICS_TOKEN>" when a token is configured.
func metricsAuth(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// withCORS lets browser apps call the OpenAI-compatible API (key auth, no cookies).
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/openai/v1/") || strings.HasPrefix(p, "/anthropic/v1/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, anthropic-beta, x-gateway-cache, x-gateway-mcp-clients")
			w.Header().Set("Access-Control-Expose-Headers", "x-gateway-provider, x-gateway-request-id, x-gateway-cache-hit")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

// bootstrap creates the operator account (and a default org) from ADMIN_EMAIL / ADMIN_PASSWORD.
func bootstrap(ctx context.Context, s *store.Store) error {
	email, pw := os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD")
	if email == "" {
		return nil
	}
	if _, err := s.GetUserByEmail(ctx, email); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	hash, err := admin.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("ADMIN_PASSWORD: %w", err)
	}
	u := &store.User{Email: email, Name: "Operator", PasswordHash: hash, Operator: true}
	if err := s.CreateUser(ctx, u); err != nil {
		return err
	}
	slog.Info("created operator account", "email", u.Email)
	return s.CreateOrg(ctx, &store.Org{Name: "Default"}, u.ID)
}

func retention(ctx context.Context, s *store.Store, days int) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		if err := s.PruneLogs(ctx, days); err != nil {
			slog.Error("log retention", "err", err)
		}
		if err := s.PruneHealth(ctx, 30); err != nil { // public provider health keeps 30 days
			slog.Error("health retention", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
