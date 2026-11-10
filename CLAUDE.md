# CLAUDE.md — project memory (read this first, every session)

This file is the persistent memory of the autonomous engineering team. Keep it short and true.
Update the **Decisions** and **Status** sections whenever something changes. `AGENTS.md` points here.

## Mission (verbatim intent from the owner)

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

- D1 Bifrost core pinned at **v1.2.38** (last release on Go ≤1.24; sandbox only has Go 1.24.7 and
  can't download newer toolchains). Upgrade path: bump Go in Dockerfile + core version.
- D2 Sandbox network only reaches GitHub. `go.mod` has `replace` lines mapping golang.org/x/*,
  gopkg.in/*, cloud.google.com/* to their GitHub mirrors (same code). Build with
  `GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod`. Harmless for users; can be removed later.
- D3 Storage split by access pattern (owner rejected plain SQL-only):
  - Small mode: **Pebble** (pure-Go LSM, RocksDB design, no CGO) for config + usage + rollups.
  - Scale mode: Postgres (config), Valkey/Redis (shared counters), ClickHouse (usage analytics).
  - Behind 3 interfaces: `ConfigStore`, `Counters`, `UsageSink`. Hot path never waits on a DB.
  - Usage writes async + batched; rollups via counters; raw events partitioned by day for cheap TTL.
  - Credits use local debits with periodic flush (small mode) / leases (scale mode).
- D4 HTTP: stdlib `net/http` (Go 1.22 patterns). UI: plain HTML/CSS/JS embedded via `go:embed`, no build step.
- D5 Provider secrets encrypted at rest with AES-GCM using `MASTER_KEY`. API keys stored as SHA-256 hash only.

## Environment notes (sandbox)

- Cloud workspace: `/home/claude/agent-gateway` (git repo). Owner's folder (Mac):
  `/Users/lap17656/code-by-hand/agent-gateway` — sync deliverables there with device_commit_files.
- Owner's local VM has no Go and no internet beyond allowlist; build/test in the cloud workspace.
- Building Go from source / custom module-proxy tools was refused by the permission classifier — don't retry.

## Status

See `docs/PROGRESS.md` for the live task board and verification evidence.
