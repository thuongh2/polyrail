# Progress board

Legend: [ ] todo · [x] done (evidence: the tests named in docs/TESTING.md, run with
`TEST_POSTGRES_URL=… TEST_REDIS_URL=… make check`).

## Milestone 1 — small mode end to end
- [x] T1 store: KV seam + Pebble backend + entity layer (orgs, users, members, keys, providers, models, audit, logs)
- [x] T2 gateway: Bifrost account adapter, catalog snapshot, router (weights, fallbacks, circuit breaker)
- [x] T3 gateway: /v1/chat/completions (+stream), /v1/embeddings, /v1/models, OpenAI error shapes
- [x] T4 limits (RPM/TPM), credits and key budgets, guardrails, response cache
- [x] T5 usage pipeline: batched counters, rollups, request logs, retention
- [x] T6 admin API: password sessions, RBAC, CRUD, audit, analytics
- [x] T7 OIDC SSO (code path reviewed; no automated test — needs a mock IdP)
- [x] T8 MCP servers via Bifrost with per-org grants (unit-tested; no live MCP server test)
- [x] T9 metrics, health, structured logs
- [x] T10 web console + public catalog
- [x] T11 e2e test with mock provider; resource smoke (below)
- [x] T12 Dockerfile, compose, systemd unit, README and docs (Dockerfile not built: no Docker daemon in the dev sandbox)

## Milestone 2 — scale mode
- [x] T13 Postgres KV backend (same KV conformance test + full e2e on Postgres)
- [x] T14 Redis limiter (conformance test + e2e)
- [x] T15 ClickHouse raw log sink (tested against a fake HTTP server only)
- [x] T16 compose for scale mode (not run here)

## Milestone 3 — OpenRouter-style UI (2026-11-11)
- [x] T17 Provider health rollups + `/api/public/models/{id}` and `/api/public/status` (backend agent, tests)
- [x] T18 Vite + React + Tailwind + shadcn scaffold, SPA serving from Go (`internal/web`, test)
- [x] T19 Public pages: home, models with filters, model detail (providers, 72 h uptime, API samples), status, docs, login (public UI agent)
- [x] T20 Console: activity, logs, playground (streaming chat), keys, credits, members, settings, audit, account (console UI agent)
- [x] T21 Platform: providers, models with routing editor, organizations, users, MCP (platform UI agent)
- [x] T22 UI review (slop, a11y, security): open redirect in login fixed, clipboard over http, contrast tokens, bundle 74 kB gzip

## Reviews
- [x] Security review → docs/SECURITY.md; all high/medium fixed or mitigated with tests
- [x] Anti-slop review → 15 findings fixed (docs aligned with code, seam trimmed, duplicates removed, swallowed errors surfaced, UI states)
- [x] QA acceptance pass → docs/TESTING.md

## Resource smoke (F9-AC2)

Method, on the 2-core dev sandbox, 2026-11-10:

```sh
go run ./tools/mockprovider &                       # fixed-answer upstream on :9901
GOMAXPROCS=1 MASTER_KEY=… ADMIN_EMAIL=… ADMIN_PASSWORD=… ./bin/polyrail &
# console: public provider type openai, base URL http://127.0.0.1:9901; model mock/fast; create a key
go run ./tools/loadtest http://127.0.0.1:8088/v1/chat/completions sk-gw-…   # 5000 req, concurrency 50
```

Result: idle RSS 30 MB; peak RSS 55 MB; `requests=5000 errors=0 rps=3645 p50=10.9ms p95=25.2ms
p99=55.0ms`; the usage endpoint then reported 5000 requests. Same load straight to the mock:
`rps=11281 p50=2.9ms`. The load generator and mock shared the machine with the gateway.

## Milestone 4 — Bifrost core v1.15.2 (2026-11-11)
- [x] T23 Go 1.27.3 built from the official tag (owner approved); core v1.2.38 → v1.15.2, all behaviour kept
- [x] T24 Images, rerank, OCR, token counting (media.go)
- [x] T25 Files, batches, videos with tenant-owned ids and exactly-once billing (async.go, store/jobs.go)
- [x] T26 Google GenAI SDK drop-in, 16 more provider types, model discovery and import
- [x] T27 UI for all new kinds, discovery dialog, image and rerank playground modes
- [x] T28 Security review: H5 (upstream-URL SSRF via replicate/huggingface/gemini) and M9/M10 fixed
- Resource smoke on v1.15.2 (same method as below, GOMAXPROCS=1): idle RSS 59 MB, peak 69 MB,
  `requests=5000 errors=0 rps=7690 p50=5.3ms p95=12.7ms p99=17.7ms`. Binary 46 MB (was 33 MB).
- [x] Outbound proxy for upstream calls (D22): global `UPSTREAM_PROXY_URL` / `UPSTREAM_NO_PROXY` /
  `UPSTREAM_PROXY_CA_FILE`, per-provider `proxy` + `no_proxy`, tenant proxies behind
  `ALLOW_TENANT_PROXY` with a public-host check; provider dialog Proxy section and "via proxy" badge.
- [x] Provider keys and alert channels backend (D25, docs/KEYS.md): per-key status (cooldown,
  quarantine, disabled) in `GetKeysForProvider`, attempt-trail attribution, hourly `keystat/` stats,
  key API for both scopes (list/bulk add/patch/delete/bulk/test/stats), shoutrrr alert channels.
  UI in progress (parallel task).
- [x] Proxy pools (D23, OmniRoute proxy parity, scoped): operator pool registry, provider
  `proxy_pool` and global `UPSTREAM_PROXY_POOL`, loopback CONNECT forwarder with round-robin /
  random / sticky / latency, set-aside 60→600 s, down after 3 failures, TCP probes, fail-closed,
  tenant target pinning, Platform → Proxies page; provider 429 never rotates.

## Next (not started)
- OIDC test against a mock IdP; live MCP server test.
- Payment integration for credits (Stripe), email verification for sign-up.
- Dial-time SSRF enforcement inside Bifrost's HTTP client (needs a Bifrost hook).

## Log
- 2026-11-10 Team, memory, PRD, architecture written. Bifrost core v1.2.38 builds on Go 1.24.
- 2026-11-10 M1 + M2 built; QA found 3 bugs, security found 4 high / 8 medium; all fixed with regression tests.
- 2026-11-10 Slop review fixes; resource smoke recorded.
- 2026-11-11 Cloud providers: Azure OpenAI, AWS Bedrock (multi-instance, org-allowed), Google Vertex AI, ElevenLabs; per-key config sealed at rest (D13); provider dialog fields; screenshots in /tmp/claude-0/ui-shots/providers/.
- 2026-11-11 Bifrost core v1.2.38 → v1.15.2 on Go 1.27.3 (built from source, owner-approved). Same HTTP API; MCP names validated, tools exposed as `<server>-<tool>`. `make check` green; binary 33 → 47 MB, idle RSS 28 → 55 MB (D1).
- 2026-11-11 OmniRoute wave 1: 9 routing strategies, fault-only breaker, Retry-After cooldown, `X-GW-Route` (D14); guardrails: credential masking, prompt-injection warn/block, lossless compression (D15); `POST /v1/models/query`, `Idempotency-Key`, `X-GW-Cost-Micros` (D16); signed org webhooks (D17); opt-in semantic cache (D18); UI (strategy + route prices, guardrails, semantic cache, Webhooks page, docs); security review M11, L16 fixed, L17–L20 recorded. `go vet ./... && go test -race ./...` green incl. Postgres and Redis.
- 2026-11-11 Production catalog (D19): `scripts/gencatalog.py` → `internal/catalog/catalog.json` from models.dev 3751776 (19 providers, 265 models, 121 KB); first start seeds disabled keyless `cat-<type>` providers + models (`SEED_CATALOG`); `POST /api/admin/catalog/sync` + "Load model catalog" button; models listed/routable only with an enabled provider; `run-local.sh` defaults to `DEMO=0`. vet + race tests green incl. Postgres and Redis.
- 2026-11-11 OpenAI-compatible vendors (groq, deepseek, xai, mistral, openrouter, fireworks, cerebras, perplexity, nebius, parasail, minimax, sarvam, wafer, opencode-go/zen) are org provider types on Bifrost's openai base at the vendor's fixed URL (D20); org `base_url` refused; UI hides Base URL for them. `gofmt`, `go vet ./...`, `go test -race ./...` green incl. Postgres and Redis.
- 2026-11-11 Knowledge bases task A (D21, docs/KB.md): `internal/store/kb.go` (KB/doc/source/queue records, leased per-doc queue, Pebble + Postgres tests), `APIKey.KBIDs`, `DeleteOrg` removes `kb*`; `internal/kb` engine (extract text/md/csv/json/html, chunker, URL fetch via injected check + dialer, int8 vectors + BM25 + RRF, per-KB LRU index with singleflight and rev reload, KV vector store, workers with retries); mockprovider embeddings = hashed bag of words. `BenchmarkSearch100k`: ~31 ms/op at 512 dims, ~1.6 KB/chunk. Gateway grounding, APIs and UI are tasks B/C. vet + race tests green incl. Postgres and Redis.
- 2026-11-11 Knowledge bases task B (D21): `Gateway.Embed` (kb.Embedder: batch Bifrost embeddings, `dimensions`, budgets → `kb.ErrInsufficientCredits`, billed to key or console; semantic cache reuses it); grounding for chat (stream + non-stream), Responses, Anthropic, GenAI via `X-GW-KB` / key `kb_ids` (system message after the last user turn, escaped delimiters, `X-GW-KB`/`X-GW-KB-Sources`, fail open, `kb_chunks` in logs, `gw_kb_grounded_total`); exact + semantic cache keys include `id@rev`; admin `/api/orgs/{org}/kbs…` (roles, audit, multipart/JSON/URL docs), key `kb_ids`; `/v1/vector_stores` subset (creator-only mutations); unknown `/api/…` → JSON 404; `KB_*` env. Tests: gateway (injection, headers, off, 404, fail-open, stream, cache sig, Responses/Anthropic/GenAI, credits, vector stores CRUD/search/403/SSRF), admin (roles, cross-org, docs, search, kb_ids, audit, /api 404), e2e (upload → ready → search → grounded chat → embedding logs). Live: `DEMO=1 run-local.sh` KB with demo/embed → ready → search → chat/stream headers `X-GW-KB`, `X-GW-KB-Sources`; ui-smoke ok. vet + race tests green incl. Postgres and Redis.
- 2026-11-11 Outbound proxy (D22): Bifrost `ProxyConfig` per provider from own proxy > global env > none; sealed proxy password; tenant proxy gated + SSRF-checked at save and request time; chat-through-CONNECT-proxy test. gofmt, vet, race tests green incl. Postgres and Redis.
- 2026-11-11 Proxy pools (D23): `store.ProxyPool` (sealed member passwords), `/api/admin/proxy-pools` CRUD + test, provider `proxy_pool`, `UPSTREAM_PROXY_POOL`, `PROXY_HEALTH_INTERVAL`; loopback CONNECT forwarder (`internal/gateway/proxypool.go`) using Bifrost's `network.DialViaProxyTLS`; metrics `gw_proxy_dials_total`, `gw_proxy_member_up`; Platform → Proxies page and provider pool select. Live (`DEMO=1 run-local.sh`, pool = refused member + local CONNECT proxy on demo-mock): chats 200 via the working member, bogus member set aside then down (`gw_proxy_dials_total{result="fail"} 1, {result="ok"} 1`); working proxy killed → 502 fail-closed (`refused`), `proxy pool exhausted` logged; fail_closed=false → 200 direct (`direct`); ui-smoke ok. gofmt, vet, race tests green incl. Postgres and Redis.
- 2026-11-11 Rename to Polyrail (D24): module `github.com/polyrail/polyrail`, `cmd/polyrail`, `make dist` → `bin/polyrail-<os>-<arch>`, UI logo + favicon, docs. Wire identifiers (X-GW-*, cookies, metrics, env, KV, key-derivation labels) unchanged. gofmt/vet clean, `go test -race ./...` green with Postgres + Redis, `scripts/ui-smoke.py` ok on a DEMO run.
- 2026-11-11 Provider keys + alert channels backend (D25, docs/KEYS.md): `ProviderKey.disabled/quarantined/status_reason` (old data reads enabled; provider PUT keeps status); `GetKeysForProvider` drops disabled/quarantined/cooling keys (all out → enabled keys + `provider.unavailable`; all disabled → error → failover); Bifrost key Name = stored key id (`RoutingInfo.Key`); per-attempt outcomes from Bifrost's attempt trail → cooldown (Retry-After ≤ 5 min, else 30 s doubling), 3 auth failures → persisted quarantine + `key.quarantined`; hourly `keystat/` stats (30-day retention); key API for `/api/admin` and `/api/orgs/{org}` (masked list, bulk add ≤ 1000 with SHA-256 dedupe, patch, delete, bulk, direct-key test, stats); `store.AlertChannel` (sealed URL) + `/alerts` routes for both scopes; `internal/alert` on shoutrrr v0.23.0 services (10 services; smtp/generic behind `-tags alertsfull`: +16 MB binary from text/template), 10-min dedupe, bounded queue with org share, auto-disable after 10 failures; `gateway.Fanout` feeds webhooks + alerts. Stripped binary 48.2 → 48.9 MB, idle RSS 41.1 → 41.7 MB (anon +0.2 MB). gofmt, vet, `go test -race ./...` green incl. Postgres and Redis (+ `-tags alertsfull` alert tests).
- 2026-11-11 D25 integration QA: UI ↔ API reconciled (added `GET …/providers/{id}` the detail page needed; `alert_services` per scope in `GET /api/auth/config`, UI greys out smtp "needs a build with -tags alertsfull" / "platform channels only"; key list shows effective weight 1 for unset weights; clearing a cooldown keeps `last_used_at`). Shoutrrr URL builders proven against v0.23.0 config parsers (`internal/alert/urls_test.go`, smtp in `alert_full_test.go`): telegram, discord, slack webhook + bot, ntfy OK as built; Teams rewritten to `teams://?host=<workflow URL>` (O365 connector format is retired upstream), smtp uses `fromaddress`/`toaddresses`. Security: org channels can no longer redirect vendor services (telegram `apiurl`, googlechat host, teams non-Power-Automate host → 400); key test errors replace an echoed key value with its mask. Mock: keys `bad-*` → 401, `rl-*`/`slow-*` → 429 Retry-After 5. Live (DEMO=1, :8104): 3 keys bulk-added (1 duplicate, 1 invalid), 160 chats → good key served, `rl-` cooldown 5 s, `bad-` cooldown 30 s/60 s then quarantined; ntfy platform channel on a local listener received the test ping, `key.quarantined` and (after bulk clear) `key.recovered`; UI drawer chart, test toast, enable switch, bulk test/clear verified with Playwright; `scripts/ui-smoke.py` ok. gofmt, vet, race tests (Postgres + Redis), tsc green.
- 2026-11-11 OpenRouter request routing (D27, W1-B): `models` fallbacks (one failover chain, billed and named on the model used, `X-GW-Route` on completions/responses too), `provider` object (order/only/ignore/allow_fallbacks/require_parameters/sort/max_price/preferred_*/quantizations/data_collection/zdr; strict 400 `invalid_routing`), `:nitro`/`:floor`, org presets `@preset/<slug>` + `/api/orgs/{org}/presets` CRUD (audited), `usage.include` → `usage.cost`/`cost_micros`/`cached_tokens` (stream + non-stream), `user` → `end_user` on logs, `enduser/` rollups, `usage?group=end_user`, ClickHouse `end_user`, cache-affinity sticky key; `Provider.Privacy`. Tests: `internal/gateway/reqroute_test.go`, `internal/admin/presets_test.go`. gofmt, vet, `go test -race ./...` green incl. Postgres and Redis.
