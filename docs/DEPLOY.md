# Deploying

## Small mode: one VPS (1 vCPU / 2 GB is enough)

Plain binary with systemd:

```sh
make build                                   # on a machine with Go 1.27+
scp bin/polyrail server:/usr/local/bin/polyrail
# on the server
sudo useradd --system --no-create-home polyrail
sudo cp deploy/polyrail.service /etc/systemd/system/
sudo install -m 600 /dev/null /etc/polyrail.env   # fill it from deploy/.env.example
sudo systemctl enable --now polyrail
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

Email (`smtp://`) and generic HTTP (`generic://`) alert channels are not in the default binary
(+16 MB): build with `make build TAGS=alertsfull` if you need them (docs/CONFIG.md "Build tags").
Telegram, Discord, Slack, Teams, Google Chat, Pushover, ntfy, Gotify, Matrix and Mattermost work in
every build.

Put TLS in front with any reverse proxy (Caddy: `gateway.example.com { reverse_proxy 127.0.0.1:8080 }`)
and set `PUBLIC_URL=https://gateway.example.com`. Streaming needs the proxy not to buffer responses;
the gateway sends `X-Accel-Buffering: no` for nginx.

Data lives in `DATA_DIR` (Pebble). Back it up by stopping the service and copying the directory, or
by filesystem snapshot. Keep `MASTER_KEY` with the backup: without it, provider keys cannot be read.

## Scale mode: several nodes

```sh
docker compose -f deploy/docker-compose.scale.yml up -d --scale polyrail=3
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

## Enterprise SSO and SCIM per organization (Okta, Entra ID)

Each org configures its own IdP in the console (owner role); the operator only needs `PUBLIC_URL`
set to the external https URL, because the SAML entity id, ACS URL and SCIM base URL are built from
it. API details: docs/API.md "Enterprise identity".

1. **Verify the email domain.** Org settings → Domains → add `example.com`, publish the TXT record
   `_polyrail-verify.example.com` = `polyrail-verification=<token>` at the DNS host, then Verify.
   SAML and SCIM only accept emails on verified domains of the org.
2. **Get the SP details.** Open the SSO settings (this creates the org's SAML signing key) and copy
   the ACS URL `<PUBLIC_URL>/api/sso/saml/<org>/acs` and entity id / metadata URL
   `<PUBLIC_URL>/api/sso/saml/<org>/metadata`.
3. **Okta:** Applications → Create App Integration → SAML 2.0. Single sign-on URL = ACS URL,
   Audience URI (SP Entity ID) = entity id, Name ID format EmailAddress, Application username Email.
   Attribute statements: `email` = `user.email`, `name` = `user.displayName`; group attribute
   statement `groups` (filter e.g. "Starts with Polyrail"). Copy the app's "Metadata URL" into the
   SSO settings and enable SAML.
   **Entra ID:** Enterprise applications → New → Create your own → Non-gallery; Single sign-on →
   SAML: Identifier (Entity ID) = entity id, Reply URL (ACS) = ACS URL. The default claims
   (`…/claims/emailaddress`, `…/claims/displayname`) are read without mapping; add a group claim if
   you map groups (Entra sends group object ids unless "Cloud-only group display names" or sAMAccountName is
   chosen, so map those values). Paste the "App Federation Metadata Url" into the SSO settings.
4. **Map groups to roles** (optional): Polyrail group name → viewer, member or admin. Without a
   mapping, new users get the SAML / SCIM default role.
5. **SCIM** (optional): create a SCIM token in the SSO settings (shown once).
   Okta: app → General → enable SCIM provisioning → Provisioning → SCIM connector base URL
   `<PUBLIC_URL>/scim/v2/<org>`, unique identifier field `userName`, actions Push New Users, Push
   Profile Updates, Push Groups; authentication HTTP Header (Bearer) = the token. Enable "Create
   Users", "Update User Attributes", "Deactivate Users".
   Entra: the app → Provisioning → Automatic; Tenant URL `<PUBLIC_URL>/scim/v2/<org>`, Secret Token =
   the token; Test Connection, then start provisioning. Deprovisioned users lose the org membership
   and their keys and management tokens in the org are revoked.
6. **Enforce SSO** once a test sign-in works: password sign-in is then refused for the verified
   domains, except for platform operators (break-glass). Keep one operator account with a strong
   password outside the IdP.

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

### Proxy pools

To spread egress over several proxies and survive one going down, create a pool in Platform →
Proxies (members, strategy, fail-closed) and either pick it in a platform provider's Proxy section or
make it the default for every provider:

```sh
UPSTREAM_PROXY_POOL=egress-eu        # pool name or id; wins over UPSTREAM_PROXY_URL
PROXY_HEALTH_INTERVAL=10m            # TCP probe of each member (default)
```

The gateway runs a CONNECT forwarder on `127.0.0.1` (random port, per-process credentials) that
providers on a pool use as their proxy; it picks a member for each new upstream connection, so with
keep-alive many requests share one member. A member that cannot be reached is set aside (60 s,
doubling to 10 min) and the next one is tried (at most 3 per connection); three failures in a row
mark it down until a probe reaches it. With every member unusable a fail-closed pool refuses the
call (Bifrost moves on to the model's next target); a fail-open pool connects directly. Provider
errors (429, 5xx) travel inside the tunnel and never change the selection. Watch
`gw_proxy_dials_total{pool,result}` (`ok`, `fail`, `refused`, `direct`) and
`gw_proxy_member_up{pool,member}`; the logs say `proxy member set aside`, `proxy member down` and
`proxy pool exhausted`. Health, cursors and sticky picks are per node. Pools tunnel with CONNECT only:
every Bifrost model client tunnels `http://` targets too, but a provider whose SDK uses net/http
(Bedrock) must use an `https://` endpoint. Through a pool, org providers' targets are checked public
and pinned to the checked address by the gateway itself.

## Upgrading from agent-gateway

Polyrail was called agent-gateway until 2026-11-11 (docs/DECISIONS.md D24). The data format, env vars,
headers, cookies, metric names and API paths are unchanged, so an existing `DATA_DIR`, Postgres
database, `.env` and `MASTER_KEY` work as they are; only the binary, image and unit names changed.

- systemd: either keep the old unit and replace `/usr/local/bin/agent-gateway` with the new binary,
  or install `deploy/polyrail.service` and point it at the old data with
  `Environment=DATA_DIR=/var/lib/agent-gateway` and `EnvironmentFile=/etc/agent-gateway.env`
  (or move both to the new paths while the service is stopped).
- Docker Compose: the service is now `polyrail` (image `polyrail:latest`); the volume names
  (`gateway-data`, `pg-data`, `ch-data`) and the Postgres credentials are unchanged, so
  `docker compose up -d --remove-orphans` attaches the existing data.
- Sessions and stored provider keys stay valid: the key-derivation labels kept their old values.

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
