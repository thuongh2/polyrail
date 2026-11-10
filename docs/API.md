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

`GET /api/public/models`: public catalog (no auth).

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
