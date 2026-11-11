# API

## Gateway (API key)

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/chat/completions` | OpenAI request body. `model` is a catalog id (`vendor/name`). `stream: true` returns SSE ending with `data: [DONE]`; the last chunk carries `usage`. Response adds `provider`. |
| POST | `/v1/embeddings` | OpenAI request body. |
| GET | `/v1/models` | Models visible to the key, with `pricing.input`/`pricing.output` (USD per 1M tokens), `pricing.image` (USD per image) and `pricing.request` (USD per request) when set, and `context_length`. |
| POST | `/v1/completions` | Legacy text completions (chat-kind models), `stream` supported. |
| POST | `/v1/responses` | OpenAI Responses API (chat-kind models). Streams named SSE events ending with `response.completed`. |
| POST | `/v1/audio/speech` | Text to speech (speech models): `{model, input, voice, response_format, speed}` → audio bytes. Priced per input token, or per 1M characters when the provider reports no tokens. |
| POST | `/v1/audio/transcriptions` | Speech to text (transcription models): multipart `file`, `model`, `language`, `prompt`, `response_format` (json or text). Priced by `audio_minute_price_micros` when set, else by tokens. |
| POST | `/v1/images/generations` | Image models: `{model, prompt, n, size, quality, response_format, background, output_format, negative_prompt, seed, user}` → `{"created","data":[{"b64_json"\|"url","revised_prompt"}],"usage"}` as the provider returns it. Guardrails apply to `prompt`. Priced `image_price_micros` × `len(data)` plus tokens when reported. `stream` is rejected (400). |
| POST | `/v1/images/edits` | Image models, multipart: `model`, `prompt`, `image` (or repeated `image[]`), optional `mask`, `n`, `size`, `quality`, `response_format`, `background`, `input_fidelity`, `output_format`, `user`. Same response and pricing as generations. |
| POST | `/v1/images/variations` | Image models, multipart: `model`, `image`, optional `n`, `size`, `response_format`, `user`. Same response and pricing. |
| POST | `/v1/rerank` | Rerank models, Cohere/Jina body `{model, query, documents: [string \| {text}], top_n, return_documents, max_tokens_per_doc}` → `{"id","model","results":[{"index","relevance_score","document"?}],"usage"?}` sorted by score. Guardrails apply to query and documents. Priced `request_price_micros` per request plus tokens when reported. |
| POST | `/v1/ocr` | OCR models (Bifrost serves OCR through Mistral-type providers): `{model, document: {type: "document_url", document_url} \| {type: "image_url", image_url}, pages, include_image_base64, table_format, document_annotation_prompt, …}` → `{"model","pages":[{"index","markdown","images","dimensions"}],"usage_info"}`. Priced `request_price_micros` per request. |
| POST | `/v1/responses/input_tokens` | Count input tokens for a chat-kind model with a Responses body → `{"object":"response.input_tokens","input_tokens"}`. Admitted and rate limited (RPM) like other calls, guardrails apply, logged at cost 0. |
| POST | `/v1/mcp/tool/execute` | Body: one `tool_calls[]` item returned by the model (tool names are `<server>-<tool>`; a bare tool name is accepted too). Returns the tool message to append. The tool's server must be granted to the org and allowed by the key's `mcp_servers` (else 403 `mcp_tool_not_allowed`). |
| POST | `/anthropic/v1/messages` | Anthropic Messages API for any chat-kind model (`model` is a catalog id; `model`, `max_tokens`, `messages` required). Converted with Bifrost core to the Responses pipeline, so routing, limits, guardrails, pricing and logs are the same as `/v1/responses`. `stream: true` returns Anthropic SSE (`event: message_start` … `event: message_stop`, no `[DONE]`). `anthropic-version` / `anthropic-beta` headers are accepted and ignored. |
| POST | `/anthropic/v1/messages/count_tokens` | Anthropic count tokens (`model`, `messages`, optional `system`/`tools`; no `max_tokens`) → `{"input_tokens"}`. Same rules as `/v1/responses/input_tokens`. |
| GET | `/anthropic/v1/models` | Chat models visible to the key: `{"data": [{"id","type":"model","display_name","created_at"}], "first_id","last_id","has_more": false}`. |
| POST | `/genai/v1beta/models/{model}:generateContent` | Google GenAI (Gemini) SDK drop-in for chat-kind models. `{model}` is a catalog id: send `vendor/name` as is (the SDKs put it in the path unescaped), `vendor%2Fname`, or `models/vendor/name`. Body is a Gemini `GenerateContentRequest` (`contents`, `systemInstruction`, `generationConfig`, `tools`…), converted with Bifrost core to the Responses pipeline: same routing, limits, guardrails (text parts), pricing and logs as `/v1/responses`. Reply is a `GenerateContentResponse` with `modelVersion` = the catalog id. Key in `x-goog-api-key`, `?key=`, `Authorization: Bearer` or `x-api-key`. |
| POST | `/genai/v1beta/models/{model}:streamGenerateContent?alt=sse` | Same, as SSE `data: {GenerateContentResponse}` frames (no `[DONE]`, no event names). Without `alt=sse` → 400 (the Gemini SDKs always send it). |
| POST | `/genai/v1beta/models/{model}:countTokens` | Gemini count tokens (`contents` or `generateContentRequest`) → `{"totalTokens"}`, counted by the serving provider. Logged at cost 0. |
| POST | `/genai/v1beta/models/{model}:embedContent`, `:batchEmbedContents` | Embedding-kind models: `{content}` → `{"embedding":{"values"}}`; `{requests:[{content}]}` → `{"embeddings":[{"values"}]}`. |
| GET | `/genai/v1beta/models[/{model}]` | Chat and embedding models visible to the key in the Gemini format: `{"models":[{"name":"models/vendor/name","baseModelId","displayName","inputTokenLimit","outputTokenLimit","supportedGenerationMethods"}]}` (one page). `/genai/v1/...` serves the same. |
| * | `/openai/v1/...` | Alias of `/v1/...` for every endpoint above (OpenAI SDK `base_url` ending in `/openai/v1`). |

Model kinds: `chat`, `embedding`, `speech`, `transcription`, `image`, `rerank`, `ocr`, `video`; each endpoint
accepts one kind (else 400 `wrong_model_kind`). Model prices (micro-USD): `input_price_micros` /
`output_price_micros` per 1M tokens, `audio_minute_price_micros`, `image_price_micros` per generated
image, `video_second_price_micros` per second of generated video, and `request_price_micros`, added to every successful, non-cached request of any kind
(token counting excepted).

Anthropic SDK: `Anthropic(base_url="https://gw.example.com/anthropic", api_key="sk-gw-…")`.
Google GenAI SDK: `genai.Client(api_key="sk-gw-…", http_options={"base_url": "https://gw.example.com/genai"})`,
then `client.models.generate_content(model="vendor/name", contents="hi")`.

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
`guardrail_blocked` (400), `prompt_injection` (400), `insufficient_credits` (402), `key_budget_exceeded` (402),
`rpm_exceeded` / `tpm_exceeded` (429), `body_too_large` (413), `provider_error` (upstream status or 502).
On `/anthropic/v1/*` errors use the Anthropic shape with the same status:
`{"type":"error","error":{"type","message"}}`, type by status: 400 `invalid_request_error`,
401 `authentication_error`, 402 `billing_error`, 403 `permission_error`, 404 `not_found_error`,
413 `request_too_large`, 429 `rate_limit_error`, other `api_error`. Stream errors are `event: error`.
On `/genai/*` errors use Google's shape with the same status: `{"error":{"code","message","status"}}`,
status 400 `INVALID_ARGUMENT`, 401 `UNAUTHENTICATED`, 402 `FAILED_PRECONDITION`, 403 `PERMISSION_DENIED`,
404 `NOT_FOUND`, 429 `RESOURCE_EXHAUSTED`, 502/503 `UNAVAILABLE`, other `INTERNAL`; stream errors are a
`data:` frame of the same shape.

## Guardrails

Set per org in `guardrails` on `PATCH /api/orgs/{org}` (admin). Applied before routing and before
the response-cache lookup, in this order:

| Field | Effect |
|---|---|
| `compression: "lossless"` | Chat and Responses message text (not tool results, not assistant `tool_calls`): outside ```` ``` ```` fences, trailing whitespace is trimmed, inner runs of spaces/tabs become one space (leading indentation kept), 3+ newlines become 2. Text starting with `{` or `[` and everything inside or after an unclosed fence is untouched. Chat also drops exact-duplicate `system`/`developer` string messages. Response header `X-GW-Compressed-Chars: N` when N > 0. |
| `blocked_patterns`, `max_input_chars` | 400 `guardrail_blocked` (counted after compression). |
| `prompt_injection: "warn" \| "block"` | Scans user-role text only (chat `user` messages, Responses `user` items, `/v1/completions` prompts), first 16 KB per request, for high-signal jailbreak phrasings ("ignore all previous instructions", "you are now DAN", "reveal your system prompt", …). Match sets `X-GW-Guardrail: prompt_injection`; `block` answers 400 `prompt_injection`, `warn` lets the request through. Content is never logged. |
| `mask_secrets: true` | Replaces credential shapes with `[REDACTED:<type>]`: `openai` (`sk-`, `sk-proj-`), `anthropic` (`sk-ant-`), `aws` (`AKIA`/`ASIA` key ids), `github` (`ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_`, `github_pat_`), `slack` (`xox[baprs]-`), `google` (`AIza`), `stripe` (`sk_live_`, `rk_live_`), `jwt`, `private_key` (PEM blocks). `password=`-style assignments are not masked. |
| `mask_pii: true` | Emails → `[email]`, phone numbers → `[phone]`. |

Batch files get blocked patterns, max chars and masking (no role data, so no injection scan or
compression). Embeddings, rerank, image, audio and video prompts get the same set. Metrics:
`gateway_prompt_injection_total{action}`, `gateway_compressed_chars_total`.

## Async: files, batches, videos (API key)

Upstream file, batch and video ids are global per provider account, so the gateway never shows or
accepts them: every object gets a gateway id (`file-gw-…`, `batch-gw-…`, `video-gw-…`) stored under
the caller's org, and an id of another org answers 404 `not_found`. Each object stays on the provider
and provider key that created it. Create calls check budget, credits and RPM and write a request log.

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/files` | Multipart `file`, `purpose` (`batch`, `user_data`, `assistants`, `vision`, `fine-tune`, `evals`) and the model the file is for: form field or header `x-gateway-model` (catalog id). Uploaded to that model's primary healthy target. Max `MAX_BODY_MB` (413 `body_too_large`). A `batch` file is checked line by line: `url` must be a batch endpoint of the model's kind and `body.model` the catalog id, which is rewritten to the upstream model name (else 400 `bad_batch_file`). |
| GET | `/v1/files` | The org's files, newest first; `limit` (1–100, default 20), `after` (a file id). Batch output/error files appear once their batch is retrieved. |
| GET | `/v1/files/{id}` | File object (`status` from the provider). |
| GET | `/v1/files/{id}/content` | File bytes (batch results are JSONL). |
| DELETE | `/v1/files/{id}` | Deletes upstream and the mapping. |
| POST | `/v1/batches` | `{input_file_id, endpoint, completion_window, metadata, model?}`. `endpoint`: `/v1/chat/completions`, `/v1/completions`, `/v1/responses` (chat models) or `/v1/embeddings` (embedding models); `model`, if sent, must be the file's model. Runs where the file is. |
| GET | `/v1/batches`, `/v1/batches/{id}` | Batch objects with gateway file ids. Unbilled batches are refreshed from the provider. |
| POST | `/v1/batches/{id}/cancel` | Cancels upstream. |
| POST | `/v1/videos` | Video-kind models. JSON or multipart `{model, prompt, seconds, size, input_reference}`; runs on the primary target only (no failover: the job lives at one provider). Prompt guardrails apply. |
| GET | `/v1/videos/{id}` | Video object (`id`, `status`, `seconds`, `size`, `progress`…). |
| GET | `/v1/videos/{id}/content` | Video bytes; `?variant=` passed through. 409 `video_not_ready` before completion. |
| DELETE | `/v1/videos/{id}` | Deletes upstream and the mapping. |

Billing, exactly once per object (an atomic `billed` flag on the stored object): a batch is billed when
first seen finished (`completed`, `expired`, `cancelled`, `failed`) on retrieve or list: the usage of
every successful result line (`prompt_tokens`/`input_tokens`, `completion_tokens`/`output_tokens`) is
summed and priced at the model's token prices as they were when the batch was created. Full price:
no batch discount is applied. Until the charge is stored, `output_file_id` and `error_file_id` are
withheld. A video is billed when first seen `completed` (create, retrieve or content download):
`seconds × video_second_price_micros` (model field, micro-USD per second). Charges go to the API key
that created the object. Budgets are checked when a job is created, not against its final cost.

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
| POST | `/api/orgs/{org}/providers/{id}/discover`, `/import` | admin (org-owned providers only) |
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

Key-only types (`value` + optional `base_url` override): `openai`, `anthropic`, `gemini`, `cohere`,
`mistral`, `groq`, `cerebras`, `perplexity`, `openrouter`, `nebius`, `parasail`, `elevenlabs`,
`deepseek`, `xai`, `fireworks`, `huggingface`, `replicate`, `minimax`, `byteplus`, `sarvam`, `wafer`,
`typesafe`, `opencode-go`, `opencode-zen`, `runway`, `runware`. `ollama`, `sgl`, `vllm` require
`base_url` (the server; key optional) and `databricks` requires `base_url` = the workspace URL plus a
token in `value`. Org providers may use the types Bifrost can run many times: `openai`, `anthropic`,
`gemini`, `cohere`, `bedrock`, `huggingface`, `replicate`, `typesafe`. Not offered: `github-copilot`
(OAuth device flow), `bedrock_mantle` and `sagemaker` (AWS SigV4 key config), `cloudflare` (per-key
account id).

Model import: `POST …/providers/{id}/discover` (platform: `/api/admin/providers/{id}/discover`) asks
the provider for its model list with its own keys (Bifrost `ListModelsRequest`; the provider must be
enabled) → `{"provider","models":[{"upstream","name","description","context_length",
"max_output_tokens","input_price_micros","output_price_micros"}]}` (prices only when the upstream lists
them, converted to micro-USD per 1M tokens). Org providers get a generic error instead of the upstream
body. `POST …/providers/{id}/import {"models":[{"upstream","id","name","description","kind",
"context_length","max_output_tokens","input_price_micros","output_price_micros"}]}` (1–500) creates
enabled models (public for the platform route, org-private for the org route) with one target
`{provider, upstream}` → `{"created":[ids]}`. Every entry is validated first; an existing id is 409;
a store error rolls back the models already created. Each model gets a `model.import` audit entry.

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
(`{"version": "<build version>", "bifrost_core": "v1.15.2"}`; the version is set at build time with
`-ldflags "-X main.version=…"`, which `make build` does from `git describe`, else `dev`).
