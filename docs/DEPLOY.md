# Deploying

## Small mode: one VPS (1 vCPU / 2 GB is enough)

Plain binary with systemd:

```sh
make build                                   # on a machine with Go 1.27+
scp bin/gateway server:/usr/local/bin/agent-gateway
# on the server
sudo useradd --system --no-create-home gateway
sudo cp deploy/agent-gateway.service /etc/systemd/system/
sudo install -m 600 /dev/null /etc/agent-gateway.env   # fill it from deploy/.env.example
sudo systemctl enable --now agent-gateway
```

Or Docker:

```sh
cp deploy/.env.example deploy/.env   # set MASTER_KEY, ADMIN_EMAIL, ADMIN_PASSWORD, PUBLIC_URL
docker compose -f deploy/docker-compose.yml up -d
```

First start: the gateway creates the operator from `ADMIN_EMAIL` / `ADMIN_PASSWORD` and loads the
built-in model catalog (`SEED_CATALOG`, on by default): every vendor provider (OpenAI, Anthropic,
Google, Mistral, Groq, …) is added **disabled and without keys**, with its models and prices. Sign in,
open Platform → Providers, edit a provider, paste its API key and enable it; its models appear in
the catalog and the API within 2 seconds. Installs that already have providers keep them as they are
and can add the catalog with Platform → Models → Load model catalog.

Put TLS in front with any reverse proxy (Caddy: `gateway.example.com { reverse_proxy 127.0.0.1:8080 }`)
and set `PUBLIC_URL=https://gateway.example.com`. Streaming needs the proxy not to buffer responses;
the gateway sends `X-Accel-Buffering: no` for nginx.

Data lives in `DATA_DIR` (Pebble). Back it up by stopping the service and copying the directory, or
by filesystem snapshot. Keep `MASTER_KEY` with the backup: without it, provider keys cannot be read.

## Scale mode: several nodes

```sh
docker compose -f deploy/docker-compose.scale.yml up -d --scale gateway=3
```

- `STORE=postgres`: control plane, usage counters and request logs in Postgres. Every node reads the
  config version every 2 s, so changes reach all nodes within about 2 s.
- `REDIS_URL`: RPM/TPM windows shared by all nodes. Without it, each node enforces the full limit.
- `CLICKHOUSE_URL` (optional): raw request logs for heavy analytics (`gateway_requests` table).
- Credit and budget checks use each node's own unflushed spend plus the stored total, refreshed every
  second, so an org can overspend by about one second of traffic per node (docs/DECISIONS.md D7).

The nodes are stateless; put any HTTP load balancer in front. Use the same `MASTER_KEY` everywhere.

## Hardening checklist

- Serve over HTTPS and set `PUBLIC_URL` to the https URL (Secure cookies).
- Set `METRICS_TOKEN`, or keep `/metrics` off the public listener at the proxy.
- If org admins may add private providers, block egress from the gateway to internal ranges
  (cloud metadata `169.254.169.254`, RFC 1918, 100.64/10) with the host firewall. The gateway checks
  provider URLs itself, but the upstream HTTP client resolves DNS on its own (docs/SECURITY.md H1).
- Keep `ALLOW_SIGNUP=false` unless you run a public service; sign-up does not verify email.
- Rotate `ADMIN_PASSWORD` after first start by changing it in the console (Account).

## Running behind a corporate proxy

When upstream APIs are reachable only through an egress proxy, set it once for every provider:

```sh
UPSTREAM_PROXY_URL=http://user:pass@proxy.corp:3128   # or https://..., socks5://...
UPSTREAM_NO_PROXY=localhost,.corp.example              # optional: hosts to reach directly
UPSTREAM_PROXY_CA_FILE=/etc/ssl/corp-root.pem          # optional: TLS-inspecting proxy CA
```

The gateway refuses to start on a malformed value. Only model calls go through it (Bifrost provider
clients); webhooks, SSO and knowledge-base URL fetches do not. A provider can use its own proxy or
opt out (`no_proxy`) in Platform → Providers → Proxy. Through a proxy, the proxy decides which
addresses are reachable: the gateway still checks org provider URLs before each request, but the
connect-time private-address block applies only to direct connections, so configure the proxy to
deny internal ranges if org admins can add providers (docs/SECURITY.md "Outbound proxy").

## Upgrading Bifrost core

The gateway pins `github.com/maximhq/bifrost/core v1.15.2`, which needs Go 1.27 (the Dockerfile
builds on `golang:1.27-alpine`; docs/DECISIONS.md D1). To move to a newer core, raise the Go version
in `go.mod` and the Dockerfile if core requires it, bump the core version (keep the
`github.com/mark3labs/mcp-go` replace in sync with core's own `go.mod`), and run `make check`;
provider code is only touched in `internal/gateway`.

Upgrading a running deployment from a v1.2.38 build:

- MCP server names must be ASCII without spaces or hyphens and must not start with a digit
  (Bifrost refuses others; the admin API now returns 400). Rename such servers before or right
  after the upgrade, otherwise they stay disconnected (logged as `mcp connect`). Update the org and
  key `mcp_servers` grants to the new names.
- Models see MCP tools as `<server>-<tool>`; `POST /v1/mcp/tool/execute` accepts that name or the
  bare tool name.
- Idle memory is about 55 MB (was about 28 MB); still well inside a 2 GB VPS.
