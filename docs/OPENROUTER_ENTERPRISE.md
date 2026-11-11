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
| Members see only their own keys; admins see all | gap · W1 | today any member lists every org key |
| Workspaces (keys, guardrails, BYOK, routing, presets, budgets, members scoped per workspace) | gap · W3 | largest item; an org would hold workspaces; today org ≈ one workspace |
| SSO (OIDC) | have | |
| SSO via SAML, domain verification (DNS TXT), enforce SSO | gap · W2 | SAML via `crewjam/saml`; domain claim + "SSO required" |
| SCIM provisioning, IdP groups → workspace + role, deprovision disables keys | gap · W2 | SCIM 2.0 Users/Groups endpoints |
| Management API keys (programmatic key create / rotate / delete) | gap · W1 | service tokens for the admin API, scoped to an org |
| Key rotation | partial | revoke + create; add `rotate` that keeps settings |

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
| ZDR enforcement (account / guardrail / request `provider.zdr`, OR-ed) | have (policy) | provider `privacy` metadata; policy `require_zdr` filters targets; request-level `provider.zdr` with the request routing work |
| `data_collection: deny` | have (policy) | policy `deny_data_collection`; request-level with the request routing work |
| Data regions (`allowed_data_regions`: global / europe / us), regional endpoints | gap · W2 | provider `region` tag + policy; regional hostnames are a deploy concern |
| Prompt / response logging off by default, opt-in per workspace | partial · W1 | we store metadata only; add opt-in content logging with TTL + encryption |
| BYOK with declared ZDR | have | org providers take the same `privacy` declaration |

## Routing (request level)

| OpenRouter | Polyrail | Notes |
|---|---|---|
| `models: [...]` fallback array | gap · W1 | try other gateway models in order |
| `provider` object: `order`, `only`, `ignore`, `allow_fallbacks`, `require_parameters`, `sort` (price / throughput / latency), `max_price`, `preferred_min_throughput`, `preferred_max_latency`, `quantizations`, `data_collection`, `zdr` | partial · W1 | we have per-model strategies (cost, latency, …); add per-request overrides on the same router |
| `:nitro` / `:floor` shortcuts | gap · W1 | aliases for `sort: throughput` / `price` |
| Presets `@preset/<slug>` (saved model + params + routing) | gap · W1 | org-scoped |
| Auto router (`openrouter/auto`) | gap · later | needs a classifier model; can reuse routing scores |
| Usage accounting in body (`usage.cost`, cached + reasoning tokens) | partial · W1 | we send `X-GW-Cost-Micros`; add to body when `usage: {include: true}` |
| `user` field for per-end-user analytics + sticky routing | gap · W1 | |

## Observability

| OpenRouter | Polyrail | Notes |
|---|---|---|
| Activity export CSV, group by model / key / creator | partial · W1 | have usage UI; add CSV export + creator grouping |
| Broadcast to OTel, Langfuse, Datadog, S3, … with sampling and key filters | gap · W2 | OTel GenAI spans first (covers most backends), S3/NDJSON second |
| Prometheus | have | OpenRouter has none |

## Proposed waves
- **W1 (policies + routing, core enterprise):** policy engine (org default / member / key; budgets with intervals, per-member budgets, allowed models + providers, filters redact/block, ZDR + data collection), provider privacy metadata, request `models` + `provider` routing + `:nitro`/`:floor`, presets, `usage.cost` in body, `user` analytics, members-see-own-keys, management API tokens, CSV export, opt-in content logging.
- **W2 (identity + compliance):** SAML SSO, domain verification, SSO enforcement, SCIM, data regions, OTel/Langfuse broadcast.
- **W3 (workspaces):** workspaces inside orgs scoping keys, providers (BYOK), guardrails, presets, budgets, members.
