// Command polyrail runs Polyrail: an OpenAI-compatible AI gateway with multi-tenancy,
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
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/polyrail/polyrail/internal/admin"
	"github.com/polyrail/polyrail/internal/alert"
	"github.com/polyrail/polyrail/internal/broadcast"
	"github.com/polyrail/polyrail/internal/catalog"
	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/kb"
	"github.com/polyrail/polyrail/internal/limits"
	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
	"github.com/polyrail/polyrail/internal/web"
	"github.com/polyrail/polyrail/internal/webhook"
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

// globalProxy reads UPSTREAM_PROXY_URL, UPSTREAM_NO_PROXY and UPSTREAM_PROXY_CA_FILE; a bad
// setting stops startup instead of sending traffic around the proxy.
func globalProxy() (*gateway.GlobalProxy, error) {
	var ca []byte
	if f := os.Getenv("UPSTREAM_PROXY_CA_FILE"); f != "" {
		var err error
		if ca, err = os.ReadFile(f); err != nil {
			return nil, fmt.Errorf("UPSTREAM_PROXY_CA_FILE: %w", err)
		}
	}
	p, err := gateway.ParseGlobalProxy(env("UPSTREAM_PROXY_URL", ""), env("UPSTREAM_NO_PROXY", ""), string(ca))
	if err != nil {
		return nil, fmt.Errorf("UPSTREAM_PROXY_URL: %w", err)
	}
	if p == nil && len(ca) > 0 {
		return nil, errors.New("UPSTREAM_PROXY_CA_FILE is set without UPSTREAM_PROXY_URL")
	}
	if p != nil {
		slog.Info("upstream proxy", "url", p.URL, "no_proxy", p.NoProxy)
	}
	return p, nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" { // for container HEALTHCHECK without curl
		resp, err := http.Get("http://127.0.0.1" + listenPort() + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	level, lerr := logLevel(env("LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	if lerr != nil {
		slog.Warn("LOG_LEVEL not understood, using info", "err", lerr)
	}
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
	proxy, err := globalProxy()
	if err != nil {
		return err
	}
	tenantProxy := envBool("ALLOW_TENANT_PROXY")
	probeEvery, err := time.ParseDuration(env("PROXY_HEALTH_INTERVAL", "10m"))
	if err != nil || probeEvery < 0 {
		return errors.New("PROXY_HEALTH_INTERVAL must be a duration like 10m (0 turns probes off)")
	}
	if pool := env("UPSTREAM_PROXY_POOL", ""); pool != "" {
		slog.Info("upstream proxy pool", "pool", pool)
	}
	regionLock := env("REGION_LOCK", "")
	if regionLock != "" {
		if regionLock = store.NormalizeRegion(regionLock); regionLock == "global" {
			return errors.New("REGION_LOCK must name a region (europe, us, ...); leave it unset for no lock")
		}
		slog.Info("region lock: requests only reach providers of this data region", "region", regionLock)
	}
	gateway.Version = version
	g, err := gateway.New(ctx, s, lim, up, gateway.Config{
		MaxBodyBytes: int64(envInt("MAX_BODY_MB", 8)) << 20,
		Concurrency:  envInt("PROVIDER_CONCURRENCY", 64),
		Buffer:       envInt("PROVIDER_BUFFER", 256),
		CacheMB:      envInt("RESPONSE_CACHE_MB", 64),
		PollInterval: 2 * time.Second,
		DebugLog:     slog.Default().Enabled(context.Background(), slog.LevelDebug),

		AllowPrivateURLs: allowPrivate,
		Proxy:            proxy,
		AllowTenantProxy: tenantProxy,

		ProxyPool:           env("UPSTREAM_PROXY_POOL", ""),
		ProxyNoProxy:        env("UPSTREAM_NO_PROXY", ""),
		ProxyHealthInterval: probeEvery,
		RegionLock:          regionLock,
	})
	if err != nil {
		return err
	}

	// knowledge bases (docs/KB.md): URL ingestion only reaches public hosts unless private
	// provider URLs are allowed
	checkURL, dialer := gateway.CheckPublicURL, gateway.PublicDialer(15*time.Second)
	if allowPrivate {
		checkURL, dialer = func(string) error { return nil }, &net.Dialer{Timeout: 15 * time.Second}
	}
	g.KB = kb.New(s, g, kb.Config{
		Workers:      envInt("KB_WORKERS", 1),
		IndexMB:      envInt("KB_INDEX_MB", 256),
		MaxOrgChunks: envInt("KB_MAX_ORG_CHUNKS", 300000),
		MaxDocMB:     envInt("KB_MAX_DOC_MB", 10),
		CheckURL:     checkURL,
		DialContext:  dialer.DialContext,
	})
	g.KB.Start(ctx)

	publicURL := strings.TrimRight(env("PUBLIC_URL", "http://localhost"+listenPort()), "/")
	acfg := admin.Config{
		MasterKey:        masterKey,
		SecureCookies:    strings.HasPrefix(publicURL, "https://"),
		AllowSignup:      envBool("ALLOW_SIGNUP"),
		SignupPrepaid:    envBool("SIGNUP_PREPAID"),
		AllowPrivateURLs: allowPrivate,
		AllowTenantProxy: tenantProxy,
		LogRetentionDays: envInt("LOG_RETENTION_DAYS", 14),
		PublicURL:        publicURL,
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
	hooks := webhook.New(s, allowPrivate, envInt("WEBHOOK_QUEUE", 1000))
	hooks.Start(ctx, 4)
	alerts := alert.New(s, allowPrivate, envInt("ALERT_QUEUE", 1000))
	alerts.Start(ctx, 2)
	g.Hooks, api.Hooks, api.Alerts = gateway.Fanout{hooks, alerts}, hooks, alerts
	traces := broadcast.New(s, allowPrivate, envInt("TRACE_QUEUE", 10000))
	traces.Start(ctx, 2)
	g.Traces, api.Traces = traces, traces
	if err := bootstrap(ctx, s); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	if err := seedCatalog(ctx, s); err != nil {
		return fmt.Errorf("seed catalog: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", g.Idempotent(g.ChatCompletions))
	mux.HandleFunc("POST /v1/embeddings", g.Idempotent(g.Embeddings))
	mux.HandleFunc("GET /v1/models", g.Models)
	mux.HandleFunc("POST /v1/models/query", g.ModelsQuery)
	// async APIs: files, batches, videos (gateway ids, tenant-scoped; see internal/gateway/async.go)
	mux.HandleFunc("POST /v1/files", g.UploadFile)
	mux.HandleFunc("GET /v1/files", g.ListFiles)
	mux.HandleFunc("GET /v1/files/{id}", g.GetFile)
	mux.HandleFunc("GET /v1/files/{id}/content", g.FileContent)
	mux.HandleFunc("DELETE /v1/files/{id}", g.DeleteFile)
	mux.HandleFunc("POST /v1/batches", g.CreateBatch)
	mux.HandleFunc("GET /v1/batches", g.ListBatches)
	mux.HandleFunc("GET /v1/batches/{id}", g.GetBatch)
	mux.HandleFunc("POST /v1/batches/{id}/cancel", g.CancelBatch)
	mux.HandleFunc("POST /v1/videos", g.CreateVideo)
	mux.HandleFunc("GET /v1/videos/{id}", g.GetVideo)
	mux.HandleFunc("GET /v1/videos/{id}/content", g.VideoContent)
	mux.HandleFunc("DELETE /v1/videos/{id}", g.DeleteVideo)
	mux.HandleFunc("POST /v1/vector_stores", g.CreateVectorStore)
	mux.HandleFunc("GET /v1/vector_stores", g.ListVectorStores)
	mux.HandleFunc("GET /v1/vector_stores/{id}", g.GetVectorStore)
	mux.HandleFunc("DELETE /v1/vector_stores/{id}", g.DeleteVectorStore)
	mux.HandleFunc("POST /v1/vector_stores/{id}/files", g.CreateVectorStoreFile)
	mux.HandleFunc("GET /v1/vector_stores/{id}/files", g.ListVectorStoreFiles)
	mux.HandleFunc("GET /v1/vector_stores/{id}/files/{file_id}", g.GetVectorStoreFile)
	mux.HandleFunc("DELETE /v1/vector_stores/{id}/files/{file_id}", g.DeleteVectorStoreFile)
	mux.HandleFunc("POST /v1/vector_stores/{id}/search", g.SearchVectorStore)
	mux.HandleFunc("POST /v1/completions", g.Completions)
	mux.HandleFunc("POST /v1/responses", g.Idempotent(g.Responses))
	mux.HandleFunc("POST /v1/audio/speech", g.Speech)
	mux.HandleFunc("POST /v1/audio/transcriptions", g.Transcriptions)
	mux.HandleFunc("POST /v1/images/generations", g.Idempotent(g.ImageGenerations))
	mux.HandleFunc("POST /v1/images/edits", g.ImageEdits)
	mux.HandleFunc("POST /v1/images/variations", g.ImageVariations)
	mux.HandleFunc("POST /v1/rerank", g.Rerank)
	mux.HandleFunc("POST /v1/ocr", g.OCR)
	mux.HandleFunc("POST /v1/responses/input_tokens", g.ResponsesInputTokens)
	mux.HandleFunc("POST /v1/mcp/tool/execute", g.MCPExecute)
	mux.HandleFunc("POST /anthropic/v1/messages", g.AnthropicMessages)
	mux.HandleFunc("POST /anthropic/v1/messages/count_tokens", g.AnthropicCountTokens)
	mux.HandleFunc("GET /anthropic/v1/models", g.AnthropicModels)
	for _, v := range []string{"v1beta", "v1"} { // Google GenAI SDK base URL: http://host/genai
		mux.HandleFunc("POST /genai/"+v+"/models/{rest...}", g.GenAI)
		mux.HandleFunc("GET /genai/"+v+"/models", g.GenAIModels)
		mux.HandleFunc("GET /genai/"+v+"/models/{rest...}", g.GenAIModel)
	}
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

	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8080"), Handler: withAccessLog(withCORS(mux)),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second}
	// No global WriteTimeout: streams can run for minutes; each stream write sets its own deadline.
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("polyrail listening", "addr", srv.Addr, "store", env("STORE", "pebble"), "public_url", publicURL)

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
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "polyrail", "version": version, "bifrost_core": core})
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
		if p := r.URL.Path; strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/openai/v1/") || strings.HasPrefix(p, "/anthropic/v1/") || strings.HasPrefix(p, "/genai/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, anthropic-beta, x-goog-api-key, x-goog-api-client, x-gateway-cache, x-gateway-mcp-clients, Idempotency-Key, X-GW-KB")
			w.Header().Set("Access-Control-Expose-Headers", "x-gateway-provider, x-gateway-request-id, x-gateway-cache-hit, X-GW-Cost-Micros, Idempotent-Replayed, X-GW-Cache, X-GW-Cache-Score, X-GW-Route, X-GW-Guardrail, X-GW-Compressed-Chars, X-GW-KB, X-GW-KB-Sources")
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

// seedCatalog loads the embedded model catalog into a store with no providers at all (a fresh
// install): disabled keyless providers plus their models, which stay hidden until an operator adds
// a key and enables a provider. SEED_CATALOG=0 turns it off; existing installs use
// POST /api/admin/catalog/sync instead.
func seedCatalog(ctx context.Context, s *store.Store) error {
	if os.Getenv("SEED_CATALOG") == "0" {
		return nil
	}
	if ps, err := s.ListProviders(ctx, false); err != nil || len(ps) > 0 {
		return err
	}
	np, nm, _, err := catalog.Sync(ctx, s)
	if err != nil {
		return err
	}
	slog.Info("seeded model catalog", "providers", np, "models", nm)
	return s.Audit(ctx, store.AuditEntry{OrgID: "_", Actor: "system", Action: "catalog.seed", Target: "catalog",
		Detail: fmt.Sprintf("providers_added=%d models_added=%d", np, nm)})
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
		if err := s.PurgeContent(ctx); err != nil { // opt-in content logs: each org's retention_days
			slog.Error("content log retention", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
