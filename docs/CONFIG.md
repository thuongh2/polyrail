# Configuration

All configuration is by environment variable. Only `MASTER_KEY` is required.

| Variable | Default | Meaning |
|---|---|---|
| `MASTER_KEY` | (required) | 16+ characters. Encrypts provider keys (AES-256-GCM) and signs sessions. Back it up; changing it makes stored provider keys unreadable and signs everyone out. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `PUBLIC_URL` | `http://localhost<port>` | External URL. `https://` turns on Secure cookies; used for the SSO callback. |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | – | On first start, creates this operator account (password 10+ characters) and a `Default` org. Ignored once the user exists. |
| `STORE` | `pebble` | `pebble` (embedded, single node) or `postgres` (shared, for several nodes). |
| `DATA_DIR` | `./data` | Pebble data directory. |
| `PEBBLE_CACHE_MB` | `32` | Pebble block cache. |
| `POSTGRES_URL` | – | With `STORE=postgres`, e.g. `postgres://user:pass@host:5432/db`. |
| `REDIS_URL` | – | Valkey/Redis for rate-limit windows shared across nodes. Without it limits are per node. |
| `CLICKHOUSE_URL` | – | ClickHouse HTTP URL (`http://user:pass@host:8123/?database=default`) to also ship raw request logs. |
| `ALLOW_SIGNUP` | `false` | Let anyone create an account (with their own org). Email is not verified. |
| `SIGNUP_PREPAID` | `false` | Orgs created by sign-up need credits before they can call models. |
| `ALLOW_PRIVATE_PROVIDER_URLS` | `false` | Let org admins point private providers at private/internal addresses. Operators can always. |
| `LOG_RETENTION_DAYS` | `14` | Request logs older than this are deleted (usage rollups are kept). |
| `RESPONSE_CACHE_MB` | `64` | Memory for the opt-in response cache. `0` disables it. |
| `MAX_BODY_MB` | `8` | Largest accepted API request body. |
| `PROVIDER_CONCURRENCY` | `64` | Concurrent upstream requests per provider (Bifrost workers). |
| `PROVIDER_BUFFER` | `256` | Queued requests per provider before callers wait. |
| `USAGE_QUEUE` | `10000` | Request-log rows buffered before rows are dropped (billing counters are never dropped). |
| `METRICS_TOKEN` | – | If set, `/metrics` requires `Authorization: Bearer <token>`. |
| `LOG_LEVEL` | `info` | `debug` for more. |
| `OIDC_ISSUER` | – | Enables SSO, e.g. `https://accounts.google.com`. Register `<PUBLIC_URL>/api/auth/oidc/callback` as the redirect URI. |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | – | OIDC client credentials. |
| `OIDC_ALLOWED_DOMAINS` | – | Comma-separated email domains allowed to sign in. Empty = any. |
| `OIDC_DEFAULT_ORG` | – | Org id new SSO users join. Requires `OIDC_ALLOWED_DOMAINS`. |
| `OIDC_DEFAULT_ROLE` | `member` | Their role: `viewer`, `member` or `admin`. |
| `OIDC_TRUST_UNVERIFIED_EMAIL` | `false` | Accept ID tokens without `email_verified` (some IdPs omit it). |

## Request headers

| Header | Effect |
|---|---|
| `Authorization: Bearer sk-gw-…` or `x-api-key` | API key. |
| `x-gateway-cache: <seconds>` | Serve an identical earlier non-stream request from cache (max 86400). Cache hits cost nothing. |
| `x-gateway-mcp-clients: name1,name2` or `*` | Inject tools from these MCP servers (only servers granted to your org). |

Response headers: `x-gateway-provider` (provider that served), `x-gateway-request-id`, `x-gateway-cache-hit`.
