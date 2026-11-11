# OpenRouter Enterprise parity

Researched 2026-11-11 from OpenRouter's docs (enterprise quickstart, organization management, SSO,
guardrails + policies, ZDR, provider logging, provider selection, routing). Their Zendesk
enterprise page could not be read, so SLA / support / invoicing terms are unverified and out of
scope (those are business terms, not software).

Status: **have** · **partial** · **gap** (with proposed wave) · **skip** (reason).

## Organization and access

| OpenRouter | Polyrail | Notes |
|---|---|---|
| Organizations, shared credit pool | have | orgs + prepaid credits |
| Roles admin / member | have | owner / admin / member / viewer |
| Members see only their own keys; admins see all | have | D28; members and viewers list only keys they created |
| Workspaces (keys, guardrails, BYOK, routing, presets, budgets, members scoped per workspace) | gap · W3 | largest item; an org would hold workspaces; today org ≈ one workspace |
| SSO (OIDC) | have | |
| SSO via SAML, domain verification (DNS TXT), enforce SSO | have | D29; per-org SAML (`crewjam/saml`), `_polyrail-verify` TXT claim (one org per domain), enforce with operator break-glass, `GET /api/auth/sso` discovery |
| SCIM provisioning, IdP groups → workspace + role, deprovision disables keys | have (role) · workspace W3 | D29; SCIM 2.0 Users/Groups (`elimity-com/scim`), group → role mappings (`workspace_id` stored for W3), deprovision removes membership and revokes keys + management tokens |
| Management API keys (programmatic key create / rotate / delete) | have | D28; `pr-mgmt-` org-scoped bearer tokens, role ≤ admin, docs/API.md "Management tokens" |
| Key rotation | have | D28; `POST …/keys/{id}/rotate` keeps settings, optional `grace_seconds` |

## Spend controls

| OpenRouter | Polyrail | Notes |
|---|---|---|
| Per-key credit limit | have | `monthly_budget_micros` |
| Budget `limit_usd` + `reset_interval` daily / weekly / monthly | have | policy `budget` with daily / weekly / monthly / lifetime (UTC) (D26) |
| Per-member budgets (not only per key) | have | policy budgets count the key creator's spend across their keys (D26) |
| Workspace budgets | gap · W3 | with workspaces |
| Include BYOK spend in budgets toggle | have | `budget.include_byok` (org-provider models) |
| Billing, invoices, payment methods | skip | business integration (Stripe) — later, optional plugin |

## Guardrails and policies

| OpenRouter | Polyrail | Notes |
|---|---|---|
| Allowed models | have | per key, and policy globs per org default / member / key |
| Allowed providers | have | policy `allowed_providers`, enforced on planned targets |
| Prompt-injection detection | have | `prompt_injection` warn / block |
| PII redact / block | have | `mask_pii` (redact); add block mode |
| Custom regex filters, redact or block | have | policy `filters` (RE2, redact / block); org `blocked_patterns` stay |
| Policy assignment: org default, per member, per key, opt-outs; intersect allowlists, OR privacy, union filters | have | `/api/orgs/{org}/policies`, effective policy per key (D26); existing org/key settings still apply |
| 403 + machine-readable reason | have | policy refusals: 403 `policy_*` codes with `error.policy`; older checks keep 400/402 |

## Data and privacy

| OpenRouter | Polyrail | Notes |
|---|---|---|
| ZDR enforcement (account / guardrail / request `provider.zdr`, OR-ed) | have | provider `privacy` metadata; policy `require_zdr` filters targets (D26); request `provider.zdr` filters on top (D27) — both apply |
| `data_collection: deny` | have | policy `deny_data_collection` (D26) and request `provider.data_collection` (D27), composed |
| Data regions (`allowed_data_regions`: global / europe / us), regional endpoints | have | D30; provider `privacy.region` normalized to global / europe / us / other, policy `allowed_regions`, `REGION_LOCK` for a regional deployment (eu.example.com); no request-level field (OpenRouter uses hostnames) |
| Prompt / response logging off by default, opt-in per workspace | have | D28; per org (no workspaces yet), AES-GCM sealed, 1–90 day TTL, chat completions only |
| BYOK with declared ZDR | have | org providers take the same `privacy` declaration |

## Routing (request level)

| OpenRouter | Polyrail | Notes |
|---|---|---|
| `models: [...]` fallback array | have | one failover chain across models, billed on the model used (D27) |
| `provider` object: `order`, `only`, `ignore`, `allow_fallbacks`, `require_parameters`, `sort` (price / throughput / latency), `max_price`, `preferred_min_throughput`, `preferred_max_latency`, `quantizations`, `data_collection`, `zdr` | have | every field; `quantizations` ignored and `require_parameters` modalities-only (no metadata), percentiles use p50 (D27) |
| `:nitro` / `:floor` shortcuts | have | `sort: throughput` / `price` |
| Presets `@preset/<slug>` (saved model + params + routing) | have | org-scoped, `/api/orgs/{org}/presets` |
| Auto router (`openrouter/auto`) | gap · later | needs a classifier model; can reuse routing scores |
| Usage accounting in body (`usage.cost`, cached + reasoning tokens) | have | with `usage: {include: true}`, streams too |
| `user` field for per-end-user analytics + sticky routing | have | `end_user` on logs, `usage?group=end_user`, cache-affinity key |

## Observability

| OpenRouter | Polyrail | Notes |
|---|---|---|
| Activity export CSV, group by model / key / creator | have | D28; `usage/export.csv` (model / key / creator / day), `logs/export.csv` |
| Broadcast to OTel, Langfuse, Datadog, S3, … with sampling and key filters | have | D30; org + platform trace exports: OTLP/HTTP JSON GenAI spans (Tempo, Jaeger, Datadog Agent, Grafana Cloud, …) and Langfuse via its OTLP endpoint; sampling, key filter, opt-in content; S3/NDJSON not built |
| Prometheus | have | OpenRouter has none |

## Proposed waves
- **W1 (policies + routing, core enterprise):** policy engine (org default / member / key; budgets with intervals, per-member budgets, allowed models + providers, filters redact/block, ZDR + data collection), provider privacy metadata, request `models` + `provider` routing + `:nitro`/`:floor`, presets, `usage.cost` in body, `user` analytics, members-see-own-keys, management API tokens, CSV export, opt-in content logging.
- **W2 (identity + compliance):** SAML SSO, domain verification, SSO enforcement, SCIM, data regions, OTel/Langfuse broadcast.
- **W3 (workspaces):** workspaces inside orgs scoping keys, providers (BYOK), guardrails, presets, budgets, members.
