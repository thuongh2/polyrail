# Product Requirements — agent-gateway

Owner: PO agent. Status: v1 scope. Every criterion below must map to an automated test (see docs/TESTING.md).

## Personas

- **Operator** — self-hosts the gateway (one VPS or a cluster), configures providers, models, prices.
- **Tenant admin** — manages one organization: members, API keys, private providers, budgets.
- **Developer (API consumer)** — calls the OpenAI-compatible API with an API key.
- **Visitor** — browses the public model catalog.

## Scope v1 (P0 unless marked)

### F1 OpenAI-compatible API
- AC1 `POST /v1/chat/completions` returns an OpenAI-shaped response for a catalog model.
- AC2 With `"stream": true` the response is `text/event-stream`, chunks are `data: {...}` and end with `data: [DONE]`.
- AC3 `POST /v1/embeddings` works for embedding models.
- AC4 `GET /v1/models` lists models visible to the caller's org (public + its private ones).
- AC5 Errors use the OpenAI error shape `{"error":{"message","type","code"}}` with correct HTTP status.

### F2 Providers (public and private)
- AC1 Operator adds a public provider (type, base URL, one or more keys with weights); usable by all orgs.
- AC2 Tenant admin adds a private provider; only that org's models can route to it.
- AC3 Any OpenAI-compatible endpoint (vLLM, Ollama, DeepSeek, Groq…) works via type `openai` + base URL.
- AC4 Provider keys are never returned by any API after creation and are encrypted at rest.

### F3 Model catalog and pricing
- AC1 A model has a public id (`vendor/name`), display name, context length, input/output price per 1M tokens.
- AC2 Cost of a request = prompt_tokens × in_price + completion_tokens × out_price (micro-USD precision).
- AC3 Public catalog page and `GET /api/public/models` require no auth and show price per 1M tokens.
- AC4 Org-private models are visible only to that org.

### F4 Routing, load balancing, failover
- AC1 A model has 1..n targets (provider + upstream model + weight); traffic splits by weight.
- AC2 If the chosen target fails with a retryable error (5xx, 429, timeout), the next targets are tried in order.
- AC3 A target that keeps failing is skipped for a cool-down period (circuit breaker).
- AC4 The response header `x-gateway-provider` names the provider that served the request.

### F5 Caching and cost control
- AC1 With header `x-gateway-cache: <ttl seconds>` an identical non-stream request is served from cache; header `x-gateway-cache-hit: true`; cost 0.
- AC2 Org `max_tokens_cap` caps `max_tokens`/`max_completion_tokens` sent upstream (token optimization).
- AC3 Org credit balance: requests are rejected with 402 when balance ≤ 0.
- AC4 Key monthly budget: requests rejected with 402 when the key's month spend ≥ budget.

### F6 API keys, rate limits, usage analytics
- AC1 Keys are shown once at creation (`sk-gw-…`), stored as SHA-256 hash, can be revoked (effective ≤ 30 s cluster-wide, immediate on the same node).
- AC2 Per-key RPM and TPM limits return 429 with `retry-after`.
- AC3 Optional per-key model allowlist and expiry.
- AC4 Usage analytics per org: requests, tokens in/out, cost, errors, avg latency, grouped by day and by model/key.
- AC5 Request log list (latest N, per org) with model, provider, tokens, cost, latency, status. Prompt bodies are not stored.

### F7 Multi-tenancy, RBAC, SSO, audit, guardrails
- AC1 Every resource belongs to an org; users act only inside orgs they are members of.
- AC2 Roles: owner > admin > member > viewer; operator (platform superadmin) above all. Matrix in docs/ARCHITECTURE.md.
- AC3 Email+password login (bcrypt) and OIDC SSO (any standard IdP) with optional auto-join org.
- AC4 Every admin mutation writes an audit entry (actor, action, target, time), viewable by org admins.
- AC5 Guardrails per org: max input characters, blocked regex patterns (400 `guardrail_blocked`), optional PII masking (emails, phone numbers) before sending upstream.

### F8 MCP and observability
- AC1 Operator registers MCP servers (HTTP/SSE); their tools are available to chat requests via Bifrost core; header `x-gateway-mcp-clients` filters which.
- AC2 `GET /metrics` Prometheus format: requests by model/provider/status, latency histogram, cache hits, queue drops.
- AC3 Structured JSON logs; `GET /healthz` (liveness) and `GET /readyz` (store reachable).

### F9 Deployment
- AC1 Single static binary + Dockerfile + docker-compose (small mode) and compose for scale mode (Postgres + Valkey + ClickHouse).
- AC2 Small mode idle RSS < 100 MB and runs on 1 vCPU / 2 GB.
- AC3 First boot creates the operator account from `ADMIN_EMAIL`/`ADMIN_PASSWORD`.

## Deferred (named, not dropped)
- Semantic (embedding-similarity) cache — exact-match cache only in v1.
- Payment gateway integration (Stripe) — credits are granted by the operator via API/UI in v1.
- SAML SSO — OIDC only in v1.
- Responses API, audio, images, batch endpoints — Bifrost supports them; HTTP routes not exposed in v1.
