# Decisions (ADR log)

Format: context → decision → cost if wrong.

**D1 Bifrost core v1.2.38.** Sandbox has Go 1.24.7 only; newer core needs Go 1.27; toolchain
download and building Go from source are blocked. → Pin core v1.2.38 (last release on Go ≤1.24),
use only its public API (`bifrost.Init`, `schemas.*`, `providers/openai` converters). → If wrong:
an upgrade touches `internal/gateway` only.

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

**D10 Models route only to providers of the same owner.** Public models → public providers, org
models → that org's providers. Prevents tenants re-pricing platform capacity (SECURITY H4).

**D11 Cost rounding.** Per-request cost is rounded to the nearest micro-USD (unbiased over many
requests); stored as int64 micro-USD.
