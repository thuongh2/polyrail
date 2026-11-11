# OmniRoute parity

Reference: github.com/cheaper-inference/OmniRoute (MIT, Next.js + SQLite, v3.8.5x), read 2026-11-11.
We port the gateway ideas that fit a 1 vCPU / 2 GB Go binary on Bifrost core, and skip anything
that depends on reusing consumer subscription logins, pooling free accounts, or client
impersonation (provider-terms risk; OmniRoute's own stealth guide calls them ban risks).

Status: **have** = already in agent-gateway, **wave 1** = in progress / shipped this wave,
**later** = fits, not scheduled, **skip** = out of scope (reason given).

## Routing

| OmniRoute | Status | Notes |
|---|---|---|
| weighted + failover chain | have | `plan()`; Bifrost `Fallbacks` |
| priority / fill-first | have | declared order |
| round-robin | have | per-model counter |
| p2c (power of two choices) | have | success rate + latency score |
| least-used (in-flight) | have | per-target in-flight counter |
| latency / SLA-aware | have | EWMA latency + error penalty |
| cost-optimized | have | per-target price override, else model price |
| cache-affinity (prompt-cache prefix) | have | rendezvous hash of `prompt_cache_key` or prefix hash |
| random / strict-random | have | |
| lkgp (last known good) | later | sticky is close to cache-affinity |
| auto-combo 16-factor engine | later | subset covered by latency/p2c |
| fusion (panel + judge), pipeline | later | M each, high token cost |
| quota / reset / headroom strategies | skip | need per-account plan windows (subscription accounts) |
| context-relay | skip | same |
| 3-layer resilience (breaker 5xx only, per-key `Retry-After` cooldown) | have | breaker counts 5xx/408/429/401/403/network only; 429 `Retry-After` opens the target (per target, not per key) |
| routing decision header | have | `X-GW-Route` |
| first-output timeout, pre-content stream retry | later | |

## Request handling

| OmniRoute | Status | Notes |
|---|---|---|
| exact response cache | have | |
| semantic cache | have | org's embedding model, in-memory cosine, temperature 0 only (D18) |
| idempotency keys | have | `Idempotency-Key`, per API key, non-stream |
| cost response headers | have | `X-GW-Cost-Micros` |
| model discovery `POST /v1/models/query` | have | |
| guardrails: PII, regex, max chars | have | |
| guardrails: credential masker, prompt-injection detector | have | `mask_secrets`; `prompt_injection` warn / block, `X-GW-Guardrail` (D15) |
| token compression (lossless: whitespace, duplicate system prompts) | have | `compression: lossless`, `X-GW-Compressed-Chars` (D15) |
| compression "Caveman"/lossy, LLMLingua | skip | changes meaning; heavy model |
| RTK tool-output filters | later | |
| `/v1/moderations` | later | Bifrost core has no moderation request type |
| `/v1/search` | later | |
| model aliases / wildcard | later | |
| modality bridges | later | |

## Platform

| OmniRoute | Status | Notes |
|---|---|---|
| webhooks (HMAC, retries, auto-disable) | have | per org, `internal/webhook` (D17) |
| budgets, cost tracking | have | ours: org credits + per-key budgets |
| multi-user, RBAC, SSO, audit | have | ours is multi-tenant; OmniRoute is single-tenant |
| Prometheus metrics | have | OmniRoute has none |
| MCP server (admin tools) | later | we have the MCP client |
| A2A, memory, skills, evals | later | low enterprise value or sandboxing risk |
| pricing sync (models.dev / LiteLLM) | later | |
| Electron app, 67-language i18n, Cloudflare tunnel | skip | not part of a server gateway |
| subscription OAuth providers, web-cookie providers, TLS/CLI fingerprinting, MITM, free-account pools | skip | provider-terms risk |
