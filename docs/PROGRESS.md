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
GOMAXPROCS=1 MASTER_KEY=… ADMIN_EMAIL=… ADMIN_PASSWORD=… ./bin/gateway &
# console: public provider type openai, base URL http://127.0.0.1:9901; model mock/fast; create a key
go run ./tools/loadtest http://127.0.0.1:8088/v1/chat/completions sk-gw-…   # 5000 req, concurrency 50
```

Result: idle RSS 30 MB; peak RSS 55 MB; `requests=5000 errors=0 rps=3645 p50=10.9ms p95=25.2ms
p99=55.0ms`; the usage endpoint then reported 5000 requests. Same load straight to the mock:
`rps=11281 p50=2.9ms`. The load generator and mock shared the machine with the gateway.

## Next (not started)
- OIDC test against a mock IdP; live MCP server test.
- Semantic cache; Responses API, images, audio routes (Bifrost supports them).
- Payment integration for credits (Stripe), email verification for sign-up.
- Dial-time SSRF enforcement inside Bifrost's HTTP client (needs a Bifrost hook).
- Upgrade to current Bifrost core once a Go 1.27 toolchain is available (D1).

## Log
- 2026-11-10 Team, memory, PRD, architecture written. Bifrost core v1.2.38 builds on Go 1.24.
- 2026-11-10 M1 + M2 built; QA found 3 bugs, security found 4 high / 8 medium; all fixed with regression tests.
- 2026-11-10 Slop review fixes; resource smoke recorded.
