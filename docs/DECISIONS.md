# Decisions (ADR log)

Format: context → decision → cost if wrong.

**D1 Bifrost core v1.15.2 on Go 1.27.** The owner wants Bifrost core as the SDK for every model
call and its full feature set. Core v1.15.2 needs Go 1.27; the sandbox only had Go 1.24.7 and can't
download toolchains, so (with the owner's approval) Go was built from source from the official
golang/go `go1.27.3` tag into `/opt/gosrc/go127`. → Pin core v1.15.2 and use only its public API
(`bifrost.Init`, `schemas.*`, `providers/openai` and `providers/anthropic` converters,
`bifrost.IsSupportedBaseProvider`, `mcp.ValidateMCPClientName`). Upgrade notes (v1.2.38 → v1.15.2):
every request takes a per-request `*schemas.BifrostContext` (from `r.Context()`); the serving
provider/model come from `ExtraFields.RoutingInfo`; key secrets are `schemas.SecretVar`, built as
literals (never `NewSecretVar`, which would resolve `env.*`/`vault.*` references from tenant
input); an empty key model list now means "none", so the Account sends `["*"]`; cloud deployments
are key `Aliases` (Azure api-version rides on each alias); Ollama/SGLang read their URL from a key,
so the Account adds one from the provider base URL; MCP clients are filtered and addressed by name
and tools are exposed as `<server>-<tool>`. Core replaced idle RSS ~28 MB with ~55 MB and the binary
grew 33 → 47 MB (more providers and SDKs linked in). → If wrong: an upgrade touches
`internal/gateway` only.

**D2 GitHub mirrors via `replace`.** Only GitHub is reachable. → `go.mod` replaces golang.org/x/*,
gopkg.in/*, cloud.google.com/*, google.golang.org/protobuf with their GitHub mirrors (identical
code). → If wrong: delete the replace block and run `go mod tidy`.

**D3 Storage split by access pattern.** Owner requirement: scale to millions of users, stay light.
→ Control plane + rollups + logs in an ordered KV; hot path served from memory; usage async.

**D4 stdlib `net/http`, embedded vanilla UI.** No framework, no JS build step.

**D5 Secrets.** Provider keys AES-256-GCM with key derived from `MASTER_KEY` (SHA-256). API keys
stored as SHA-256 only; shown once. Passwords bcrypt cost 12.

**D6 One `KV` seam, Pebble + Postgres.** RocksDB rejected: CGO + C++ toolchain, larger memory.
Pebble is a pure-Go LSM with the RocksDB design (CockroachDB's engine). Postgres used as an ordered
KV (B-tree) so entity code is written once. → If wrong: hot relational queries would need real
tables; the seam keeps that local to `store`.

**D7 Budget enforcement by aggregated delta flush** instead of per-request DB writes or leases:
each node flushes per-org/per-key spend deltas every second. Overspend bound = 1 s of traffic per
node. Simpler than leases, same write rate.

**D8 Prometheus via client_golang.** Already a transitive dependency (Pebble); reuse it.

**D9 MCP via Bifrost core MCP manager.** HTTP/SSE servers only; STDIO is not supported (it would
execute local commands). Operators grant servers per org; tools are injected only on request.
Server names follow Bifrost's rule (ASCII, no spaces or hyphens, not starting with a digit; checked
at save time with core's validator) and tools reach the model as `<server>-<tool>`. Each server is
one shared persistent connection (`NeedsSessionStickiness`), as before the core upgrade.

**D10 Models route only to providers of the same owner.** Public models → public providers, org
models → that org's providers. Prevents tenants re-pricing platform capacity (SECURITY H4).

**D11 Cost rounding.** Per-request cost is rounded to the nearest micro-USD (unbiased over many
requests); stored as int64 micro-USD.

**D12 Web UI: React + Vite + Tailwind + shadcn/ui, prebuilt and embedded.** The owner asked for an
OpenRouter-like UI on shadcn. Built files are committed in `internal/web/dist` so Go builds need no
Node; no chart or state libraries (hand-written SVG charts, `useAsync`), wouter for routing, console
lazy-loaded. Initial JS 74 kB gzip. CSP keeps `script-src 'self'`; styles allow inline attributes
(Radix positioning).

**D13 Cloud providers (Azure, Bedrock, Vertex) as per-key config.** Bifrost carries their credentials
in `schemas.Key` (`AzureKeyConfig`, `BedrockKeyConfig`, `VertexKeyConfig`), so `store.ProviderKey`
gets optional `azure` / `bedrock` / `vertex` objects mapped 1:1 in the gateway Account. Secret parts
(Bedrock access/secret/session keys, Vertex service-account JSON) are sealed with the same `Box` as
key values and redacted from every API response; non-secret parts (endpoint, region, deployments)
are returned so the UI can edit them. Bedrock is a Bifrost custom-base type, so it is multi-instance
and tenants may add it; Azure and Vertex are single-slot standard providers, platform only. Empty
Bedrock/Vertex credentials mean the host's cloud identity (IAM), which only operators may use: the
admin API rejects it for org providers and the Account drops such tenant keys. Regions and project
ids are checked against `^[a-z0-9][a-z0-9-]{0,62}$` because Bifrost formats them into upstream
hostnames. Not tested against real Azure/AWS/GCP in the sandbox (no cloud accounts).

**D14 Routing strategies.** `store.Model.strategy` picks how `plan()` orders healthy targets
(`internal/gateway/routing.go`): weighted (default), priority, round-robin, random, p2c, least-used,
latency, cost, cache-affinity (OmniRoute parity, wave 1). Open-circuit targets always go last, so
every strategy keeps the failover chain. Stats (in-flight, EWMA latency and error rate, alpha 0.2)
keyed by `provider|upstream model` and round-robin counters keyed by model are per node and in
memory, so bounded by the catalog; no cross-node sharing (each node balances its own traffic). In-flight is counted only
around the chat completions and Responses upstream calls, the hot paths; latency is whole-request
time (streams included) recorded in `settle`, not time to first token. Cache-affinity uses
rendezvous hashing of `prompt_cache_key` (else SHA-256 of the system/tools prefix) × target id, so
the same key keeps its primary on every node and removing a target moves only its keys; the key is
computed lazily (a func passed to `plan`) so other strategies never hash prompts. The `cost`
strategy may use per-target price overrides; billing stays on the model price. Breaker rule: only
target faults count (5xx, 408, 429, network errors/timeouts, and upstream 401/403, because the
gateway has already authenticated the caller so those mean the provider key is wrong); 400/404/413/
422 are request errors and no longer open circuits. When everything failed, each fallback's status
comes from Bifrost's `FallbackAttempts` (skipped attempts are not counted). A 429 carrying
Retry-After (Bifrost's `ExtraFields.RetryAfter`, already clamped 1 s..5 min) opens the primary
until then, capped at 5 min. `X-GW-Route: <strategy>; <provider name>/<upstream model>` on chat
completions names the decision; the provider name is already in `x-gateway-provider`.

**D15 Guardrails: credential masker, prompt-injection tripwire, lossless compression.** Three
opt-in fields on `store.Guardrails` (`mask_secrets`, `prompt_injection: ""|warn|block`,
`compression: ""|lossless`), kept in the existing struct so they ride the org JSON and auth cache.
`applyGuardrails` takes a `guardInput` (all / user-role / prose text) so the injection scan sees
only user text and compression never touches tool results; batch files have no role data and get
neither. Secret patterns are fixed well-known prefixes; generic `password=` assignments are skipped
(high false-positive rate on code). Injection is one stdlib regex of high-signal phrasings, capped
at 16 KB per request, no content logging. Compression is whitespace only (lossy "Caveman"/LLMLingua
stay out, see OMNIROUTE_PARITY). The cache key stays the raw body: deterministic, and compression
runs before the lookup so an identical body always compresses identically.

**D16 Idempotency keys in node memory; cost as a response header.** `Idempotency-Key` (OmniRoute
parity) wraps the non-stream JSON POST handlers (`Gateway.Idempotent`, applied at route
registration): scope = API key id + method + path + key, body compared by SHA-256, only 2xx stored
(24 h, bounded `ttlCache`), replays are not billed, 422 on body mismatch, 409 while in flight.
Streams ignore it (a replay cannot reproduce timing, and the response is not buffered). Storage is
per node; a multi-node deployment needs Redis to make retries exact across nodes (shortcut noted in
code). `X-GW-Cost-Micros` is the value `record()` billed, set before the body is written; streams
cannot carry it because headers go out before usage arrives (a trailer would be invisible to most
SDKs). Model discovery `POST /v1/models/query` reuses `Snapshot.Visible` and the key's model list,
so it can never show more than `GET /v1/models`; prices in the query are USD per 1M as numbers.

**D17 Webhooks: in-process queue, signed, SSRF-guarded.** New package `internal/webhook`. Events go
into a bounded channel (`WEBHOOK_QUEUE`, drop + metric when full) served by 4 workers per node; the
gateway hot path only calls a non-blocking `Emit` through the `gateway.Events` interface (no import
cycle, no DB wait). Webhooks live in the KV under `hook/<org>/<id>` with the secret sealed by the
existing `Box` (`storedWebhook` wrapper); their writes skip the config-version bump because no
catalog snapshot depends on them and the failure counter changes on every failed delivery.
Signature `t=<unix>,v1=<hex HMAC-SHA256(secret, t "." body)>` (Stripe-style, so receivers can reuse
known code). Retries: 3 after the first attempt on network errors/5xx with 1/2/4 s backoff; a
delivery that ends failed counts once toward the 10-failure auto-disable. Delivery is best effort
(events in the queue are lost on restart): a durable outbox would need a KV queue and a leader per
event, not worth it for notifications. `request.failed` is emitted from `record()` for any logged
upstream error (status ≥ 400 except 499 and guardrail blocks), deduplicated per (org, model) per
minute; `budget.exceeded` from `admit()` on the 402s, per (org, code, key) per minute. URL rules:
https + public DNS at save and send, plus a dial-time check (`gateway.PublicDialer`);
`ALLOW_PRIVATE_PROVIDER_URLS` is the one dev/test escape hatch, as for provider base URLs.

**D18 Semantic cache: in-memory, per node, brute-force cosine.** Opt-in per org
(`Org.SemanticCache{model, threshold, ttl_seconds}`), served in `gateway/semcache.go`. Only
deterministic chat requests qualify (temperature explicitly 0, no tools/MCP/json_schema, non-stream).
The embedding is computed by calling Bifrost's `EmbeddingRequest` with the org's embedding model on
the same resolve/settle/record path as `/v1/embeddings` (no HTTP self-call), billed to the calling
key; the key's model allowlist is skipped because the model is an org setting. Vectors are normalized
float32; lookup is a linear dot-product scan of the org's ≤ 1,000 entries (≈ 1.5 M multiply-adds at
1,536 dims, under a per-org lock), LRU eviction inside the org and a global byte budget of
`RESPONSE_CACHE_MB` (separate from the response cache's own, SECURITY L18). No vector DB or ANN index: the ponytail ladder stops at a slice; revisit if
orgs need more than ~10k entries or cross-node sharing. Fail open on any embedding error.

**D19 Production model catalog from models.dev, hidden until a provider is enabled.** Owner request:
a fresh gateway starts with the real catalog and no keys. Source: models.dev (MIT, credit in
`internal/catalog/LICENSE.models.dev`), commit 3751776. `scripts/gencatalog.py` (Python 3.11
`tomllib`: go.mod has no TOML library and this runs only at build time) writes
`internal/catalog/catalog.json` (≈120 KB, `go:embed`). One gateway model per canonical models.dev id
`<author>/<model>` (`base_model`, or the file id for a first-party file without one), so a model
offered by several providers is one model with several targets: first-party first, then cheapest
input price, OpenRouter last, strategy `priority` (fallback chain); the model price is the first-party
price, else the cheapest. Kind from models.dev `type`, else modalities. Skipped: deprecated models,
models without a positive price in a unit we bill (per-character TTS such as `tts-1`, megapixel
images), realtime/live, deep-research and router models, rerank models (models.dev has no price for
them), providers needing per-deployment config (azure, bedrock, vertex, sagemaker, cloudflare,
databricks, ollama, vllm, sgl) and types models.dev lacks (parasail, replicate, byteplus, typesafe,
runway, runware). OpenRouter only adds targets to models another provider offers (no
OpenRouter-only models). Descriptions: first sentence of the OpenRouter `models.json` entry when the
model matches, else models.dev's own. Seeding writes through the store (no admin key validation):
providers `cat-<type>` (deterministic, so a second node's create fails with ErrExists and is skipped),
disabled, keyless; models enabled. It runs only when the store has no providers (`SEED_CATALOG=0`
off); `POST /api/admin/catalog/sync` adds missing ids for existing installs and never edits records.
Visibility: `Snapshot.Live` = enabled and at least one target provider loaded (enabled); `Model()`
and `Visible()` use it, so lists, public pages and routing agree, and a model with no live provider
is `model_not_found`. The platform admin list still shows everything. Regenerate with
`python3 scripts/gencatalog.py <models.dev checkout> <commit> > internal/catalog/catalog.json`.

**D20 OpenAI-compatible vendors as org provider types.** Org admins used to get only Bifrost's multi-instance base types, because a
native provider (groq, mistral, ...) has one Bifrost slot per type that a tenant must not take. The
key-only vendors whose API is OpenAI-compatible (`gateway.OpenAICompatible`: groq, deepseek, xai,
mistral, openrouter, fireworks, cerebras, perplexity, nebius, parasail, minimax, sarvam, wafer,
opencode-go, opencode-zen) are now org types too: an org provider of such a type is registered as a
custom provider `p-<id>` on Bifrost's openai base (`CustomProviderConfig{BaseProviderType: openai}`)
with `NetworkConfig.BaseURL` fixed to the URL Bifrost's native adapter defaults to (without `/v1`,
which the openai base appends). DeepSeek and Perplexity serve chat at `/chat/completions` (and
DeepSeek models at `/models`), so they carry `RequestPathOverrides` copied from the native adapters.
Org admins cannot set `base_url` for these types (400), so there is no tenant-chosen host. The
provider keeps its own `type` for logos and labels. Platform providers of these types are unchanged
(native adapter, optional `base_url`). Trade-off: org providers lose native extras (e.g. Groq audio,
Fireworks' config-based model list, Perplexity search fields beyond the OpenAI shape); chat,
embeddings where offered and model discovery go through the generic OpenAI engine. Left out:
byteplus (`/api/v3` paths, regional host, no model list), runware and runway (task APIs),
elevenlabs (not OpenAI-compatible). `gateway.OrgType(t)` = multi-instance or in the table.

**D21 Knowledge bases: gateway-native RAG on the KV seam.** Owner request: orgs ground chat on their
own documents with any embedding provider. Bifrost core v1.15.2 has no vector store (only the OpenAI
`file_search` tool shape for passthrough), so KBs are gateway-native (design: docs/KB.md). KB, doc and
queue records plus AES-GCM-sealed text and int8-quantized vectors live in `store.KV` (`kb/`, `kbdoc/`,
`kbsrc/`, `kbchunk/`, `kbq/`) without bumping `meta/version`. Ingestion is a KV-leased queue drained by
`KB_WORKERS` per node; embeddings go through the normal Bifrost resolve/record path and are billed to
the org. Retrieval is a per-node in-memory index per KB (LRU byte budget `KB_INDEX_MB`), brute-force
int32 dot product + BM25 fused with RRF k=60. Grounding inserts a system message of delimited,
untrusted excerpts with `[n]` citations after the last user turn, for keys with `kb_ids` or requests
with `X-GW-KB`; fails open; cache keys include `kb@rev`. OpenAI-compatible `/v1/vector_stores` subset
with direct upload. `kb.VectorStore` is the seam for pgvector/Qdrant. If wrong: ~1M resident chunks
per node needs ~1.6 GB index (measured ~1.6 KB/chunk at 512 dims) or the external seam; only `internal/kb` changes.

**D22 Outbound proxy for upstream calls.** Owner request: model calls through a proxy. Bifrost core
v1.15.2 has it per provider (`ProviderConfig.ProxyConfig`: http/https via CONNECT, socks5, a CA, a
runtime `NoProxy` list), so the gateway only builds that config: a provider's own `proxy` >
the global `UPSTREAM_PROXY_URL` (with `UPSTREAM_NO_PROXY`, `UPSTREAM_PROXY_CA_FILE`) unless the
provider sets `no_proxy` > direct. No Bifrost "environment" type: `HTTP(S)_PROXY` would also catch
webhooks and SSO and is read differently by fasthttp and net/http. The password follows the sealed
secret pattern. Org providers get their own proxy only with `ALLOW_TENANT_PROXY` and a public proxy
host (docs/SECURITY.md "Outbound proxy"). A proxy change restarts that provider's client through
`Reload` → `BF.UpdateProvider`, like a `base_url` change. If wrong: per-key proxies or proxy pools
would need Bifrost changes; everything else is in `internal/gateway/proxy.go`.

**D23 Proxy pools: rotation and egress reliability (OmniRoute port, scoped).** Owner request: configure
and rotate proxies like OmniRoute. Ported: an operator-managed pool registry (`store.ProxyPool`,
platform-only, member passwords sealed), assigned per platform provider (`proxy_pool`, exclusive with
`proxy`) or as the default for every provider (`UPSTREAM_PROXY_POOL`, id or name); precedence is
own proxy/pool > global pool > `UPSTREAM_PROXY_URL` > direct, `no_proxy` opts out; strategies
round-robin, random, sticky (`sticky_minutes`, default 30) and latency (EWMA of connect time); only
usable members are candidates; a member whose dial fails is set aside 60 s, doubling to 600 s;
three consecutive failures (dials or probes) mark it down until a good TCP probe
(`PROXY_HEALTH_INTERVAL`, default 10m, 2 s connect); `fail_closed` (default true) refuses when no
member is usable instead of connecting directly. Mechanism: Bifrost takes one static proxy per
provider, so providers on a pool get a loopback CONNECT forwarder (127.0.0.1, random port, provider
id + per-process token as proxy credentials) that picks a member per tunnel and dials it with
Bifrost's own `network.DialViaProxyTLS` (http/https CONNECT, SOCKS5). Rotation is per new upstream
connection (keep-alive reuses one); cursors and health are per node. Tenant tunnels are resolved and
refused when the target is not public, then pinned to the checked IP. Not ported (tech-lead ruling,
binding): rotating to another exit on a provider 429 or per-IP quota set-aside, spreading accounts
across egress IPs or observing accounts per exit, free public proxy lists, proxy subscriptions /
sing-box, throwaway edge relays, stealth fingerprinting: these evade provider rate limits or terms.
The forwarder sees only TLS tunnels, so a provider 429 cannot move the selection (test
`TestProvider429DoesNotRotate`). Pool events are logs + `gw_proxy_dials_total` /
`gw_proxy_member_up`; no platform webhook system was invented (org webhooks are per org). If wrong:
shared cursors/health need Redis; per-request rotation would need Bifrost to open a connection per
request.

**D24 Rename to Polyrail.** Owner ruling (2026-11-11): the open-source name is **Polyrail**
("One API for every model. Self-hosted, light, open."), replacing agent-gateway. Renamed: Go module
`github.com/polyrail/polyrail`, `cmd/polyrail` (binary `polyrail`, still with the `healthcheck`
subcommand; `make dist` builds `bin/polyrail-<os>-<arch>`), Docker image and entrypoint `/polyrail`,
compose service `polyrail`, systemd unit `deploy/polyrail.service`, `scripts/run-local.sh` (log in
`polyrail.log`), UI name and logo (`web/src/components/Logo.tsx`, `web/public/favicon.svg`,
`docs/assets/polyrail.svg`), startup log `polyrail listening`, `GET /api/version` gains
`"name":"polyrail"`, outbound User-Agents `Polyrail` (knowledge-base URL fetch) and
`Polyrail-Webhook/1` (webhooks). Kept unchanged for compatibility, because clients, dashboards and
existing data depend on them: HTTP headers `X-GW-*` and `x-gateway-*` (including `X-GW-CSRF`,
`X-GW-Signature`, `X-GW-Event`), cookies `gw_session` and `gw_oidc`, Prometheus metrics `gw_*` and
`gateway_*`, the ClickHouse `gateway_requests` table, every env var, KV key prefixes and stored
formats, API paths, the compose volume names and Postgres credentials, and the key-derivation labels
`agent-gateway/session/v1` and `agent-gateway/secret/v1` (changing them would sign everyone out and
make sealed provider keys unreadable). An existing data dir therefore works with no migration
(docs/DEPLOY.md "Upgrading from agent-gateway"). The workspace folders are still named
`agent-gateway`; that is a path, not the product name. If wrong: the kept identifiers can move to
new names only with a dual-read period (accept both, emit the new one) and a release note.

**D25 Key management, key monitoring, alert channels.** Owner request (gpt-load style key list).
Design in docs/KEYS.md: per-key status (available / cooldown / quarantined / disabled), per-key
hourly stats on the AddAgg pipeline, bulk add/test/enable APIs, and alert channels through
`github.com/nicholas-fedor/shoutrrr` (one URL per channel: Telegram, Discord, Slack, Teams, email,
ntfy, …) instead of hand-written integrations. Cooldown only stops sending to a failing key; it is
not used to rotate around provider limits. Implementation ruling: the gateway builds shoutrrr
services directly (not its router, which imports XMPP/MQTT clients); `smtp` and `generic` are behind
`-tags alertsfull` because their `text/template` use costs +16 MB of binary (CONFIG.md).

**D26 Guardrail policies and provider privacy (OpenRouter W1-A).** Owner request: OpenRouter-style
policies. `store.Policy` (KV `policy/<org>/<id>`, written with the config version bump) applies to a
key as org default (minus opted-out members/keys), via its creator (`APIKey.CreatedBy` in
`member_ids`) or via `key_ids`. Combination follows OpenRouter: model and provider allowlists
intersect (every list must admit), `require_zdr` / `deny_data_collection` OR, filters union with
block beating redact, budgets checked one by one. Budgets are per person and per key: a policy
reaching the key as org default or by member limits the creator's spend over all their keys; by key
it limits that key (creatorless keys fall back to per key). Spend goes to `spend/pol/<org>/<u|k>/<id>/<period>/<a|p>`
counters on the AddAgg pipeline (period `dYYYYMMDD`, `wYYYYMMDD` of the Monday, `mYYYYMM`, `l`, UTC;
`/p` = platform models only, `/a` = with BYOK), read with `Pipeline.Spent` in admit (D7 delta, no DB
wait on a warm cache). Shortcut: counters are written only for keys under a budget, so a new budget
counts from when it applies. The effective policy is compiled at authentication and cached in the
principal (auth cache, cleared by `InvalidateAuth` on policy writes and by every catalog reload).
Providers carry admin-declared `privacy {zdr, trains_on_data, retention_days, region}`; undeclared
privacy fails ZDR and data-collection rules (fail closed). Routing seam: `EffectivePolicy.AllowTarget(p)`
filters planned targets in `resolve` (one call site, `policyTargets`); request-level `provider`
filters compose by also having to pass; if none is left the request gets 403
`policy_no_eligible_provider` instead of falling back. Refusals are 403 with `error.policy`.
`allowed_regions` is stored for W2 data regions. Org guardrails and key limits stay and apply on top.
If wrong: per-org policy counts beyond ~100 or per-request compile cost would move compilation into
the catalog snapshot; budgets that must count spend before assignment need counters for every key.

**D27 OpenRouter-compatible request routing (W1-B).** Requests on chat completions, Responses and
completions accept OpenRouter's `models`, `provider`, `:nitro` / `:floor`, `@preset/<slug>`,
`usage.include` and `user` (docs/API.md "Request routing"), in `internal/gateway/reqroute.go`.
Rulings: (1) `models` fallbacks become one Bifrost fallback chain (each model's targets planned by
its own strategy, then the next model's), not a loop of upstream calls: one call, streams fail over
before the first byte as today, and the served target's `Via` names the model to bill
(`store.Target.Via`, `json:"-"`, never stored); per-model health and breakers stay correct
(`ownerOf`). Upstream error text is withheld when any model in the chain is org-private. (2) The
`provider` object is strict: unknown fields and bad values are 400 `invalid_routing`, so a typo in
`ignore` or `zdr` never routes somewhere the caller excluded. (3) Filters always apply; `order`
then `sort` replace the strategy's order; open circuits stay last; `allow_fallbacks: false` keeps
one target per model. (4) Throughput is a new per-node EWMA of whole-request output tokens/s
(`targetStats.tps`); percentile objects use `p50` only; `quantizations` is accepted and ignored and
`require_parameters` checks declared modalities only — no metadata exists for the rest.
(5) Privacy filters read the policy engine's `Provider.Privacy *ProviderPrivacy{ZDR, TrainsOnData,
RetentionDays, Region}` (D26; one type); `data_collection: deny` drops `trains_on_data` or
`retention_days > 0` unless ZDR, and undeclared privacy fails both request filters as it fails the
policy rules. Order: policy filter (`resolve`), then request filters, then ordering; a request can
only tighten its key's policy.
(6) Presets are org-scoped KV records in the catalog snapshot (`preset/<org>/<slug>`, version
bump, ≤ 200 per org); request `models` / `provider` replace the preset's, params fill only unset
fields. (7) `usage.cost` / `cost_micros` are added only with `usage: {"include": true}` (the parity
doc's semantics), so existing clients see unchanged bodies; cost equals the billed amount.
(8) `user` is stored as `RequestLog.end_user` (128 bytes) and rolled up per day under
`enduser/<day>/<org>/<user>`, a prefix separate from `agg/` so the normal usage report never scans
it and log retention prunes it by range; ClickHouse gains an `end_user` column (`ADD COLUMN IF NOT
EXISTS`). It is also the cache-affinity key when the request has no prompt key or prefix.
If wrong: a per-model upstream loop would allow different request params per model (presets per
fallback); end-user rollups may need a cap per org if tenants send unbounded distinct ids.

**D28 Key visibility, rotation, management tokens, exports, content logging (W1-C).** OpenRouter
enterprise parity (docs/OPENROUTER_ENTERPRISE.md). Rulings: (1) members and viewers list only keys
they created; admins/owners all; rotate/revoke of others' keys is admin-only. (2) Rotation keeps the
key id, so limits, allowlists, KBs, spend counters and anything assigned by key id stay; the old
hash stays indexed for an optional grace (≤ 24 h) and `KeyByHash` caps its `expires_at` at the grace
end, which reuses the gateway's expiry check with no gateway change. (3) Management tokens are a
separate entity (`mgmt/`, `mhash/`), not API keys with a flag: different prefix (`pr-mgmt-`),
different surface (org admin API only), so a leak of one kind never opens the other. They act with
min(token role, creator's current role); they cannot manage tokens; CSRF is skipped for bearer auth
only. (4) Exports stream CSV from the existing daily rollups and day-partitioned logs (no new
indexes); creator grouping resolves `APIKey.CreatedBy` at export time. (5) Content logging is an
org setting (admin), stored sealed under `logc/<org>/<day>/<log id>` (org first, so each org's
retention is one range delete), with a `logcorg/` index the purge job walks instead of all orgs;
capture is a per-request copy of the principal (`withContent`) plus one call in `record`, for
`/v1/chat/completions` only (streams store the assembled delta text). If wrong: other endpoints
add the same `withContent` line; a per-member visibility exception would be a policy field (agent A's
policy engine).

**D29 Enterprise identity: verified domains, SAML SSO, enforced SSO, SCIM (W2-E).** OpenRouter
enterprise parity (docs/OPENROUTER_ENTERPRISE.md). Rulings: (1) Libraries over protocol code
(ponytail): SAML on `github.com/crewjam/saml` v0.6.3 (signatures, conditions, InResponseTo, replay
cache hook; deps etree, goxmldsig, clockwork, all on GitHub); SCIM on `github.com/elimity-com/scim`
pinned to master 2641426 (2026-07-28; no recent tags but actively maintained, ~5 kLOC, deps
scim2/filter-parser, q-uint/parser, q-uint/xsd-datetime) for routing, schema and PATCH validation,
filters, paging and the discovery endpoints; we write only the Users/Groups handlers. Binary
+2.0 MB (69.8 → 71.8 MB unstripped). (2) The trust anchor is the verified domain: an org's IdP (SAML
or SCIM) may only sign in, create or link accounts whose email is on a domain that org verified by
DNS TXT; one verified claim per domain across orgs; operators never sign in through a tenant IdP.
(3) Everything per org lives under `ident/<org>/` (config, sealed SP key bundle, SCIM token hash,
SCIM users, SCIM groups, domains) and is removed with the org; domain claims are `vdomain/<name>`.
(4) The SP key is created when an owner first opens the SSO settings, so the IdP can be set up
from SP metadata first. IdP metadata URLs are fetched once at save (shortcut: no periodic refresh;
re-save after an IdP certificate rotation). (5) Enforce blocks password sign-in and sign-up for the
org's verified domains; operator password sign-in is the break-glass. (6) SCIM users are
memberships, not accounts: deprovision = remove membership + revoke the user's keys and management
tokens in that org (OpenRouter behaviour), with a tombstone so SAML JIT cannot undo it;
reactivation does not restore credentials. (7) One group → role mapping list serves SAML `groups`
values and SCIM groups (highest role wins, owners untouched); `workspace_id` is stored for W3.
If wrong: periodic metadata refresh is a ticker over orgs with a URL; subdomain matching would make
`DomainOrg` walk parent labels; a cross-node SCIM lock would be a KV lease (today a per-node mutex).

**D30 Data regions and trace export (W2-F).** OpenRouter "data regions" and "Broadcast" parity.
Rulings: (1) Regions are the provider's declared `privacy.region`, normalized to `global | europe |
us | <other>` (`store.NormalizeRegion`: eu, eu-*, europe-* → europe; us, usa, us-* → us; empty →
global) on save and again at compare time (older rows). Policy `allowed_regions` lists intersect
like the other allowlists; `global` passes only when listed. Regional *endpoints* are a deployment:
`REGION_LOCK=<region>` applies the same filter to every request of that node (run
`eu.example.com` with `REGION_LOCK=europe`); no request-level region field, as OpenRouter uses
hostnames. The 403 stays `policy_no_eligible_provider` with `policy: "REGION_LOCK"`.
(2) Trace export is hand-rolled OTLP/HTTP **JSON**, not the OpenTelemetry Go SDK. Measured
(go1.27.3, `-ldflags=-s -w`): a minimal program with `otlptracehttp` + `sdk/trace` v1.40.0 is
14.26 MB and links 180 non-stdlib packages (gRPC, grpc-gateway, genproto, protobuf, logr, backoff)
needing ~10 new `replace` lines for the GitHub mirrors; the same program with `net/http` +
`encoding/json` is 6.70 MB — about +7.5 MB on a 49.2 MB binary. The SDK also models one process-wide
TracerProvider per exporter, while we need per-tenant endpoints, headers, sampling, key filters, a
shared bounded buffer with per-org shares, auto-disable and an SSRF-pinned client — all of which we
would write around it anyway. OTLP/HTTP JSON is a stable public protocol and the encoder is ~150
lines (`internal/broadcast/span.go`, golden-tested). If wrong: protobuf encoding (smaller payloads)
can be added with the already-linked `google.golang.org/protobuf` and the OTLP proto package.
(3) Langfuse goes through its OTLP endpoint (`/api/public/otel/v1/traces`, Basic auth of the key
pair), not its ingestion API: one encoder, and Langfuse maps `gen_ai.*` attributes itself. S3/NDJSON
is not built (OTLP covers the backends asked for). (4) Spans are best effort: in-memory per-export
buffers, batches of ≤ 100 or every 2 s, one batch in flight per export, 3 retries on network/429/5xx,
auto-disable after 10 failed batches (webhook M11 pattern), bounded by `TRACE_QUEUE` with a tenth per
org. Sampling hashes the request id, so exports with the same rate keep the same requests. The trace
id is derived from the request log id and the span id is that id, linking spans to request logs.
(5) Exports are KV records without a config version bump; each node reloads them every 10 s (and at
once after an admin write on that node), and the request path asks an atomic set whether the org or
platform has any export before building a span. If wrong: a node may send for up to 10 s after an
export is deleted elsewhere.
