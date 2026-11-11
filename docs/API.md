# API

## Gateway (API key)

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/chat/completions` | OpenAI request body. `model` is a catalog id (`vendor/name`). `stream: true` returns SSE ending with `data: [DONE]`; the last chunk carries `usage`. Response adds `provider`. |
| POST | `/v1/embeddings` | OpenAI request body. |
| GET | `/v1/models` | Models visible to the key, with `pricing.input`/`pricing.output` (USD per 1M tokens) and `context_length`. |
| POST | `/v1/completions` | Legacy text completions (chat-kind models), `stream` supported. |
| POST | `/v1/responses` | OpenAI Responses API (chat-kind models). Streams named SSE events ending with `response.completed`. |
| POST | `/v1/audio/speech` | Text to speech (speech models): `{model, input, voice, response_format, speed}` → audio bytes. Priced per input token, or per 1M characters when the provider reports no tokens. |
| POST | `/v1/audio/transcriptions` | Speech to text (transcription models): multipart `file`, `model`, `language`, `prompt`, `response_format` (json or text). Priced by `audio_minute_price_micros` when set, else by tokens. |
| POST | `/v1/mcp/tool/execute` | Body: one `tool_calls[]` item returned by the model. Returns the tool message to append. The tool's server must be granted to the org and allowed by the key's `mcp_servers` (else 403 `mcp_tool_not_allowed`). |
| POST | `/anthropic/v1/messages` | Anthropic Messages API for any chat-kind model (`model` is a catalog id; `model`, `max_tokens`, `messages` required). Converted with Bifrost core to the Responses pipeline, so routing, limits, guardrails, pricing and logs are the same as `/v1/responses`. `stream: true` returns Anthropic SSE (`event: message_start` … `event: message_stop`, no `[DONE]`). `anthropic-version` / `anthropic-beta` headers are accepted and ignored. |
| GET | `/anthropic/v1/models` | Chat models visible to the key: `{"data": [{"id","type":"model","display_name","created_at"}], "first_id","last_id","has_more": false}`. |
| * | `/openai/v1/...` | Alias of `/v1/...` for every endpoint above (OpenAI SDK `base_url` ending in `/openai/v1`). |

Anthropic SDK: `Anthropic(base_url="https://gw.example.com/anthropic", api_key="sk-gw-…")`.

```
POST /anthropic/v1/messages
x-api-key: sk-gw-…
{"model":"anthropic/claude-sonnet","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}

200 {"id":"msg_…","type":"message","role":"assistant","model":"anthropic/claude-sonnet",
     "content":[{"type":"text","text":"Hello!"}],"stop_reason":"end_turn",
     "usage":{"input_tokens":8,"output_tokens":3}}
```

Errors: `{"error": {"message", "type", "code"}}`. Codes include `invalid_api_key` (401),
`expired_api_key` (401), `model_not_found` (404), `model_not_allowed` (403), `wrong_model_kind` (400),
`guardrail_blocked` (400), `insufficient_credits` (402), `key_budget_exceeded` (402),
`rpm_exceeded` / `tpm_exceeded` (429), `body_too_large` (413), `provider_error` (upstream status or 502).
On `/anthropic/v1/*` errors use the Anthropic shape with the same status:
`{"type":"error","error":{"type","message"}}`, type by status: 400 `invalid_request_error`,
401 `authentication_error`, 402 `billing_error`, 403 `permission_error`, 404 `not_found_error`,
413 `request_too_large`, 429 `rate_limit_error`, other `api_error`. Stream errors are `event: error`.

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

Key bodies: `name`, `rpm`, `tpm`, `monthly_budget_usd`, `models` (allowlist), `mcp_servers` (MCP server
names the key may use, intersected with the org grant; empty or absent = every granted server),
`expires_in_days`, `revoked`. On PATCH only admins may change limits, `models` or `mcp_servers`.

| GET/POST/PUT/DELETE | `/api/orgs/{org}/providers[/{id}]` | admin |
| GET | `/api/orgs/{org}/models` | viewer |
| POST/PUT/DELETE | `/api/orgs/{org}/models[/{vendor/name}]` | admin |
| GET | `/api/orgs/{org}/usage?days=30` | viewer |
| GET | `/api/orgs/{org}/logs?limit=100` | viewer |
| GET | `/api/orgs/{org}/audit` | admin |
| * | `/api/admin/providers`, `/api/admin/models`, `/api/admin/usage`, `/api/admin/audit`, `/api/admin/users`, `/api/admin/mcp` | operator |

Provider bodies: `name`, `type`, `base_url`, `timeout_seconds`, `enabled`, `keys: [{id, name, value,
weight, models}]`. `value` and every secret below are write-only: encrypted at rest, never returned;
sending a key `id` with a secret left empty keeps the stored one, except when `base_url`, `type` or an
Azure `endpoint` changes (re-enter secrets then). Types `azure`, `bedrock`, `vertex` take per-key
config and no `base_url`; `elevenlabs` takes `value` and an optional `base_url`.

- `azure` (platform only): `value` (API key) and `azure: {endpoint, api_version, deployments}`.
- `bedrock` (platform and org): `bedrock: {region, access_key*, secret_key*, session_token*, arn,
  deployments}`; or a Bedrock API key in `value`. No credentials = the gateway host's AWS identity
  (IAM role, environment), operators only: org providers must supply keys.
- `vertex` (platform only): `vertex: {project_id, project_number, region, auth_credentials*,
  deployments}`; `auth_credentials` is service-account JSON, empty = host Google credentials.

`*` = secret. `deployments` maps model name → deployment / inference profile; when set, only those
models are served by that key. `GET /api/auth/config` lists the types per scope in
`provider_types.platform` / `provider_types.org`.

```json
{"name":"azure-eu","type":"azure","enabled":true,"keys":[{"value":"<azure key>","azure":{
  "endpoint":"https://my-res.openai.azure.com","api_version":"2024-10-21","deployments":{"gpt-4o":"prod-4o"}}}]}
{"name":"bedrock-us","type":"bedrock","enabled":true,"keys":[{"bedrock":{"region":"us-east-1",
  "access_key":"AKIA...","secret_key":"...","deployments":{"claude-sonnet":"us.anthropic.claude-3-5-sonnet-20241022-v2:0"}}}]}
{"name":"vertex","type":"vertex","enabled":true,"keys":[{"vertex":{"project_id":"my-proj","region":"us-central1",
  "auth_credentials":"{\"type\":\"service_account\",...}"}}]}
```

Prices in model bodies are `input_price_micros` / `output_price_micros`: micro-USD per 1M tokens
(`$0.15` → `150000`).

## Ops

`GET /healthz` and its Bifrost-compatible alias `GET /health` (process up), `GET /readyz` (store
reachable), `GET /metrics` (Prometheus; `METRICS_TOKEN` if set), `GET /api/version`
(`{"version": "<build version>", "bifrost_core": "v1.2.38"}`; the version is set at build time with
`-ldflags "-X main.version=…"`, which `make build` does from `git describe`, else `dev`).
