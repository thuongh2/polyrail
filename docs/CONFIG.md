# Configuration

All configuration is by environment variable. Only `MASTER_KEY` is required.

| Variable | Default | Meaning |
|---|---|---|
| `MASTER_KEY` | (required) | 16+ characters. Encrypts provider keys (AES-256-GCM) and signs sessions. Back it up; changing it makes stored provider keys unreadable and signs everyone out. |
| `LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `PUBLIC_URL` | `http://localhost<port>` | External URL. `https://` turns on Secure cookies; used for the SSO callback. |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | – | On first start, creates this operator account (password 10+ characters) and a `Default` org. Ignored once the user exists. |
| `SEED_CATALOG` | on | On a store with no providers at all (first start), loads the embedded model catalog: disabled keyless providers `cat-<type>` and their models, hidden until a provider is enabled (docs/API.md "Model catalog"). `0` turns it off. |
| `STORE` | `pebble` | `pebble` (embedded, single node) or `postgres` (shared, for several nodes). |
| `DATA_DIR` | `./data` | Pebble data directory. |
| `PEBBLE_CACHE_MB` | `32` | Pebble block cache. |
| `POSTGRES_URL` | – | With `STORE=postgres`, e.g. `postgres://user:pass@host:5432/db`. |
| `REDIS_URL` | – | Valkey/Redis for rate-limit windows shared across nodes. Without it limits are per node. |
| `CLICKHOUSE_URL` | – | ClickHouse HTTP URL (`http://user:pass@host:8123/?database=default`) to also ship raw request logs. |
| `ALLOW_SIGNUP` | `false` | Let anyone create an account (with their own org). Email is not verified. |
| `SIGNUP_PREPAID` | `false` | Orgs created by sign-up need credits before they can call models. |
| `ALLOW_PRIVATE_PROVIDER_URLS` | `false` | Let org admins point private providers at private/internal addresses. Operators can always. |
| `UPSTREAM_PROXY_URL` | – | Global outbound proxy for every upstream model call: `http://`, `https://` or `socks5://`, credentials allowed (`http://user:pass@proxy:3128`). Providers with their own `proxy` or `no_proxy` skip it. A malformed value stops startup. |
| `UPSTREAM_NO_PROXY` | – | Comma-separated hosts that bypass the global proxy (`.corp.example` = domain and subdomains, `*` = all). Bypassed hosts keep the normal private-network dial checks. |
| `UPSTREAM_PROXY_CA_FILE` | – | PEM file of CA certificates to trust for the global proxy and for upstream TLS through it (TLS-inspecting proxies). Needs `UPSTREAM_PROXY_URL`. |
| `UPSTREAM_PROXY_POOL` | – | Default proxy pool (id or name, Platform → Proxies) for every provider without its own proxy or pool; wins over `UPSTREAM_PROXY_URL`. `UPSTREAM_NO_PROXY` applies to it. A name that matches no pool fails closed (calls refused) until the pool exists. |
| `PROXY_HEALTH_INTERVAL` | `10m` | How often each enabled pool member gets a TCP probe (2 s timeout). `0` turns probes off; a down member then comes back only via a pool test. |
| `ALLOW_TENANT_PROXY` | `false` | Let org admins set a proxy on their own providers. The proxy host must be public unless `ALLOW_PRIVATE_PROVIDER_URLS`. |
| `LOG_RETENTION_DAYS` | `14` | Request logs older than this are deleted (usage rollups are kept). Public provider health counters and per-key hourly stats (`keystat/`) are kept 30 days (fixed); stats of deleted keys and providers expire with them. |
| `RESPONSE_CACHE_MB` | `64` | Memory for the opt-in response cache. `0` disables it. |
| `MAX_BODY_MB` | `8` | Largest accepted API request body. |
| `PROVIDER_CONCURRENCY` | `64` | Concurrent upstream requests per provider (Bifrost workers). |
| `PROVIDER_BUFFER` | `256` | Queued requests per provider before callers wait. |
| `WEBHOOK_QUEUE` | `1000` | Webhook events buffered before new events are dropped (`gateway_webhook_deliveries_total{result="dropped"}`). |
| `ALERT_QUEUE` | `1000` | Alert channel events buffered before new events are dropped (`gateway_alert_sends_total{result="dropped"}`); 2 sender goroutines. |
| `TRACE_QUEUE` | `10000` | Trace export spans buffered across all exports (one org's exports may hold a tenth, at least 1000) before new spans are dropped (`gateway_trace_spans_total{result="dropped"}`); 2 sender goroutines (docs/API.md "Trace exports"). |
| `REGION_LOCK` | – | Route every request only to providers whose `privacy.region` is this data region (`europe`, `us`, or another region name; `eu` → `europe`). Providers without a region count as `global` and are skipped. No eligible provider → 403 `policy_no_eligible_provider` with `policy: "REGION_LOCK"`. For a regional deployment (docs/DEPLOY.md). `global` is refused at startup. |
| `KB_WORKERS` | `1` | Knowledge-base ingestion goroutines per node (chunk + embed queued documents). |
| `KB_INDEX_MB` | `256` | Memory for in-memory KB search indexes per node (LRU across KBs; ~1.6 KB per 512-dim chunk). |
| `KB_MAX_ORG_CHUNKS` | `300000` | Chunks one org may store across its knowledge bases. |
| `KB_MAX_DOC_MB` | `10` | Largest knowledge-base document upload. |
| `USAGE_QUEUE` | `10000` | Request-log rows buffered before rows are dropped (billing counters are never dropped). |
| `METRICS_TOKEN` | – | If set, `/metrics` requires `Authorization: Bearer <token>`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. `debug` adds one line per HTTP request (method, path without query string, status, bytes, ms, client; never headers or bodies) and Bifrost core's debug log. |
| `OIDC_ISSUER` | – | Enables SSO, e.g. `https://accounts.google.com`. Register `<PUBLIC_URL>/api/auth/oidc/callback` as the redirect URI. |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | – | OIDC client credentials. |
| `OIDC_ALLOWED_DOMAINS` | – | Comma-separated email domains allowed to sign in. Empty = any. |
| `OIDC_DEFAULT_ORG` | – | Org id new SSO users join. Requires `OIDC_ALLOWED_DOMAINS`. |
| `OIDC_DEFAULT_ROLE` | `member` | Their role: `viewer`, `member` or `admin`. |
| `OIDC_TRUST_UNVERIFIED_EMAIL` | `false` | Accept ID tokens without `email_verified` (some IdPs omit it). |

## Request headers

| Header | Effect |
|---|---|
| `Authorization: Bearer sk-gw-…` or `x-api-key` | API key (`x-api-key` is what the Anthropic SDKs send). |
| `x-goog-api-key` or `?key=` | API key on `/genai/*` (what the Google GenAI SDKs send). |
| `anthropic-version`, `anthropic-beta` | Accepted (and allowed by CORS) on `/anthropic/v1/*`, ignored. |
| `x-gateway-cache: <seconds>` | Serve an identical earlier non-stream request of the same org and workspace from cache (max 86400). Cache hits cost nothing. |
| `X-GW-KB: vs_a,vs_b` or `off` | Ground the request on these knowledge bases (max 3) or turn off the key's `kb_ids` (docs/API.md "Knowledge bases"). |
| `x-gateway-mcp-clients: name1,name2` or `*` | Inject tools from these MCP servers (only servers granted to your org and, if the key has `mcp_servers`, listed there). |

SDK base URLs: OpenAI SDKs use `https://<host>/v1` or the alias `https://<host>/openai/v1`;
Anthropic SDKs use `https://<host>/anthropic`; Google GenAI SDKs use `https://<host>/genai` as
`http_options.base_url` (Python `genai.Client(api_key="sk-gw-…", http_options={"base_url": "https://<host>/genai"})`,
JS `new GoogleGenAI({apiKey: "sk-gw-…", httpOptions: {baseUrl: "https://<host>/genai"}})`) and catalog
ids as model names (`model="vendor/name"`).

Response headers: `x-gateway-provider` (provider that served), `x-gateway-request-id`, `x-gateway-cache-hit`,
and on grounded requests `X-GW-KB` (KBs used, `empty` or `error`) and `X-GW-KB-Sources` (`doc#chunk` list).

## Build tags

| Tag | Effect |
|---|---|
| `alertsfull` | Adds the `smtp://` (email) and `generic://` (any HTTP endpoint) alert channel services: `go build -tags alertsfull ./cmd/polyrail`. Off by default: their payload templates use Go's `text/template`, which keeps the linker from dropping unused methods (measured on linux/amd64: stripped binary 48.9 → 65.1 MB, idle RSS about +5 MB). The other services (Telegram, Discord, Slack, Teams, Google Chat, Pushover, ntfy, Gotify, Matrix, Mattermost) add about 0.7 MB. `make build TAGS=alertsfull` (or `make dist TAGS=alertsfull`) builds it. `GET /api/auth/config` lists the services of the running build in `alert_services.platform` and those org channels may use in `alert_services.org`; the console's Alerts dialog greys out the rest ("needs a build with -tags alertsfull", "platform channels only"). |
