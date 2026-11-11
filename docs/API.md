# API

## Gateway (API key)

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/chat/completions` | OpenAI request body. `model` is a catalog id (`vendor/name`). `stream: true` returns SSE ending with `data: [DONE]`; the last chunk carries `usage`. Response adds `provider`. |
| POST | `/v1/embeddings` | OpenAI request body. |
| GET | `/v1/models` | Models visible to the key, with `pricing.input`/`pricing.output` (USD per 1M tokens) and `context_length`. |
| POST | `/v1/mcp/tool/execute` | Body: one `tool_calls[]` item returned by the model. Returns the tool message to append. |

Errors: `{"error": {"message", "type", "code"}}`. Codes include `invalid_api_key` (401),
`expired_api_key` (401), `model_not_found` (404), `model_not_allowed` (403), `wrong_model_kind` (400),
`guardrail_blocked` (400), `insufficient_credits` (402), `key_budget_exceeded` (402),
`rpm_exceeded` / `tpm_exceeded` (429), `body_too_large` (413), `provider_error` (upstream status or 502).

## Public

No auth. Only public (platform) models and providers appear; provider ids, keys and base URLs are never returned.

| Method | Path | Notes |
|---|---|---|
| GET | `/api/public/models` | Public catalog: `{"data": [ModelInfo]}`. ModelInfo adds `max_output_tokens` and `providers` (provider names). `Cache-Control: public, max-age=60`. |
| GET | `/api/public/models/{vendor/name}` | One public, enabled model (else 404): `{"model": ModelInfo, "stats_24h": Stats, "providers": [{"name","type","upstream_model","weight","status","stats_24h": Stats, "hourly": [{"hour" (RFC3339 UTC),"requests","errors","avg_latency_ms"}]}]}`. `hourly` is the last 72 hours, oldest first, zero hours included. `Cache-Control: public, max-age=30`. |
| GET | `/api/public/status` | `{"providers": [{"name","type","status","models","stats_24h": Stats}], "updated_at"}`: every public provider, sorted by name, over its public models. `Cache-Control: public, max-age=30`. |

Stats (last 24 h of provider attempts): `{"requests","errors","uptime" (0..1, null without traffic),
"avg_latency_ms" (over successful attempts),"throughput_tps" (output tokens / successful latency seconds, null without latency)}`.
Status: `no_data` (no attempts in 24 h), `down` (circuit breaker open on this node for the target, or for a
provider every one of its public targets; or uptime < 0.9), `degraded` (uptime < 0.99), else `operational`.
Attempts are recorded by the gateway per target: targets it failed over from (or all, when the request
failed) count as errors with latency 0; the serving target counts the request latency and output tokens.
Hourly counters are kept 30 days.

## Admin (session cookie)

Sign in with `POST /api/auth/login {email, password}`. Every non-GET request needs the header
`X-GW-CSRF: 1`. `{org}` is an org id; operators may act on any org.

| Method | Path | Minimum role |
|---|---|---|
| GET | `/api/auth/me`, `/api/auth/config` | signed in / none |
| POST | `/api/auth/logout`, `/api/auth/password`, `/api/auth/signup` | – |
| GET | `/api/auth/oidc/login` | – (SSO) |
| GET/POST | `/api/orgs` | list: member / create: operator |
| GET/PATCH/DELETE | `/api/orgs/{org}` | viewer / admin (operator for `prepaid`, `mcp_servers`) / owner |
| POST | `/api/orgs/{org}/credits {usd, note}` | operator |
| GET/POST | `/api/orgs/{org}/members` | viewer / admin |
| PUT/DELETE | `/api/orgs/{org}/members/{user}` | admin (owners only change owners) |
| GET/POST | `/api/orgs/{org}/keys` | viewer / member (secret returned once) |
| PATCH | `/api/orgs/{org}/keys/{id}` | admin, or the member who created it (rename/revoke only) |
| GET/POST/PUT/DELETE | `/api/orgs/{org}/providers[/{id}]` | admin |
| GET | `/api/orgs/{org}/models` | viewer |
| POST/PUT/DELETE | `/api/orgs/{org}/models[/{vendor/name}]` | admin |
| GET | `/api/orgs/{org}/usage?days=30` | viewer |
| GET | `/api/orgs/{org}/logs?limit=100` | viewer |
| GET | `/api/orgs/{org}/audit` | admin |
| * | `/api/admin/providers`, `/api/admin/models`, `/api/admin/usage`, `/api/admin/audit`, `/api/admin/users`, `/api/admin/mcp` | operator |

Prices in model bodies are `input_price_micros` / `output_price_micros`: micro-USD per 1M tokens
(`$0.15` → `150000`).

## Ops

`GET /healthz` (process up), `GET /readyz` (store reachable), `GET /metrics` (Prometheus;
`METRICS_TOKEN` if set).
