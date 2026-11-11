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
every strategy keeps the failover chain. Stats (in-flight, EWMA latency and error rate, alpha 0.2,
round-robin counters) are per node and in memory, keyed by `provider|upstream model`, so bounded by
the catalog; no cross-node sharing (each node balances its own traffic). In-flight is counted only
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
