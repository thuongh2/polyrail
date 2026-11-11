# OmniRoute parity

Reference: github.com/cheaper-inference/OmniRoute (MIT, Next.js + SQLite, v3.8.5x), read 2026-11-11.
We port the gateway ideas that fit a 1 vCPU / 2 GB Go binary on Bifrost core, and skip anything
that depends on reusing consumer subscription logins, pooling free accounts, or client
impersonation (provider-terms risk; OmniRoute's own stealth guide calls them ban risks).

Status: **have** = already in Polyrail, **wave 1** = in progress / shipped this wave,
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

## Proxies (D23)

| OmniRoute | Status | Notes |
|---|---|---|
| proxy registry, pools with ordered members (http/https/socks5, auth, CA) | have | `/api/admin/proxy-pools`, Platform → Proxies; passwords sealed, write-only |
| pool at global / provider scope | have | `UPSTREAM_PROXY_POOL`; provider `proxy_pool` (platform providers); tenants only get the global default |
| strategies round-robin, random, sticky (window, default 30 min), latency | have | per new upstream connection; round-robin cursor in memory per node (OmniRoute persists it) |
| only alive members are candidates | have | enabled, not down, not set aside |
| set aside with exponential cooldown 60 s → 600 s | have | on proxy dial/handshake failure (unreachable, refused, timeout) |
| auto-disable after 3 consecutive failures, a good probe re-activates | have | in memory per node ("down"), not persisted |
| background health probe (10 min), TCP fast-fail 2 s | have | `PROXY_HEALTH_INTERVAL`; probe connects only; per-dial bound 5 s, ≤ 3 members per tunnel |
| fail closed when the pool is exhausted, configurable | have | `fail_closed` (default true); open = direct dial (tenants still public-only) |
| test a pool | have | `POST /api/admin/proxy-pools/{id}/test`: CONNECT handshake per member, latency |
| webhooks `proxy.set_aside`, `proxy.pool.exhausted` | partial | logs (`proxy member set aside`, `proxy pool exhausted` at most every 300 s) + metrics `gw_proxy_dials_total`, `gw_proxy_member_up`; our webhooks are per org, these are platform events |
| rotate exit on provider 429, per-IP quota set-aside | skip | provider rate-limit evasion; a 429 never changes the selection |
| spread accounts across egress IPs, observe accounts per exit | skip | same (multi-account evasion) |
| free public proxy lists, proxy subscriptions / sing-box, throwaway edge relays | skip | untrusted egress for API keys and prompts; terms risk |
| stealth fingerprinting | skip | provider-terms risk |
