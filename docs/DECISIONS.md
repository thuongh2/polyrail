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

**D14 Webhooks: in-process queue, signed, SSRF-guarded.** New package `internal/webhook`. Events go
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

**D15 Semantic cache: in-memory, per node, brute-force cosine.** Opt-in per org
(`Org.SemanticCache{model, threshold, ttl_seconds}`), served in `gateway/semcache.go`. Only
deterministic chat requests qualify (temperature explicitly 0, no tools/MCP/json_schema, non-stream).
The embedding is computed by calling Bifrost's `EmbeddingRequest` with the org's embedding model on
the same resolve/settle/record path as `/v1/embeddings` (no HTTP self-call), billed to the calling
key; the key's model allowlist is skipped because the model is an org setting. Vectors are normalized
float32; lookup is a linear dot-product scan of the org's ≤ 1,000 entries (≈ 1.5 M multiply-adds at
1,536 dims, under a per-org lock), LRU eviction inside the org and a global byte budget shared with
`RESPONSE_CACHE_MB`. No vector DB or ANN index: the ponytail ladder stops at a slice; revisit if
orgs need more than ~10k entries or cross-node sharing. Fail open on any embedding error.
