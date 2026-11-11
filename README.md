<p align="center"><img src="docs/assets/polyrail.svg" width="96" height="96" alt="Polyrail logo"></p>

# Polyrail

**One API for every model. Self-hosted, light, open.**

Polyrail is an open-source, self-hosted AI model gateway: one OpenAI-compatible API in front of many providers,
with per-model pricing, credits, routing and failover, API keys, rate limits, usage analytics,
multi-tenancy, RBAC, SSO, audit logs, guardrails, MCP tools and Prometheus metrics.
A self-hosted take on OpenRouter, built on [Bifrost core](https://github.com/maximhq/bifrost) for
the provider adapters.

It is one Go binary with an embedded database. It runs on a 1 vCPU / 2 GB VPS, and scales out to
stateless replicas on Postgres + Valkey + ClickHouse when you need it.

## Run it in one command (demo)

```sh
./scripts/run-local.sh
```

It starts Polyrail, which loads the built-in model catalog (generated from
[models.dev](https://models.dev)): every vendor provider is added disabled and without keys. Sign in
with the credentials it prints (kept in `.env.local`), open Platform → Providers, paste an API key
into a provider and enable it; its models then appear in the catalog and the API. Prebuilt binaries
for macOS and Linux are in `bin/` in the release package; without them the script builds with Go.
`DEMO=1` also starts a local mock provider with demo models, an API key and a few requests so the
dashboards have data; `PORT=9090` changes the port.

## Quick start

```sh
make build                       # builds bin/polyrail; or: docker build -t polyrail .
export MASTER_KEY=$(openssl rand -hex 32) ADMIN_EMAIL=you@example.com ADMIN_PASSWORD='choose-a-long-one'
./bin/polyrail                   # listens on :8080, stores data in ./data
```

Open `http://localhost:8080/app/`, sign in, then:

1. **Platform → Public providers**: the first start added every catalog provider, disabled. Edit one
   (for example `openai`), paste your key and enable it: its catalog models become available. You
   can also add any OpenAI-compatible endpoint such as vLLM or Ollama as a new provider.
2. **Platform → Public models**: adjust catalog models (prices, routes) or add your own with an id
   (`vendor/name`), prices per 1M tokens and one or more routing targets.
3. **API keys**: create a key and call the API:

```sh
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-gw-..." -H "Content-Type: application/json" \
  -d '{"model": "openai/gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}'
```

Any OpenAI SDK works by setting its base URL to `http://localhost:8080/v1`.
The public model catalog with prices is at `http://localhost:8080/`.

Coming from agent-gateway (the former name)? Data dirs, env vars, API paths, `X-GW-*` /
`x-gateway-*` headers, `sk-gw-` keys and metric names are unchanged; see
[docs/DEPLOY.md](docs/DEPLOY.md#upgrading-from-agent-gateway).

## Screens

Public: a model catalog with filters, a page per model with pricing, each provider's live health
(status, 24 h uptime, latency, throughput, 72-hour uptime bars) and API samples, a provider status
page and API docs. Console: activity charts, request logs, a streaming chat playground, API keys,
credits, members, guardrails and audit log; operators also manage providers, models and routing,
organizations, users and MCP servers. Built with shadcn/ui, light and dark.

## Features

| Area | What you get |
|---|---|
| API | `/v1/chat/completions` (streaming and not), `/v1/embeddings`, `/v1/models`, OpenAI error shapes |
| Providers | Providers from Bifrost core v1.15.2: OpenAI, Azure OpenAI, Anthropic, AWS Bedrock, Google Vertex AI, Gemini, Mistral, Groq, Cohere, Ollama, Cerebras, Perplexity, OpenRouter, Nebius, Parasail, SGLang, ElevenLabs, DeepSeek, xAI, Fireworks, Hugging Face, Replicate, vLLM, Databricks, MiniMax, BytePlus, Sarvam, Wafer, Typesafe, OpenCode, Runway, Runware, and any OpenAI-compatible URL; operators and org admins can discover and import a provider's models. Public (platform) or private (per-org, bring your own key) |
| Pricing | Per-model input/output price per 1M tokens; cost per request in micro-USD; org credit balances (prepaid) or unlimited |
| Routing | Several targets per model with weights; automatic failover to the next target; circuit breaker skips failing targets for 30 s |
| Cost control | Opt-in exact response cache (`x-gateway-cache: <seconds>`), org max-output-token cap, per-key monthly budgets |
| Keys and limits | `sk-gw-…` keys stored as SHA-256 hashes; per-key RPM, TPM, model allowlist, expiry, revocation |
| Analytics | Per-day, per-model and per-key requests, tokens, cost, errors, latency; request log (no prompt bodies stored) |
| Tenancy | Organizations, roles owner/admin/member/viewer, platform operators, audit log of every admin change |
| SSO | OIDC (Google, Entra ID, Okta, Keycloak, …) with domain allowlist and auto-join |
| Guardrails | Per org: max input characters, blocked regex patterns, email/phone masking |
| MCP | Register HTTP/SSE MCP servers; grant them per org; tools are injected on request and executed through the gateway |
| Ops | `/metrics` (Prometheus), `/healthz`, `/readyz`, JSON logs, graceful shutdown |

## Measured footprint

On the development machine, with `GOMAXPROCS=1` and a local mock provider (so the numbers show
gateway overhead, not model latency), Bifrost core v1.15.2 build: idle RSS about 59 MB, peak 69 MB
while serving 5,000 chat requests at concurrency 50, about 7,700 requests per second, p50 5.3 ms,
p99 18 ms, zero errors. The load generator shared the same CPU. Your numbers will differ;
`docs/PROGRESS.md` has the method.

## Documentation

- [docs/CONFIG.md](docs/CONFIG.md): every environment variable
- [docs/DEPLOY.md](docs/DEPLOY.md): VPS, Docker, scale-out, backups, hardening
- [docs/API.md](docs/API.md): gateway and admin endpoints
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): design and storage tiers
- [docs/SECURITY.md](docs/SECURITY.md): threat model and review findings
- [docs/TESTING.md](docs/TESTING.md): acceptance criteria to tests
- [docs/BIFROST_PARITY.md](docs/BIFROST_PARITY.md): which Bifrost features are covered and what is next

## Development

```sh
make check   # go vet + race-enabled tests
TEST_POSTGRES_URL=postgres://... TEST_REDIS_URL=redis://... make check   # also the scale-mode backends
```

This repository is built by an AI agent team (product owner, tech lead, developer, QA, security,
anti-slop reviewer). Their rules and memory are in [CLAUDE.md](CLAUDE.md) and `.claude/agents/`.

## License

Apache-2.0 (the same as Bifrost core).
