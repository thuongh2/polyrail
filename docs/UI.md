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

- Top bar (no border, on the frame): wordmark, org switcher, then Models, theme toggle, account, sign out.
- Sidebar (md and up, on the frame, no border): nav groups from the `groups` config with plain small
  muted headings; active item is a `--frame-active` pill, bold, with a thin `bg-signal` left mark. The nav
  scrolls internally (`no-scrollbar`); Account, Docs and "Collapse sidebar" are pinned below it.
  Collapsed = icons only (`aria-label` + `title` on each link), stored in `localStorage["console.sidebar"]`.
- Content is a raised panel (`bg-background`, `rounded-xl`, border, small shadow, inset from the frame).
  It is the only scroll container (`<main tabIndex=0>`, `no-scrollbar`, keyboard/wheel/touch scroll
  still work) and resets to the top on navigation. Pages must not rely on window scroll. Inside: a
  breadcrumb derived from the nav config ("Organization / API keys"), then the page, max width 1200px.
- Below md the sidebar is the Sheet drawer and the panel is full width without the rounded inset.
- Scrollbars: `no-scrollbar` (hidden, still scrollable) and `scrollbar-thin` (thin, border-colored) are
  utilities in index.css. Wide tables (`TableFrame`) and dialogs use `scrollbar-thin`.

## Routes

Public: `/`, `/models`, `/models/<vendor>/<name>`, `/status`, `/docs`, `/login`, `/signup`.
Sign-in is email first: `GET /api/auth/sso?email=` decides between "Continue with SSO" and the password field;
an `sso_required` login error switches to the SSO button.
Console: `/app/activity|logs|playground|keys|credits|models|presets|kbs|providers|members|policies|settings|webhooks|alerts|observability|sso|audit|account`
(`/app/sso` Security & SSO is owners only; `/app/observability` trace exports, admins)
(`/app/keys?tab=tokens` is the management tokens tab, admins only),
`/app/kbs/<id>`, `/app/providers/<id>` (provider detail: keys, docs/KEYS.md),
operators: `/app/platform/activity|orgs|providers|proxies|models|users|mcp|alerts|observability|audit`, `/app/platform/providers/<id>`.
