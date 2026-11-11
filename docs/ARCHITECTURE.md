# Architecture — agent-gateway

Owner: Tech Lead agent. Decisions referenced as D-n live in docs/DECISIONS.md.

## Shape

One Go binary. No microservices.

```
client ──HTTP──▶ net/http mux
                  ├─ /v1/*          gateway: auth → limits → guardrails → cache → router → Bifrost core ─▶ providers
                  ├─ /api/admin/*   admin API: session/OIDC auth → RBAC → store (+ audit)
                  ├─ /api/public/*  catalog (no auth)
                  ├─ /metrics /healthz /readyz
                  └─ /              embedded web UI (static HTML/JS)
                         │
            ┌────────────┴─────────────┐
            ▼                          ▼
   store.KV (control plane,     usage pipeline (async, batched)
   logs, rollup counters)        └─▶ KV rollups + request logs
     ├─ Pebble (small mode)      └─▶ ClickHouse raw events (optional)
     └─ Postgres (scale mode)
   limits.Limiter
     ├─ in-process (small mode)
     └─ Valkey/Redis (scale mode)
```

## Storage: one KV seam, two backends (D3, D6)

All control-plane entities are JSON values in an ordered key-value space. Entity code is written
once against `store.KV`; backends only implement the KV primitives:

| primitive | Pebble | Postgres |
|---|---|---|
| Get / Put / Delete | point ops | `kv(k text pk, v bytea)` |
| Scan(prefix, after, limit) | iterator | `k >= $1 AND k < $2 ORDER BY k LIMIT` (B-tree) |
| Update(fn) atomic | mutex + indexed batch | SERIALIZABLE tx + retry |
| AddAgg(map[key]Agg) | single writer read-modify-write | `INSERT … ON CONFLICT DO UPDATE SET n = n + excluded.n` |
| DeleteRange | `DeleteRange` (cheap, LSM tombstone) | range `DELETE` |

Key layout (`/` separated, ids are random 16-hex):

```
org/<org>                     Org            user/<user>          User
idx/email/<email>             user id        mem/<org>/<user>     Membership (role)
umem/<user>/<org>             reverse index  key/<org>/<key>      APIKey (no secret)
khash/<sha256>                org/key ids    prov/<provider>      Provider (keys encrypted)
model/<model id>              Model          audit/<org>/<inv ts> AuditEntry
mcp/<id>                      MCP server     log/<yyyymmdd>/<org>/<inv ts> RequestLog
agg/<org>/<yyyymmdd>/<dim>    Agg counters   spend/org/<org>, spend/key/<key>/<yyyymm>  Agg counters
health/<yyyymmddhh>/<model>|<provider>       Agg counters: public provider health per attempt (kept 30 d)
meta/version                  config version (bumped on every control-plane write)
```

Why this scales: lookups are point reads or bounded prefix scans; nothing scans all users or all
logs. Millions of orgs/keys are fine on both backends. Logs and rollups are partitioned by day so
retention is a `DeleteRange`.

## Hot path (D7)

Never waits on a synchronous DB write; reads are cached.

1. **Auth** — `sha256(key)` → bounded TTL cache (30 s, random eviction, negative results too) →
   `khash/*` + `key/*` + `org/*` on miss. Cleared on this node by key/org changes and on every config
   version change (other nodes ≤ 2 s).
2. **Limits** — RPM: fixed one-minute window counter. TPM: tokens counted per minute window, checked before the call,
   added after. Local in-process; Redis `INCRBY`+`EXPIRE` fixed windows in scale mode.
3. **Budget** — org balance = `credits` (org record) − `spend/org` counter. A node keeps a local
   spend cache, adds its own unflushed deltas, and refreshes from the store every flush (1 s).
   Max overspend ≈ one second of one node's traffic per org. Same rule for key monthly budgets.
4. **Guardrails** — lossless compression, max input chars, blocked regexes (compiled once per config
   version), prompt-injection tripwire (user text), credential and PII masking (D14).
5. **Cache** — exact match, opt-in by header, bounded TTL cache (entries + bytes, random eviction),
   key = sha256(org + raw request body). Skipped for streams and MCP requests.
6. **Router** — resolve model (org-private first, then public) from the in-memory catalog snapshot
   (rebuilt when `meta/version` changes; polled every 2 s across nodes). Pick primary target by
   weight among targets whose circuit is closed; remaining targets become Bifrost fallbacks.
7. **Bifrost core** executes the call (provider adapters, key weights, retries, fallbacks, streaming, MCP tools).
8. **Usage event** → bounded channel → batcher (1 s / 512 events) → `AddAgg` + request logs
   (+ ClickHouse). On a full channel the event is accounted in-memory (counters) and only the log row
   is dropped and counted in `/metrics` (billing is never dropped).

## Providers and Bifrost mapping

- Provider types: those configured by key + base URL alone (openai, anthropic, gemini, cohere,
  mistral, groq, ollama, cerebras, perplexity, openrouter, nebius, parasail, sgl). Azure, Vertex and
  Bedrock need cloud credentials Bifrost reads from extra key config and are not exposed in v1; use
  their OpenAI-compatible endpoints. Tenants may only create the multi-instance types below. Providers of base type openai/anthropic/gemini/
  cohere are registered as Bifrost *custom providers* keyed by provider id, so any number of
  instances (public or per-org private) can coexist. Other types (azure, vertex, groq, mistral, …)
  use the Bifrost standard key and may exist once (platform-wide) — or be added as `openai` type
  with a base URL, since they are OpenAI-compatible.
- `store` → `gateway.account` implements `schemas.Account` from the in-memory snapshot. On config
  change the gateway calls `bifrost.UpdateProvider` for changed providers.

## RBAC matrix

| action | viewer | member | admin | owner | operator |
|---|---|---|---|---|---|
| view usage, logs, models | ✓ | ✓ | ✓ | ✓ | ✓ |
| create/revoke own API keys |  | ✓ | ✓ | ✓ | ✓ |
| manage all org keys, private providers/models, guardrails |  |  | ✓ | ✓ | ✓ |
| view audit log |  |  | ✓ | ✓ | ✓ |
| manage members and roles |  |  | ✓ (not owners) | ✓ | ✓ |
| delete org, transfer ownership |  |  |  | ✓ | ✓ |
| public providers/models, credits, MCP servers, all orgs |  |  |  |  | ✓ |

## Resource budget (small mode, 1 vCPU / 2 GB)

Pebble block cache 32 MB + memtable 16 MB; auth cache 50k entries; response cache 64 MB; usage
channel 10k events; Bifrost per-provider concurrency 64 / buffer 256 (configurable).

## Scale mode

N stateless gateway nodes behind any load balancer, `STORE=postgres`, `REDIS_URL` for shared
limits, optional `CLICKHOUSE_URL` for raw events. Config changes propagate via `meta/version` polling.
