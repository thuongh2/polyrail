# Security — threat model and findings

Owner: Security agent. Review of 2026-11-10 covering `cmd/gateway`, `internal/{admin,gateway,store,usage,limits,web}`.
Severity: critical/high must be fixed before release. Line numbers refer to the reviewed revision.

## Threat model

**Assets**
- Provider secrets (platform and per-org API keys), sealed with AES-GCM under `MASTER_KEY`.
- Gateway API keys (stored as SHA-256), session cookies, user passwords (bcrypt).
- Platform money: platform provider keys are paid by the operator; orgs are billed by credits/prices.
- Tenant data: request logs, usage, member lists, org config (guardrails, private models/providers).
- The host network the gateway sits in (cloud metadata, internal services, MCP servers).

**Actors**
- Anonymous internet user (catalog, login, signup, `/metrics`, `/v1/*` with no or a guessed key).
- Self-signup user: becomes **owner of their own org**, so "org admin" capabilities are effectively anonymous when `ALLOW_SIGNUP=true`.
- Org member roles viewer < member < admin < owner, holding API keys of one org.
- Operator (platform admin) — trusted.
- Upstream providers / MCP servers — semi-trusted (responses flow back to tenants).

**Trust boundaries**
1. Internet → HTTP mux (`/v1/*` key auth; `/api/*` cookie session + `X-GW-CSRF`; public catalog and `/metrics` unauthenticated).
2. Tenant ↔ tenant: everything keyed by `{org}`; `org()` middleware checks membership, handlers must check resource ownership.
3. Tenant config → outbound network: tenant-supplied `base_url` / `extra_headers` drive server-side HTTP requests.
4. Gateway → shared platform resources: public providers, MCP servers, caches, limiter, metrics.

**Verified as sound:** org membership middleware (`admin.go:165`) returns 404 to non-members; keys/members/models/providers/logs/usage are all looked up under the path org (no IDOR found on key/provider/model/member ids); provider ownership is enforced on PUT/DELETE (`admin.go:744`, `admin.go:761`, `entities.go:544`); provider key values are never returned (`redact`), `PasswordHash`/key `Hash` are `json:"-"`; response cache key includes the org id (`gateway.go:381`); API keys are 192-bit random and SHA-256 hashed; bcrypt cost 12 with 72-byte cap and dummy-hash timing equalisation; HMAC compared with `hmac.Equal`; OIDC checks state and nonce; mutations need `X-GW-CSRF` plus `SameSite=Lax`; all UI interpolation of server data goes through `esc()` (app.js, catalog.js) and the CSP forbids inline script; SQL is parameterised.

## Findings

| ID | Sev | Location | Issue | Fix |
|---|---|---|---|---|
| H1 | high | `internal/admin/admin.go:683-704`, `internal/gateway/gateway.go:158-168` | **SSRF with response read-back.** `checkBaseURL` resolves the host once at save time; Bifrost resolves again on every request (DNS rebinding / TTL 0 bypasses the check). Ranges not blocked: `100.64.0.0/10` (e.g. Alibaba metadata `100.100.100.200`), `0.0.0.0/8`, `198.18.0.0/15`, NAT64 `64:ff9b::/96`. Bifrost's `HandleProviderAPIError` puts a non-JSON upstream body verbatim into the error message (`"provider API error: <body>"`), which `fromBifrost` returns to the caller and stores in request logs, so an org admin reads internal HTTP responses. | Enforce at **connect time**: give Bifrost a fasthttp `Dial` that resolves, rejects non-public IPs (use `netip` + explicit deny list incl. CGNAT, NAT64, 6to4, multicast, reserved), and dials the vetted IP. Keep the save-time check only for UX. For tenant providers, replace upstream error text with a generic message plus status (log the raw body server-side). |
| H2 | high | `internal/gateway/mcp.go:95-106`, `:109-133` | **MCP tools are a shared, unscoped resource.** Any API key of any org can call `POST /v1/mcp/tool/execute` for any tool on any operator-registered MCP server (Bifrost `findMCPClientForTool` searches all clients) and inject all of them with `x-gateway-mcp-clients: *`. No admit/limits, no usage record, no audit. An MCP server meant for internal use (DB, filesystem, tickets) becomes reachable by every tenant. | Add an explicit per-org allowlist of MCP server ids (org field set by operator), filter `withMCP` by it, and in `MCPExecute` resolve the tool's client and reject if not allowed. Run `admit` (RPM) and write a usage/audit row per execution. |
| H3 | high | `internal/admin/auth.go:285-289`, `auth.go:163`, `internal/admin/admin.go:489-499` | **Account pre-hijack via unverified email.** Signup and `addMember` (any org admin, i.e. any self-signup user) create users for arbitrary emails with an attacker-chosen password and no email verification. The OIDC callback then links the real person to that existing account by email and logs them in; the attacker keeps the password and, with `OIDC_DEFAULT_ORG`, gains whatever org the victim is auto-joined to. Also lets anyone squat emails (victim cannot sign up). | Do not let `addMember` set passwords or create users for unknown emails — create a pending invite instead. Require email verification on signup before the account is usable. On OIDC link to an existing account that was not created via SSO, require the password (or clear the password hash and existing sessions when linking). |
| H4 | high | `internal/store/entities.go:639`, `internal/gateway/catalog.go:207-218` | **Billing bypass through public providers.** `PutModel` accepts targets on public providers (`p.OrgID == ""`) for an org-private model whose price the org admin sets (can be 0). Requests are then served from the operator's paid keys and billed at the tenant's price. Only barrier is knowing a public provider id (64-bit, not treated as secret; UI states public providers are "available to every organization's models"). | For org models, accept only the org's own providers, or add an operator-controlled flag `shared_with_orgs` on public providers and enforce a price floor (public provider cost × markup) for such targets. Re-check ownership in `plan()` as defence in depth. |
| M1 | medium | `internal/admin/auth.go:276`, `:280-302` | OIDC treats a **missing `email_verified` claim as verified** (some IdPs omit it while letting users set `email`), combined with email-based account linking (incl. the operator account). With `OIDC_ALLOWED_DOMAINS` empty and `OIDC_DEFAULT_ORG` set, every account at the IdP (e.g. any Google user) is auto-added to the default org. | Require `email_verified == true` (opt-out env for IdPs known to verify). Refuse to start when `OIDC_DEFAULT_ORG` is set without `OIDC_ALLOWED_DOMAINS`. Never auto-link SSO to an operator account. |
| M2 | medium | `internal/admin/admin.go:706-713`, `internal/store/entities.go:125` | `extra_headers` (commonly `Authorization`, `api-key`, `x-api-key` for custom/Azure-style endpoints) are **stored in plaintext and returned** by list/put provider responses; `redact` only strips `keys`. Breaks D5 ("secrets encrypted, never returned"). | Seal header values with the `Box`; return header names only (values masked); treat a header sent without a value as "keep stored". |
| M3 | medium | `internal/admin/admin.go:662-666`, `:601-624` | **Members can lift admin-imposed limits** on their own keys: `patchKey` applies the full `keyInput` (rpm, tpm, monthly budget, model allowlist, expiry) when `CreatedBy == user`. Budgets/allowlists set by an admin are undone by the member. | For role < admin allow only `name` and `revoked=true`; changing limits/models/expiry requires admin. |
| M4 | medium | `internal/admin/admin.go:828`, `internal/web/static/app/app.js:434`, `:445` | **Model id path traversal (confused deputy).** Model ids only reject space/`?`/`#`, so an org admin can create a model `x/../..`; the UI builds `${base}/models/${id}` without encoding and the browser normalises dot segments, so an **owner** clicking "Delete" on it sends `DELETE /api/orgs/{org}` (deletes the org); `x/../../members/<owner-id>` removes a member. | Server: restrict model ids to `^[A-Za-z0-9._:-]+(/[A-Za-z0-9._:-]+)+$` and reject `.`/`..` segments. UI: `encodeURIComponent` every path segment of ids. |
| M5 | medium | `cmd/gateway/main.go:164`, `internal/gateway/metrics.go:6-15` | `/metrics` is **unauthenticated** and labels by model id and provider name, exposing every org's private model ids and provider names to anyone; tenant-chosen labels also give unbounded series cardinality (memory). | Serve metrics on a separate listen address or behind a bearer token; label private models as `private` (or by org-agnostic bucket) instead of their id. |
| M6 | medium | `cmd/gateway/main.go:177` | `http.Server` sets only `ReadHeaderTimeout`: no `ReadTimeout`/`WriteTimeout`. An anonymous client can trickle a 1 MB login body or an 8 MB `/v1` body indefinitely (slowloris), and a slow-reading stream client pins a provider stream and goroutines indefinitely. | Set `ReadTimeout` (e.g. 60 s); for streams use `http.ResponseController.SetWriteDeadline` per chunk (e.g. 30 s idle) instead of a global `WriteTimeout`. |
| M7 | medium | `internal/gateway/gateway.go:493-523` | **Guardrails bypass:** `/v1/embeddings` never calls `applyGuardrails`, so blocked patterns, `max_input_chars` and PII masking are skipped for any embedding model. | Apply guardrails to embedding input texts (string and string-array forms) before routing. |
| M8 | medium | `internal/admin/auth.go:69-72`, `:140-143` | Sessions are stateless 7-day tokens: logout only clears the cookie (a stolen cookie stays valid), and for SSO-only users the epoch is `sha256("")[:4]`, a constant, so their sessions can never be invalidated short of disabling the user. | Add a per-user `session_gen` counter in the user record included in the token; bump it on logout-all, password change, role/operator removal; shorten TTL (e.g. 24 h sliding). |
| L1 | low | `internal/gateway/gateway.go:185-200`, `cache.go:288-293` | Unauthenticated callers sending random `sk-gw-…` keys force one store read each and insert negative entries; with 50k entries and random eviction they evict valid principals (cache thrash → DB load). | Rate-limit auth failures per IP before the store lookup; keep negative entries in a separate small cache. |
| L2 | low | `internal/gateway/gateway.go:68`, `cache.go:282-289` | `RESPONSE_CACHE_MB=0` (natural way to "disable") makes `maxBytes=0`, i.e. **no byte bound** — up to 100k unbounded responses in memory. | Treat `CacheMB<=0` as cache disabled (skip Get/Set). |
| L3 | low | `internal/admin/admin.go:72-74`, `:193-201` | `login`, `signup`, `logout` are not behind the CSRF header check and `decode` ignores `Content-Type`, so a cross-site `text/plain` form can post JSON (login CSRF: victim is signed into the attacker's account). | Require `X-GW-CSRF` (or `Content-Type: application/json`) on all POSTs including auth. |
| L4 | low | `internal/admin/auth.go:121`, `admin.go:231` | Login limit is per `RemoteAddr` only: behind a reverse proxy all users share one bucket (20/min global lockout), and there is no per-account throttle against distributed guessing. | Add a per-email counter; make the client-IP source configurable (`TRUSTED_PROXY` + `X-Forwarded-For`). |
| L5 | low | `internal/admin/auth.go:75`, `:240` | One HMAC key signs both session and OIDC-state tokens with the same 2-part shape; no purpose binding. Not exploitable today (state is server-random) but fragile. | Prefix the payload with a purpose (`"sess"`, `"oidc"`) and check it in `verify`. |
| L6 | low | `internal/admin/admin.go:740-751`, `internal/store/entities.go:561-567` | Changing `base_url` keeps the stored keys, so an org admin (or anyone with admin) can repoint a provider at their own host and receive the plaintext provider key that the API never reveals. | Require re-entering key values when `base_url` or `type` changes. |
| L7 | low | `internal/admin/admin.go:694-701`, `internal/store/entities.go:637-640`, `internal/gateway/gateway.go:163-166` | Info oracles: `checkBaseURL` distinguishes "cannot resolve" from "private address" (internal DNS probing); `PutModel` says "belongs to another org" (cross-tenant provider id existence); public-provider upstream error texts (which may echo masked platform key suffixes) go to tenants. | Single generic error for both URL cases; return "provider not found" for foreign ids; sanitise upstream messages from public providers. |
| L8 | low | `internal/gateway/gateway.go:379-383` | Response cache key ignores `x-gateway-mcp-clients`, so a response containing MCP tool calls is replayed to same-org requests that did not opt into tools. | Include the resolved MCP include-list in the cache key, or skip caching when MCP is requested. |
| L9 | low | `internal/usage/usage.go:96-104`, `internal/gateway/guard.go:340` | `spendCache` and `guardCache` never evict expired / deleted-org entries (`spend/key/<id>/<yyyymm>` grows monthly). Bounded by real keys/orgs, so slow growth only. | Sweep expired spend entries on flush; delete guard entries on org reload/delete. |

## Release gate

H1–H4 block release. M-items should be fixed in the same cycle; M2, M3, M4 are small changes.

## Resolution (developer pass, 2026-11-10)

Verified by `go test -race ./...` (admin, gateway, e2e suites). New regression tests named in brackets.

| ID | Status | What changed |
|---|---|---|
| H1 | Mitigated | Full deny list (`gateway/netguard.go`: 0/8, 100.64/10, 198.18/15, 64:ff9b::/96, multicast, ULA, …); tenant provider URLs re-checked at request time (30 s cache); upstream error text is never returned for org-private models; key-like strings redacted for public ones. Residual: DNS-rebinding window ≤ 30 s because Bifrost's HTTP client resolves on its own — deployment docs require an egress firewall for internal ranges. [TestBlockedIPRanges, TestUpstreamErrorsSanitized] |
| H2 | Fixed | Operators grant MCP servers per org (`mcp_servers`); `withMCP` and `/v1/mcp/tool/execute` enforce it; tool calls are admitted (limits, budget) and logged as `mcp/<server>`. [TestMCPOrgAllowlist, TestMCPGrantOperatorOnly] |
| H3 | Mitigated | SSO links only to an account with the same issuer+subject, or an invited account with no password; otherwise 409. Residual: with password login, email is an unverified label; self sign-up is off by default (`ALLOW_SIGNUP`). |
| H4 | Fixed | A model may only target providers of its own owner (public↔public, org↔same org). [TestCrossTenantProvidersAndModels] |
| M1 | Fixed | `email_verified` must be true (opt-out `OIDC_TRUST_UNVERIFIED_EMAIL`); `OIDC_DEFAULT_ORG` without `OIDC_ALLOWED_DOMAINS` refuses to start; default role cannot be owner. |
| M2 | Fixed | `extra_headers` removed (nobody needed it). |
| M3 | Fixed | Members may only rename/revoke their own keys. [TestMemberCannotLiftAdminLimits] |
| M4 | Fixed | Strict model id pattern, no `..`; UI encodes path segments. [TestModelIDTraversalRejected] |
| M5 | Fixed | `METRICS_TOKEN` protects `/metrics`; private models are labelled `private`. |
| M6 | Fixed | `ReadTimeout` 60 s; each stream write has a 30 s deadline. |
| M7 | Fixed | Guardrails apply to `/v1/embeddings`. |
| M8 | Fixed | Session lifetime 3 days; sign-out bumps a per-user generation, ending every session (SSO users too). |
| L1 | Accepted | Negative auth results are cached; per-IP throttle of failed key auth is future work. |
| L2 | Fixed | `RESPONSE_CACHE_MB=0` disables the cache. |
| L3 | Fixed | `X-GW-CSRF` required on login, signup and logout. [TestLogin] |
| L4 | Partial | Per-email login throttle added (10/min); trusted-proxy client IP not implemented. |
| L5 | Fixed | Tokens carry a purpose (`sess`, `oidc`). |
| L6 | Fixed | Changing `base_url` or `type` requires re-entering keys. |
| L7 | Fixed | Unresolvable and private hosts give the same error; missing/foreign providers give the same error. |
| L8 | Fixed | No response caching when MCP tools are requested. |
| L9 | Accepted | Bounded by real orgs/keys; spend cache entries are regenerated per flush generation. |

Also fixed from QA: a tenant can no longer shadow a platform provider (tenants get multi-instance types only), org deletion is audited at platform level, key revocation and org changes clear the auth cache on the same node immediately.

## Review: Bifrost core v1.15.2 upgrade and new endpoints (2026-11-11)

Scope: `git diff 7d830e9` without `web/` — catalog secret handling, images/rerank/OCR/token
counting, files/batches/videos, Google GenAI drop-in, Anthropic, model discovery/import, new
provider types, MCP changes. Verified by `go vet` and `go test -race -count=1` (admin, gateway,
store, limits, usage, e2e; Postgres + Redis); `internal/web` and `cmd/gateway` were skipped because
the UI build in `internal/web/dist` was being regenerated concurrently.

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| H5 | high | **Full-read SSRF through upstream-supplied URLs.** Replicate, Hugging Face and Typesafe became tenant-creatable (Bifrost `SupportedBaseProviders`). The Replicate adapter polls `urls.get`/`urls.stream` and downloads output URLs taken from the upstream reply; Hugging Face speech downloads `audio.url`; Gemini video download fetches the operation's video URI (following 5 redirects). These use the provider HTTP client, whose v1.15 dialer always allows **loopback**. A tenant pointing `base_url` at its own public server could make the gateway GET e.g. `http://127.0.0.1:8123/?query=…` (ClickHouse with every org's logs) and receive the body as a chat result, audio or video. | Fixed. Org providers of types that follow upstream URLs (`replicate`, `huggingface`) must use the vendor host (`base_url` empty) — refused at save, at discovery and at request time (`tenantReachable`); org Gemini providers with a custom `base_url` are refused for files/batches/videos. `ALLOW_PRIVATE_PROVIDER_URLS` lifts both. [TestTenantProvidersThatFollowUpstreamURLs, TestProviderSSRF] |
| M9 | medium | **Batch input bypassed guardrails and the org output cap.** Every line of a `purpose=batch` file ran upstream without blocked patterns, `max_input_chars`, PII masking or `max_tokens_cap`. | Fixed. `rewriteBatchFile` applies the org's guardrails per request to the text fields (`content`, `text`, `prompt`, `input`, `instructions`, `system`; not image/audio data) and clamps or sets `max_completion_tokens`/`max_tokens`/`max_output_tokens`. A blocked file is `guardrail_blocked` and logged. [TestBatchFileGuardrailsAndTokenCap] |
| M10 | medium | **Private-network policy not passed to Bifrost.** v1.15's dialer refuses RFC 1918 unless `NetworkConfig.AllowPrivateNetwork`; the gateway never set it, so operator providers on a LAN (Ollama/vLLM/SGLang at `10.x`, Docker networks) failed, inviting operators to work around the guard. | Fixed. `AllowPrivateNetwork` is true for operator providers, and for org providers only with `ALLOW_PRIVATE_PROVIDER_URLS`; tenants keep Bifrost's dial-time block of private and link-local addresses on top of our checks (narrows the H1 DNS-rebinding residual to loopback). [TestTenantProvidersThatFollowUpstreamURLs] |
| L10 | low | GenAI `:countTokens` charged the model's `request_price` although documented as not billed. | Fixed (priced at 0 like the other count-tokens endpoints). |
| L11 | low | `GET /v1/files/{id}/content`, `/v1/videos/{id}/content` and batch billing (`BatchResultsRequest`) hold the whole upstream object in memory (Bifrost core returns `[]byte`); a large batch output on a 2 GB node is a memory risk. | Accepted; needs a streaming download in Bifrost core. Uploads are bounded by `MAX_BODY_BYTES`. |
| L12 | low | `?key=` (GenAI SDK auth) puts the API key in the URL, so reverse-proxy access logs can capture it. | Accepted for SDK compatibility; SDKs send `x-goog-api-key` by default. Deployment docs should keep query strings out of proxy logs. |
| L13 | low | A video is billed on its upstream `seconds`; if the provider omits it the charge is 0, and a video deleted before it was ever retrieved is never billed. Upstream `usage.cost` and asset ids are passed through in video objects. | Open. Fall back to the requested seconds; strip `usage`. |
| L14 | low | `GET /v1/batches` refreshes up to 100 unbilled batches upstream per call (amplification). Batch spend is only admitted at creation, so one batch can exceed the remaining budget. | Accepted (rate limits apply per call; usage is billed exactly once). |
| L15 | low | Non-batch file uploads (`user_data`, `vision`, …) are not guardrailed; MCP execute with a bare tool name present on several servers picks the last one (then denies if not granted). Responses/Anthropic `instructions`/`system` text is not guardrail-checked (pre-existing). | Open. |

## Guardrails: credential masking, prompt injection, compression (2026-11-11)

Per-org, opt-in (`docs/API.md` → Guardrails, D15).

- **Credential masker** (`mask_secrets`): fixed, precompiled patterns for provider keys, cloud and
  SaaS tokens, JWTs and PEM private keys, replaced with `[REDACTED:<type>]` before the request
  leaves the gateway. Patterns require a word boundary and a minimum token length, so prose such as
  `risk-assessment-…` or `scikit-learn` is not hit (tested). Generic `password=` / `api_key=`
  assignments are deliberately not masked (mostly `os.getenv(...)` calls in code). The masker cuts
  accidental leaks to providers; it is not DLP: unknown key formats pass.
- **Prompt-injection detector** (`prompt_injection`): a single precompiled regex of high-signal
  phrasings, run on user-role text only and on at most 16 KB per request (bounded CPU). `block`
  answers 400 `prompt_injection`; `warn` passes and sets `X-GW-Guardrail`. Neither logs the text;
  only `gateway_prompt_injection_total{action}` is counted. Regexes are easy to evade (paraphrase,
  other languages, encodings); treat it as a tripwire, not a defence. System/developer text is the
  tenant's own and is not scanned.
- **Lossless compression** (`compression`): whitespace-only rewrite outside code fences; tool
  results, JSON-looking text and assistant tool calls are never changed. The response cache key is
  still the raw request body, so identical bodies always share a key.
- Order: compression → blocked patterns / max chars → injection → secret masking → PII masking.
  Blocked patterns therefore see unmasked text.

Checked and fine: stored secrets go to Bifrost as literal `SecretVar`s (no `env.`/`vault.` resolution);
job ids are validated and scoped `<kind>/<org>/<id>` (another org's id is a 404), upstream ids never
leave the gateway, `ClaimBilling` is exactly-once and batch output stays hidden until billed; batch
lines are pinned to the file's model and endpoint kind; new endpoints all authenticate, resolve +
admit, guard text and record; upstream errors are hidden for org-private providers; multipart
bodies are bounded by `MAX_BODY_BYTES`; OCR `document_url`/`image_url` and chat image URLs are
fetched by the provider or by Bifrost's SSRF-hardened fetcher; discovery/import are scoped to the
route's owner; Databricks/vLLM/Ollama/SGLang URLs come only from the checked `base_url`.

## Webhooks and semantic cache (2026-11-11, developer notes for review)

Webhooks make the gateway send HTTP requests to tenant-chosen URLs (trust boundary 3), and the
semantic cache shares answers between requests of one org.

- **SSRF.** A webhook URL must be `https` on a host whose every DNS answer is public
  (`gateway.CheckPublicURL`), checked on create/update and again before every delivery attempt. The
  delivery client dials through `gateway.PublicDialer`, whose `Control` hook refuses blocked ranges
  after resolution, so DNS rebinding between check and connect cannot reach loopback, RFC 1918,
  link-local (cloud metadata) or other blocked nets. Redirects are not followed, no egress proxy is
  used (it would hide the destination from the dial check), URLs with userinfo are refused.
  `ALLOW_PRIVATE_PROVIDER_URLS=true` lifts the https and public-address rules (dev setups).
  [TestSSRFGuard, TestWebhookURLNeedsPublicHTTPS]
- **No response oracle.** Receiver bodies are read (≤ 64 KiB) and discarded; the test endpoint
  reports only the status code and a generic error, so a webhook cannot be used to read internal
  services.
- **Signing.** Each org webhook has a 256-bit random secret, sealed with the `MASTER_KEY` Box like
  provider keys, `json:"-"` in API output and returned once on create. Signatures are
  HMAC-SHA256 over `"<unix ts>.<body>"`; receivers should check the timestamp window to stop replays
  (snippet in docs/API.md). Audit entries record only the webhook host (paths often carry tokens).
- **Payloads** never contain prompt or completion text or upstream error messages: `request.failed`
  carries model id, status, a fixed code and key id.
- **Abuse bounds.** At most 20 webhooks per org; bounded queue (drop + metric); 10 s timeout and 3
  retries per delivery; auto-disable after 10 consecutive failures; `request.failed` and
  `budget.exceeded` deduplicated per minute (dedupe map capped at 10k keys).
- **Semantic cache isolation.** Entries are keyed by org and chat model and only served to the same
  org. Only `temperature: 0` requests without tools, MCP or `json_schema` output qualify. A
  near-match can return another member's answer to a semantically similar prompt within the same
  org; orgs opt in knowing this. Memory is bounded per org (1,000) and globally (`RESPONSE_CACHE_MB`).

## Review: OmniRoute wave 1 (2026-11-11)

Scope: `git diff fd7e404..HEAD -- internal cmd` (routing strategies, guardrails, models query,
Idempotency-Key, webhooks, semantic cache, `PublicDialer`). Verified by `go vet ./...` and
`go test -race -count=1 ./...` with Postgres and Redis.

Checked and fine: Idempotency-Key entries are scoped by API key id, method and path (another key,
even in the same org, never gets a replay; a revoked key fails auth first) and only 2xx bodies are
stored; semantic-cache entries are keyed by org, chat model and embedding model and looked up only
after `resolve` (key model allowlist), `admit` and guardrails, so neither another org nor a key
without access to the chat model can get a hit; the embedding call is billed to the key;
`/v1/models/query` uses the same visibility and key allowlist as `GET /v1/models`; webhook
secrets are sealed with the Box, `json:"-"`, returned once, and signatures are HMAC-SHA256 over
`<t>.<body>`; webhook routes need org admin and every mutation is audited (host only); delivery
re-checks the URL, dials through `PublicDialer` (IPv4-mapped addresses unmapped), uses no proxy and
follows no redirects; the injection regex and secret patterns are RE2 (linear time) and the
injection scan stops at 16 KB; block mode logs only the fixed reason; `X-GW-Route` values pass
through `net/http`, which replaces CR/LF in header values.

| ID | Severity | Finding | Resolution |
|---|---|---|---|
| M11 | medium | **One org could starve every org's webhooks.** The delivery queue (1,000) is shared and `key.created`/`key.revoked` are not deduplicated, so an org admin creating keys in a loop (or 20 slow endpoints) filled the queue and other orgs' events were dropped. | Fixed. An org may hold at most 1/10 of the queue (queued or being delivered, minimum 16); its excess events are dropped and counted, other orgs keep their slots. Residual: 20 endpoints that answer just under the 10 s timeout can still keep the 4 workers busy and delay (not drop) other orgs' events. [TestEmitPerOrgQueueShare] |
| L16 | low | The deny list missed IPv4-compatible (`::a.b.c.d`), Teredo (`2001::/32`), 6to4 (`2002::/16`) and local NAT64 (`64:ff9b:1::/48`) addresses, which embed IPv4 addresses a relay could forward internally. | Fixed (`::/96`, `2001::/32`, `2002::/16`, `64:ff9b:1::/48` blocked). [TestBlockedIPRanges] |
| L17 | low | The idempotency store has its own fixed 256 MB budget (not `RESPONSE_CACHE_MB`) with random eviction shared by all orgs: one key can fill it cheaply on a free model, and evicting another org's in-flight claim or stored result lets that org's retry run (and bill) twice. | Open. Size it from config and evict per key/org; Redis when multi-node. |
| L18 | low | The semantic cache budget is separate from the response cache (total memory up to 2 × `RESPONSE_CACHE_MB`), and eviction only removes the adding org's entries, so one org can occupy the whole budget and other orgs' entries are skipped. | Open. Per-org byte share; count both caches against one budget. |
| L19 | low | Router stats (`targetStats`) and breaker maps are never pruned when targets change; an org admin churning model targets grows them slowly (one entry per used target). | Accepted (bounded by admin writes plus requests); prune on snapshot reload. |
| L20 | low | Upstream 401/403 now count as target faults on shared public targets; a tenant that can provoke a 403 with request content (for example a foreign file URI) can open a public target's breaker for 30 s for all orgs. Open targets are still tried last, so this reorders rather than denies. | Accepted. |

## OpenAI-compatible org provider types (2026-11-11, developer notes for review)

Org providers of `groq`, `deepseek`, `xai`, `mistral`, `openrouter`, `fireworks`, `cerebras`,
`perplexity`, `nebius`, `parasail`, `minimax`, `sarvam`, `wafer`, `opencode-go`, `opencode-zen` run on
Bifrost's openai base at a URL fixed in code (`gateway.OpenAICompatible`), so they add no SSRF
surface: `base_url` is refused at save for org providers of these types (even with
`ALLOW_PRIVATE_PROVIDER_URLS`), and the stored `base_url` is never used for them. The request-time
guard (`tenantReachable`) sees an empty `base_url` and passes, as for other hosted APIs; tenant
providers still get `AllowPrivateNetwork=false`. The openai base does not follow URLs from upstream
replies for chat, embeddings or model lists, so H5 does not apply. Tenant keys are sent only to the
vendor host. [TestOrgOpenAICompatibleConfig, TestOrgOpenAICompatibleChat, TestOrgOpenAICompatibleProviders]

## Outbound proxy (2026-11-11, developer notes for review)

Global proxy (`UPSTREAM_PROXY_URL`, operator env) and per-provider `proxy` are passed to Bifrost as
`ProviderConfig.ProxyConfig` with literal SecretVars (never `env.*` resolution). Rules:

- **Tenant proxies are off by default.** An org provider's `proxy` is refused (400) unless
  `ALLOW_TENANT_PROXY=true`; a proxy stored while it was allowed is ignored (the global proxy or a
  direct connection is used) once it is turned off. With it on, the proxy host must resolve only to
  public addresses at save (`gateway.CheckPublicProxy`) and again before each request
  (`tenantReachable`, 30 s cache), unless `ALLOW_PRIVATE_PROVIDER_URLS`. A proxy connects wherever it
  is asked, so a tenant-chosen private proxy would be an SSRF relay into internal networks.
- **The dial-time private-address block does not apply through a proxy.** Bifrost's dialer
  (`AllowPrivateNetwork=false` for tenants) checks the address it connects to, which is then the
  proxy, not the upstream. Org provider `base_url`s are still checked at save and before every
  request (H1 rebinding residual unchanged). Operators who let org admins add providers and use a
  global proxy should make the proxy deny internal ranges. Hosts in `UPSTREAM_NO_PROXY` connect
  directly and keep the dial-time checks (Bifrost returns them to the checked dialer).
- **Secrets:** the proxy password is sealed at rest (`sealed_password`), never returned (admin
  `redact` → `ProviderProxy.Redacted`), and must be re-entered when the proxy URL or username
  changes, like keys on a `base_url` change. Credentials in `UPSTREAM_PROXY_URL` are split out and
  never logged (startup logs the URL without them).
- A tenant's `ca_cert_pem` is trusted only by that provider's own client.
[TestProviderProxy, TestProxyConfigPrecedence, TestCheckProxyAndParseGlobal, TestChatGoesThroughProviderProxy]
