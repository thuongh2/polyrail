# Testing — acceptance criteria → tests

Owner: QA agent. Run everything with `make check` (vet + `go test -race`). Set
`TEST_POSTGRES_URL` / `TEST_REDIS_URL` to include the scale-mode backends; without them those
tests skip. `go test -short` skips the end-to-end tests (they build and run the real binary).

Status legend: **covered** (test passes), **FAILING** (test exists and fails: open bug),
**partial** (only part of the criterion is asserted), **uncovered** (no automated test).

Test locations: `gw` = internal/gateway/gateway_test.go, `adm` = internal/admin/admin_test.go,
`st` = internal/store/*_test.go, `us` = internal/usage/*_test.go, `lim` = internal/limits/limits_test.go,
`e2e` = test/e2e/e2e_test.go (`TestE2EPebble`, `TestE2EPostgresRedis` run the same flow).

## F1 OpenAI-compatible API
| AC | Tests | Status |
|---|---|---|
| AC1 chat non-stream | gw TestChatFailoverPricingAndBreaker; e2e | covered |
| AC2 stream SSE + `[DONE]` | gw TestStreaming; e2e | covered |
| AC3 embeddings | gw TestEmbeddingsAndModels; e2e | covered |
| AC4 `/v1/models` = public + own private | gw TestEmbeddingsAndModels (public only); e2e (public only) | partial: no test that org B's key does not see org A's private model via `/v1/models` (adm TestCrossTenantProvidersAndModels checks it only on the admin API and public catalog) |
| AC5 OpenAI error shape + status | gw TestPolicies (401/400/404 codes); e2e (401/404/400) | covered |

## F2 Providers
| AC | Tests | Status |
|---|---|---|
| AC1 operator public provider, multiple weighted keys | adm TestProviderSecretsNeverReturned; e2e | partial: key weights are stored but traffic split across keys is not asserted |
| AC2 tenant private provider, only that org routes to it | st TestProviderSecretsAndModelIsolation; adm TestCrossTenantProvidersAndModels, TestProviderSSRF | covered |
| AC2 (isolation) tenant cannot disturb public providers | adm TestTenantProviderDoesNotShadowPublicProvider | covered (fixed) |
| AC3 any OpenAI-compatible endpoint via `openai` + base URL | gw (mock upstream); e2e (mock upstream through real binary) | covered |
| AC4 keys never returned, encrypted at rest | st TestProviderSecretsAndModelIsolation; adm TestProviderSecretsNeverReturned; e2e | covered |

## F3 Catalog and pricing
| AC | Tests | Status |
|---|---|---|
| AC1 id, name, context, in/out price per 1M | adm TestCrossTenantProvidersAndModels; e2e | covered |
| AC2 cost = in×price + out×price, micro-USD | gw TestChatFailoverPricingAndBreaker; e2e (usage cost) | covered |
| AC3 public catalog page + `/api/public/models` no auth | adm TestCrossTenantProvidersAndModels; e2e | partial: the HTML catalog page itself is not tested |
| AC4 private models visible only to that org | adm TestCrossTenantProvidersAndModels; st TestProviderSecretsAndModelIsolation | covered (admin API + public catalog; see F1-AC4 gap) |
| AC5 public provider health + status page | us TestRecordHealthKeys; gw TestChatFailoverPricingAndBreaker (failure recorded for the failed target, success for the server, `TargetOpen`); adm TestPublicModelHealthAndStatus (404 for private, no ids/base_url, all four statuses, 72 hourly buckets); st TestPruneHealth | partial: `down` via an open breaker is asserted only through `TargetOpen`, not through the HTTP status |

## F4 Routing, load balancing, failover
| AC | Tests | Status |
|---|---|---|
| AC1 weighted split across targets | gw TestPlanWeightedSplitAndBreakerOrder | covered |
| AC2 failover on retryable error | gw TestChatFailoverPricingAndBreaker (5xx) | partial: 429 and timeout failover not tested |
| AC3 circuit breaker cool-down | gw TestChatFailoverPricingAndBreaker | partial: opening covered, re-closing after cool-down not tested |
| AC4 `x-gateway-provider` header | gw TestChatFailoverPricingAndBreaker; e2e | covered |

## F5 Caching and cost control
| AC | Tests | Status |
|---|---|---|
| AC1 response cache by header, cache-hit header, cost 0 | gw TestPolicies | partial: cost 0 on a hit not asserted |
| AC2 org `max_tokens_cap` | gw TestPolicies | covered (`max_completion_tokens` variant not tested) |
| AC3 org credit balance ≤ 0 → 402 | gw TestPolicies; adm TestCreditsOperatorOnly (granting) | covered |
| AC4 key monthly budget → 402 | gw TestPolicies (key budget) | covered |

## F6 API keys, limits, analytics
| AC | Tests | Status |
|---|---|---|
| AC1 shown once, SHA-256 only | adm TestKeySecretShownOnceAndAudited; e2e | covered |
| AC1 revocation immediate on the same node | adm TestKeyRevocationImmediateOnSameNode | covered (fixed) |
| AC1 revocation ≤ 30 s cluster-wide | gw TestPolicies (after Reload); e2e (within 6 s) | covered |
| AC2 RPM and TPM → 429 with `retry-after` | gw TestPolicies (RPM); lim TestLocal/TestRedis | partial: TPM 429 and the `retry-after` header not asserted end-to-end |
| AC3 per-key model allowlist and expiry | adm TestKeySecretShownOnceAndAudited (stored) | partial: enforcement on `/v1/*` not tested |
| AC4 usage analytics by day/model/key | us TestPipelineCountsSpendAndRetriesFailedFlush; e2e (`/usage`) | covered (errors and avg latency fields not asserted) |
| AC5 request logs, no prompt bodies | e2e (`/logs`) | partial: absence of prompt text in logs not asserted |

## F7 Multi-tenancy, RBAC, SSO, audit, guardrails
| AC | Tests | Status |
|---|---|---|
| AC1 resources belong to an org; non-members get 404 | adm TestRBACMatrix, TestCrossTenantProvidersAndModels | covered |
| AC2 RBAC matrix | adm TestRBACMatrix, TestLastOwnerCannotBeDemotedOrRemoved, TestCreditsOperatorOnly | covered (MCP mutation routes only checked for GET) |
| AC3 email+password login (bcrypt), sessions | adm TestLogin, TestDisabledUserAndPasswordChangeEndSessions, TestCSRFHeaderRequiredOnMutations; e2e | covered |
| AC3 OIDC SSO + auto-join | — | **uncovered** (needs a mock IdP) |
| AC4 audit entry per admin mutation, viewable by org admins | adm TestKeySecretShownOnceAndAudited, TestCreditsOperatorOnly | covered for key/member/org/credits |
| AC4 org deletion audit viewable | adm TestOrgDeletionAuditIsViewable | covered (fixed) |
| AC5 guardrails: max chars, blocked regex 400, PII mask | gw TestPolicies | partial: max input chars not tested |

## F8 MCP and observability
| AC | Tests | Status |
|---|---|---|
| AC1 MCP servers, tools in chat, `x-gateway-mcp-clients` | gw TestMCPOrgAllowlist; adm TestMCPGrantOperatorOnly | partial: no live MCP server |
| AC2 `/metrics` | e2e (`gateway_requests_total`, latency histogram, dropped logs) | partial: cache-hit metric not asserted |
| AC3 JSON logs, `/healthz`, `/readyz` | e2e (healthz, readyz, JSON "shutting down" log) | covered |

## F9 Deployment
| AC | Tests | Status |
|---|---|---|
| AC1 static binary + Dockerfile + compose | e2e builds and runs the binary with `CGO_ENABLED=0` | partial: no Dockerfile/compose exist in the repo yet |
| AC2 idle RSS < 100 MB on 1 vCPU / 2 GB | manual: docs/PROGRESS.md resource smoke (30 MB idle, 55 MB peak) | covered (manual) |
| AC3 first boot creates operator from env | e2e (login as `ADMIN_EMAIL`, operator=true, owner of Default org) | covered |

Also covered: graceful SIGTERM shutdown (e2e), both storage backends + Redis limiter through the
real binary (e2e `TestE2EPostgresRedis`, fresh database per run), SSRF protection on tenant
provider URLs (adm TestProviderSSRF), malformed/oversized admin JSON (adm TestMalformedJSON).

## Bugs found by tests

All three bugs QA found (tenant provider shadowing, unviewable org-deletion audit, delayed key
revocation) are fixed; their tests pass. Security regression tests: TestBlockedIPRanges,
TestUpstreamErrorsSanitized, TestMCPOrgAllowlist, TestMemberCannotLiftAdminLimits,
TestModelIDTraversalRejected, TestMCPGrantOperatorOnly, TestCostMicros.
