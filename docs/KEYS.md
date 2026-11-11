# Provider key management, key monitoring and alert channels (design, D25)

Owner request: manage a provider's API keys like gpt-load (list, bulk add, test, enable/disable,
status, per-key monitoring chart) and send alerts to Telegram and other channels through an
all-in-one notification library (not hand-written per channel).

## Key status
Computed per key (per node, in memory, plus persisted flags):
- `available` — enabled, not cooling down, not quarantined.
- `cooldown` — temporary: upstream 429/5xx/timeout on this key. `cooldown_until` = Retry-After
  (capped 5 min) else 30 s doubling to 5 min on consecutive failures; cleared by a success.
  A cooldown never moves traffic to dodge a provider's limits beyond the normal weighted choice
  among the operator's own keys; it only stops hammering a key that is already failing.
- `quarantined` — persisted: 3 consecutive auth failures (401/403) or an operator action. Excluded
  until a successful **Test** or manual re-enable. Emits `key.quarantined`.
- `disabled` — persisted, manual (`enabled:false`).
Keys that are not `available` are left out of `GetKeysForProvider` (if every key is out, all are
returned so the request still fails over through normal routing). Bifrost reports the key used in
`ExtraFields.RoutingInfo.Key` (key name) — the gateway names each Bifrost key by its stored key id.

## Stats
Per key, hourly aggregate `keystat/<yyyymmddhh>/<provider>|<key>` (requests, errors, latency sum,
in/out tokens) via the existing AddAgg pipeline; `last_used_at` per key (in memory, flushed with
stats). Retained like provider health rollups.

## API (scope = `/api/admin` for platform providers, `/api/orgs/{org}` for org providers; admin role)
- `GET …/providers/{id}/keys` → `[{id, name, masked, weight, models, enabled, status,
  status_reason, cooldown_until, last_used_at, stats_24h:{requests, errors, avg_latency_ms}}]`
  `masked` = first 4 chars + `****` + last 4 (never more).
- `POST …/providers/{id}/keys` `{"keys":"k1\nk2,k3" | ["k1","k2"], weight?, models?}` → bulk add,
  dedupe by SHA-256 of the value against existing keys → `{added, duplicates, invalid}` (≤1000/call).
- `PATCH …/providers/{id}/keys/{kid}` `{enabled?, weight?, name?, models?}`;
  `DELETE …/providers/{id}/keys/{kid}`.
- `POST …/providers/{id}/keys/bulk` `{action: enable|disable|delete|test|clear, ids:[…]}` →
  per-id results (`clear` resets cooldown/quarantine).
- `POST …/providers/{id}/keys/{kid}/test` `{model?}` → `{ok, status, latency_ms, error}` — a list
  models call (or a 1-token chat on `model`) pinned to that key; error text sanitized for tenants.
  Success clears quarantine/cooldown.
- `GET …/providers/{id}/keys/{kid}/stats?hours=72` → `[{hour, requests, errors, avg_latency_ms,
  in_tokens, out_tokens}]` oldest first.
Cloud types (azure/bedrock/vertex…) keep their key forms; the list shows them with `masked` of the
key name.

## Alert channels
Library: `github.com/nicholas-fedor/shoutrrr` (maintained fork of containrrr/shoutrrr, MIT):
one URL per channel — `telegram://token@telegram?chats=@chan`, `discord://`, `slack://`,
`teams://`, `smtp://`, `ntfy://`, `gotify://`, `pushover://`, `matrix://`, `googlechat://`,
`generic://` …
- `store.AlertChannel{id, org_id ("" platform), name, url (sealed, write-only; list shows the
  service name only), events [], enabled, failures, created_at}`.
- Routes: `GET/POST …/alerts`, `PATCH/DELETE …/alerts/{id}`, `POST …/alerts/{id}/test`.
- Events: `key.quarantined`, `key.recovered`, `provider.unavailable` (no available key),
  `budget.exceeded`, `request.failed` (reuses the webhook event source), `*`. Deduped per
  (channel, event, subject) for 10 min; async bounded queue; auto-disable after 10 failures.
- Tenant channels: only services with fixed vendor hosts (telegram, discord, slack, teams,
  googlechat, pushover, ntfy with default host); services with a custom host (smtp, gotify,
  generic, matrix, mattermost, ntfy self-hosted) are platform-only unless
  `ALLOW_PRIVATE_PROVIDER_URLS`, and then hosts must pass the public-URL check.

## UI
Provider detail page (`/app/providers/:id`, `/app/platform/providers/:id`, opened by clicking a
provider row): header (logo, name, status, type, base URL), search, status tabs with counts
(All, Available, Cooldown, Quarantined, Disabled), sort (Needs attention first, Recently used,
Name), **Add keys** dialog (paste many, one per line), select-all + bulk bar (Enable, Disable,
Test, Clear status, Delete), key cards (masked key, status dot, last used, weight, 24h ✓/✗, chart
button → drawer with 72 h requests/errors/latency chart, Test button with result toast, more menu:
edit name/weight/models, delete; enable switch). Alerts pages: Organization → Alerts and
Platform → Alerts (channel list, add with service picker + URL help, events, test, enable).
