# CLAUDE.md — project memory (read this first, every session)

This file is the persistent memory of the autonomous engineering team. Keep it short and true.
Update the **Decisions** and **Status** sections whenever something changes. `AGENTS.md` points here.

## Mission (verbatim intent from the owner)

Product name: **Polyrail** — "One API for every model. Self-hosted, light, open." (renamed from
agent-gateway on 2026-11-11, D24; compatibility identifiers such as `X-GW-*` headers stay).

Build a production-ready, fully open-source **Unified AI Model Platform** — an OSS alternative to
OpenRouter with Bifrost-inspired capabilities — that is **lightweight and easy to self-host**.

- Target: runs well on **1 vCPU / 2 GB RAM VPS**, single binary; scales horizontally when needed.
- **Must scale to millions of users** while staying light (owner requirement, 2026-11-10).
- Language: **Go**. Use the open-source **Bifrost core** (`github.com/maximhq/bifrost/core`) as the
  provider engine — never hand-write provider adapters (ponytail rule: reuse before writing).
- Enterprise features in OSS: multi-tenancy, RBAC, SSO, audit logs, guardrails.
- Never stop at planning: ship a functional, tested, documented, deployable product.

## Required feature list (do not drop any)

1. Unified OpenAI-compatible API (`/v1/chat/completions` stream + non-stream, `/v1/embeddings`, `/v1/models`).
2. Public and private model providers (provider keys per org = private; platform keys = public).
3. Custom model pricing and flexible monetization (per-model in/out price, markup, org credits).
4. Intelligent routing, load balancing (weighted targets), automatic failover (fallback chain).
5. Token optimization, caching (response cache), cost management (budgets).
6. API keys, rate limits (RPM/TPM), quotas/budgets, usage analytics.
7. Multi-tenancy (orgs), RBAC (owner/admin/member/viewer), SSO (OIDC), audit logs, guardrails.
8. MCP integration and observability (Prometheus metrics, structured logs, request logs).
9. Easy provider extensions and custom integrations (OpenAI-compatible custom base URL, config-only).
10. Public models catalog page (like openrouter.ai/models): name, provider, context, price in/out per 1M.

## Engineering principles (from the references — binding)

- **Ponytail** (github.com/dietrichgebert/ponytail): smallest complete change. Ladder: does it need
  to exist → already in codebase → stdlib → installed dependency → one line → minimum code.
  No speculative abstractions/options. Never cut validation, error handling, security, a11y.
  Non-trivial logic leaves one small test. Shortcuts get `// shortcut: <limit>, <when to upgrade>`.
- **Superpowers** (github.com/obra/superpowers): spec → plan → TDD (red/green) → review →
  **verification before completion** (no claim without fresh command output).
  Decide ordinary things yourself; record rulings in `docs/DECISIONS.md`.
- **Hallmark** (github.com/Nutlope/hallmark) for the web UI: tokens on `:root`, no `transition: all`,
  `:focus-visible` on all controls, `prefers-reduced-motion`, no invented metrics/testimonials,
  no eyebrows / tracked caps labels, no purple-blue gradients, no emoji icons, no card-in-card.

## Team & workflow

Agents live in `.claude/agents/`: `po`, `tech-lead`, `developer`, `qa`, `security`, `slop-reviewer`.
Flow per feature: **PO** (requirement + acceptance criteria in `docs/PRD.md`) → **Tech Lead**
(design/ADR in `docs/ARCHITECTURE.md`, `docs/DECISIONS.md`) → **Developer** (TDD, code) →
**QA** (tests, e2e against mock provider) → **Security** + **Slop reviewer** (review) → fix → done.
A feature is *done* only when: `make check` passes (vet + tests + race), docs updated,
`docs/PROGRESS.md` updated with evidence.

## Decisions (ADR summary — details in docs/DECISIONS.md)

- D1 Bifrost core pinned at **v1.15.2** on **Go 1.27** (built from the official go1.27.3 tag in the
  sandbox with the owner's approval). Bifrost core is the SDK for every model call.
- D2 Sandbox network only reaches GitHub. `go.mod` has `replace` lines mapping golang.org/x/*,
  gopkg.in/*, cloud.google.com/* to their GitHub mirrors (same code). Build with
  `GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod`. Harmless for users; can be removed later.
- D3 Storage split by access pattern (owner rejected plain SQL-only):
  - Small mode: **Pebble** (pure-Go LSM, RocksDB design, no CGO) for config + usage + rollups.
  - Scale mode: Postgres (config), Valkey/Redis (shared counters), ClickHouse (usage analytics).
  - Seams: `store.KV` (Pebble | Postgres, all entities written once on it), `limits.Limiter`
    (local | Redis), `usage.LogSink` (store | ClickHouse). Hot path never waits on a DB.
  - Usage writes async + batched; rollups via counters; logs partitioned by day for cheap TTL.
  - Credits/budgets: stored spend + this node's unflushed spend, refreshed each 1 s flush (D7).
- D4 HTTP: stdlib `net/http` (Go 1.22 patterns). UI: plain HTML/CSS/JS embedded via `go:embed`, no build step.
- D5 Provider secrets encrypted at rest with AES-GCM using `MASTER_KEY`. API keys stored as SHA-256 hash only.
- D6–D35 in docs/DECISIONS.md (KV seam, delta-flush budgets, Prometheus, MCP grants, same-owner routing, rounding, UI, cloud providers, routing strategies, guardrails, idempotency + cost header, webhooks, semantic cache, models.dev catalog seed + visibility rule, org OpenAI-compatible types, knowledge bases / gateway RAG, upstream proxy, proxy pools, rename to Polyrail, provider key status + alert channels, policy engine, request routing + presets, key identity/exports/content logging, enterprise identity: domains/SAML/SCIM, data regions + OTLP trace export, workspaces, prompt cache pricing, Playground Markdown renderer, budgets count BYOK by default, OpenCode session header).

## Lessons (do not repeat)

- Never return persisted secrets from JSON: hide with `json:"-"` and persist through a stored* wrapper.
- Every admin mutation that affects keys/orgs must call `G.InvalidateAuth()`.
- Tenant-controlled URLs: validate at save time AND at request time; never echo upstream bodies to tenants.
- Tenant IdPs (SAML/SCIM) may only vouch for emails on domains their org verified, never for operators (D29).
- Docs must describe what the code does; the slop reviewer checks this.
- No docker daemon in the sandbox: Dockerfile/compose are written but not built here.
- UI agents working in parallel must not run `vite build` (shared dist) — verify with tsc + dev server.
- UI changes: rebuild with `make ui` and commit `internal/web/dist`; follow docs/UI.md.

## Environment notes (sandbox)

- Cloud workspace: `/home/claude/agent-gateway` (git repo). Owner's folder (Mac):
  `/Users/lap17656/code-by-hand/agent-gateway` — sync deliverables there with device_commit_files.
  Both folders keep the old name after the rename to Polyrail (D24); they are paths, not the product name.
- Build: `make build` → `bin/polyrail`; `make dist` → `bin/polyrail-<os>-<arch>` + `bin/mockprovider-<os>-<arch>`.
- Owner's local VM has no Go and no internet beyond allowlist; build/test in the cloud workspace.
- Go 1.27.3 (built from source, owner-approved) lives at `/opt/gosrc/go127/bin`; always
  `export PATH=/opt/gosrc/go127/bin:$PATH GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod GOTOOLCHAIN=local`.
  Old Go 1.24.7 binaries on PATH cannot build this module. Custom module-proxy tools stay off-limits.

## Status

v1 functional: gateway, admin API, OpenRouter-style UI (shadcn; catalog, model pages with provider
health, status page, docs, console, platform admin), Pebble + Postgres stores, Redis limiter,
ClickHouse sink, SSO, MCP, metrics. Security review fixed (docs/SECURITY.md), slop review fixed.
Live board and evidence: `docs/PROGRESS.md`. Next candidates: docs/PROGRESS.md "Next".
