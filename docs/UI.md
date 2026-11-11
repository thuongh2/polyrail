# Web UI

React 19 + Vite + Tailwind 4 + shadcn/ui (new-york, copied into `web/src/components/ui`), routed with
wouter. Built into `internal/web/dist` and embedded in the Go binary; Go serves `index.html` for any
path without a file (SPA). No Node at runtime. `make ui` rebuilds; built files are committed.

Budget: initial JS for public pages ≤ 120 kB gzip; the console is a lazy chunk. No chart library:
charts are small SVG components. No new runtime dependency without a reason in docs/DECISIONS.md.

## Conventions (binding for every agent touching web/)

- Data: `web/src/lib/api.ts` (types + `publicApi`, `authApi`, `api()`), `useAsync` from `lib/hooks.ts`,
  `useSession()` for user/org/role (`can("admin")`). Formatting helpers (`usd`, `perMillion`, `fmt`,
  `compact`, `pct`, `when`) live in api.ts; reuse them.
- Every data region renders four states through `<DataState>` (loading skeleton, error with retry,
  empty with the one action that fills it, full). Page titles via `<PageHeader>`.
- Tables before cards. Use shadcn `Table`, `Badge`, `Button`, `Dialog`, `Tabs`, `Select`, `Input`.
  Destructive actions use `AlertDialog` confirmation. Feedback via `toast()` from components/toast.
- Colors only through tokens (`bg-background`, `text-muted-foreground`, `text-success`, `bg-signal`,
  `bg-danger`, `bg-warning`, `bg-nodata`...). Never a literal color, never `transition-all`, no
  gradients, no emoji, no tracked uppercase labels, no eyebrow text above headings, no invented
  numbers or testimonials. Status is shown as text (+ color), never a decorative dot alone.
- Filters, tabs and sort live in the URL (`useQueryParam`).
- Accessible: labels on every input, `aria-label` on icon buttons, keyboard reachable, focus visible.
- Light and dark mode both checked.
- Provider and model-author logos: `<ProviderLogo type|model />` (components/ProviderLogo.tsx), labels and
  the logo map in `lib/providers.ts`. SVGs are static files in `web/public/logos/` (served from dist, never
  bundled), taken unchanged from `@lobehub/icons-static-svg` 1.102.0 (MIT, `web/public/logos/LICENSE`).
  One-color logos render as a CSS mask on `bg-current` so they follow the theme; colored ones as `<img>`;
  types without a logo get a monogram. Decorative (`aria-hidden`) when the name is written next to them.

## Console layout

`components/layout/ConsoleLayout.tsx`, modeled on GitLab's current frame. The browser page never scrolls:
the frame is `h-dvh overflow-hidden` on the muted `--frame` surface (token on `:root` and `.dark`).

- Top bar (no border, on the frame): wordmark (sm and up), org switcher and workspace switcher (an "Admin" badge
  instead inside the Admin area), then "Search…" with the Ctrl/⌘K hint (icon only below lg), Models, theme toggle,
  account email (xl), sign out.
- Sidebar (md and up, on the frame, no border): the current section's groups from `components/layout/nav.ts`
  (see "Navigation map") with plain small muted headings; active item is a `--frame-active` pill, bold, with a
  thin `bg-signal` left mark. The nav scrolls internally (`no-scrollbar`); Settings, Docs, Admin area (operators),
  Account and "Collapse sidebar" are pinned below it. Collapsed = icons only (`aria-label` + `title` on each
  link), stored in `localStorage["console.sidebar"]`.
- Command palette (`CommandPalette.tsx`, lazy chunk loaded on first open): Ctrl/⌘K or the Search button. Lists
  every page the user may open (from nav.ts, same role gating) with its group, plus create actions that deep-link
  with `?new=1` ("Create API key", "Add provider", "Add public provider"; pages read it with `useNewParam`).
  Subsequence filter, ↑↓ Home End Enter, Esc closes; combobox + listbox + `aria-activedescendant` in the shadcn Dialog.
- Content is a raised panel (`bg-background`, `rounded-xl`, border, small shadow, inset from the frame).
  It is the only scroll container (`<main tabIndex=0>`, `no-scrollbar`, keyboard/wheel/touch scroll
  still work) and resets to the top on navigation. Pages must not rely on window scroll. Inside: a
  breadcrumb derived from nav.ts (section / group / page: "Build / API keys", "Settings / Integrations / Alerts",
  "Admin / Routing / Proxies"), then the page, max width 1200px.
- Workspaces (D31): `useSession()` exposes `workspaces`, `ws` (null = "All workspaces", org owners/admins only),
  `wsCan(min)` (role in the current workspace), `wsList(path)` (adds `?workspace=`) and `wsBody` (`{workspace_id}` for
  create bodies). The choice is stored per org in `localStorage["console.ws.<org>"]` and mirrored in `?ws=` (absent
  for the default). Workspace-scoped pages (API keys, Providers, Models, Policies, Presets, Knowledge bases,
  Observability, Logs, Activity, Playground) list with `wsList` and create with `wsBody`; under "All workspaces" they
  add a Workspace column and create in the default workspace. The layout remounts the page on a switch, so lists reload.
- Below md the sidebar is the Sheet drawer (same section nav and pinned entries) and the panel is full width
  without the rounded inset.
- Scrollbars: `no-scrollbar` (hidden, still scrollable) and `scrollbar-thin` (thin, border-colored) are
  utilities in index.css. Wide tables (`TableFrame`) and dialogs use `scrollbar-thin`.

## Navigation map (binding)

Owner ruling 2026-11-11: the sidebar stays short and task based. Config lives in `components/layout/nav.ts`;
URLs never change when an entry moves (add a redirect if a new path replaces an old one).

| Section | Where it shows | Entries |
|---|---|---|
| Organization sidebar | every org page | Activity, Logs, Playground · **Build**: API keys, Models, Presets, Knowledge bases · **Manage**: Providers (admin), Policies (admin), Credits · pinned: Settings, Docs, Admin area (operators), Account, Collapse |
| Settings hub (`/app/settings`) | sub-nav inside the panel: left column from lg, scrolling tab row below; the sidebar highlights Settings | General (`/app/settings`), Members, Workspaces, Security & SSO (owner) · **Integrations**: Webhooks, Alerts, Observability (admins) · Audit log (admin) |
| Admin area (`/app/platform/*`, operators) | replaces the org sidebar; top bar shows "Admin" | ← Back to org · Overview, Organizations, Users · **Routing**: Providers, Models, Proxies, MCP servers · **Monitoring**: Alerts, Observability, Audit log |

Rules for new pages: put an org feature into Build (things developers create and call), Manage (things admins
govern: upstreams, rules, money) or the Settings hub (organization configuration and integrations); put
platform-wide operator pages into an Admin area group. Never add a new top-level sidebar item or group
without an owner decision. Give the item `min`/`ws` for role gating and `keywords` for the palette; the
breadcrumb and palette follow from it. Add the path to the matching click-through list in `scripts/ui-smoke.py`.

## Routes

Public: `/`, `/models`, `/models/<vendor>/<name>`, `/status`, `/docs`, `/login`, `/signup`.
Sign-in is email first: `GET /api/auth/sso?email=` decides between "Continue with SSO" and the password field;
an `sso_required` login error switches to the SSO button.
Console: `/app/activity|logs|playground|keys|credits|models|presets|kbs|providers|workspaces|members|policies|settings|webhooks|alerts|observability|sso|audit|account`
(`/app/sso` Security & SSO is owners only; `/app/observability` trace exports, admins)
(`/app/keys?tab=tokens` is the management tokens tab, admins only; `/app/workspaces?tab=members&w=<id>` a workspace's members),
`/app/kbs/<id>`, `/app/providers/<id>` (provider detail: keys, docs/KEYS.md),
operators: `/app/platform/activity|orgs|providers|proxies|models|users|mcp|alerts|observability|audit`, `/app/platform/providers/<id>`.
