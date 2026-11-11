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

**D14 Idempotency keys in node memory; cost as a response header.** `Idempotency-Key` (OmniRoute
parity) wraps the non-stream JSON POST handlers (`Gateway.Idempotent`, applied at route
registration): scope = API key id + method + path + key, body compared by SHA-256, only 2xx stored
(24 h, bounded `ttlCache`), replays are not billed, 422 on body mismatch, 409 while in flight.
Streams ignore it (a replay cannot reproduce timing, and the response is not buffered). Storage is
per node; a multi-node deployment needs Redis to make retries exact across nodes (shortcut noted in
code). `X-GW-Cost-Micros` is the value `record()` billed, set before the body is written; streams
cannot carry it because headers go out before usage arrives (a trailer would be invisible to most
SDKs). Model discovery `POST /v1/models/query` reuses `Snapshot.Visible` and the key's model list,
so it can never show more than `GET /v1/models`; prices in the query are USD per 1M as numbers.
