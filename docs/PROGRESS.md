# Progress board

Legend: [ ] todo · [~] in progress · [x] done (with evidence)

## Milestone 1 — small mode end to end
- [ ] T1 store: KV interface + Pebble backend + entity layer (orgs, users, members, keys, providers, models, audit) + tests
- [ ] T2 gateway: Bifrost account adapter, catalog snapshot, router (weights, fallbacks, circuit breaker)
- [ ] T3 gateway: /v1/chat/completions (+stream), /v1/embeddings, /v1/models, errors
- [ ] T4 limits (RPM/TPM), budgets, guardrails, response cache
- [ ] T5 usage pipeline: batcher, rollups, request logs, retention
- [ ] T6 admin API: auth (password + sessions), RBAC, CRUD, audit, analytics
- [ ] T7 OIDC SSO
- [ ] T8 MCP servers via Bifrost
- [ ] T9 metrics, health, structured logs
- [ ] T10 web UI (catalog, login, dashboard, keys, models, providers, members, audit, playground)
- [ ] T11 e2e test with mock provider; resource smoke
- [ ] T12 Dockerfile, compose, README, deploy docs

## Milestone 2 — scale mode
- [ ] T13 Postgres KV backend (+ same KV conformance tests)
- [ ] T14 Redis limiter
- [ ] T15 ClickHouse raw event sink
- [ ] T16 compose for scale mode

## Reviews
- [ ] Security review (security agent) — findings in docs/SECURITY.md
- [ ] Anti-slop review (slop-reviewer agent)
- [ ] QA acceptance pass (qa agent) — docs/TESTING.md

## Log
- 2026-11-10 Team, memory (CLAUDE.md), PRD, architecture, decisions written. Deps resolve; Bifrost core v1.2.38 builds.
