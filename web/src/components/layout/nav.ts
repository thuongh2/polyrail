import {
  Activity, BarChart3, Bell, Cable, BookOpen, Boxes, Building2, CircleUserRound, CreditCard, FileText, FolderKanban, KeyRound,
  LockKeyhole, MessageSquare, Network, Plug, Scale, ScrollText, Server, Settings, ShieldCheck, SlidersHorizontal, Telescope,
  UserRound, Users, Webhook, Wrench,
} from "lucide-react";
import type { Role } from "@/lib/api";
import { useSession } from "@/lib/session";

/**
 * The console information architecture (docs/UI.md "Navigation map"). Three sections, each with its own
 * sidebar: the organization ("main"), the Settings hub (sub-nav inside the panel) and the Admin area
 * (operators; replaces the org sidebar). New pages go into an existing group, never a new top-level item.
 *
 * min: least role needed; ws: the page is workspace-scoped, so `min` is checked against the role in the
 * current workspace. keywords: extra words the command palette matches.
 */
export type Item = { href: string; label: string; icon: typeof Activity; min?: Role; ws?: boolean; keywords?: string };
export type Group = { label?: string; items: Item[] };
export type Section = "main" | "settings" | "admin";

export const SETTINGS_HREF = "/app/settings";
export const ADMIN_HREF = "/app/platform/activity";

export const mainGroups: Group[] = [
  {
    items: [
      { href: "/app/activity", label: "Activity", icon: BarChart3, keywords: "usage dashboard spend" },
      { href: "/app/logs", label: "Logs", icon: ScrollText, keywords: "requests" },
      { href: "/app/playground", label: "Playground", icon: MessageSquare, keywords: "chat try" },
      { href: "/app/connect", label: "Connect", icon: Cable, keywords: "codex claude code opencode gemini cli cline open webui setup endpoint" },
    ],
  },
  {
    label: "Build",
    items: [
      { href: "/app/keys", label: "API keys", icon: KeyRound, keywords: "tokens management" },
      { href: "/app/models", label: "Models", icon: Boxes, keywords: "pricing aliases" },
      { href: "/app/presets", label: "Presets", icon: SlidersHorizontal, keywords: "routing" },
      { href: "/app/kbs", label: "Knowledge bases", icon: BookOpen, keywords: "rag documents" },
    ],
  },
  {
    label: "Manage",
    items: [
      { href: "/app/providers", label: "Providers", icon: Plug, min: "admin", ws: true, keywords: "upstream keys" },
      { href: "/app/policies", label: "Policies", icon: Scale, min: "admin", ws: true, keywords: "guardrails limits" },
      { href: "/app/credits", label: "Credits", icon: CreditCard, keywords: "billing budget" },
    ],
  },
];

export const settingsGroups: Group[] = [
  {
    items: [
      { href: "/app/settings", label: "General", icon: Settings, keywords: "organization name settings" },
      { href: "/app/members", label: "Members", icon: Users, keywords: "invite roles team" },
      { href: "/app/workspaces", label: "Workspaces", icon: FolderKanban },
      { href: "/app/sso", label: "Security & SSO", icon: LockKeyhole, min: "owner", keywords: "saml oidc scim domains" },
    ],
  },
  {
    label: "Integrations",
    items: [
      { href: "/app/webhooks", label: "Webhooks", icon: Webhook, min: "admin" },
      { href: "/app/alerts", label: "Alerts", icon: Bell, min: "admin", keywords: "notifications channels" },
      { href: "/app/observability", label: "Observability", icon: Telescope, min: "admin", ws: true, keywords: "otlp traces export" },
    ],
  },
  { items: [{ href: "/app/audit", label: "Audit log", icon: ShieldCheck, min: "admin" }] },
];

export const adminGroups: Group[] = [
  {
    items: [
      { href: "/app/platform/activity", label: "Overview", icon: Activity, keywords: "platform activity usage" },
      { href: "/app/platform/orgs", label: "Organizations", icon: Building2, keywords: "tenants" },
      { href: "/app/platform/users", label: "Users", icon: UserRound },
    ],
  },
  {
    label: "Routing",
    items: [
      { href: "/app/platform/providers", label: "Providers", icon: Server, keywords: "public upstream" },
      { href: "/app/platform/models", label: "Models", icon: Boxes, keywords: "public catalog" },
      { href: "/app/platform/proxies", label: "Proxies", icon: Network, keywords: "proxy pools" },
      { href: "/app/platform/mcp", label: "MCP servers", icon: Wrench, keywords: "tools" },
    ],
  },
  {
    label: "Monitoring",
    items: [
      { href: "/app/platform/alerts", label: "Alerts", icon: Bell },
      { href: "/app/platform/observability", label: "Observability", icon: Telescope },
      { href: "/app/platform/audit", label: "Audit log", icon: ShieldCheck },
    ],
  },
];

export const accountItem: Item = { href: "/app/account", label: "Account", icon: CircleUserRound, keywords: "profile password" };
export const docsItem: Item = { href: "/docs", label: "Docs", icon: FileText, keywords: "help api reference" };

const sections: { key: Section; root?: { label: string; href: string }; groups: Group[] }[] = [
  { key: "main", groups: mainGroups },
  { key: "settings", root: { label: "Settings", href: SETTINGS_HREF }, groups: settingsGroups },
  { key: "admin", root: { label: "Admin", href: ADMIN_HREF }, groups: adminGroups },
];

const under = (loc: string, href: string) => loc === href || loc.startsWith(href + "/");

/** The nav item the location is on or under (longest href wins), with its section and group. */
export function locate(loc: string) {
  let best: { section: (typeof sections)[number]; group: Group; item: Item } | undefined;
  for (const s of sections)
    for (const g of s.groups)
      for (const i of g.items)
        if (under(loc, i.href) && (!best || i.href.length > best.item.href.length)) best = { section: s, group: g, item: i };
  return best;
}

export function sectionOf(loc: string): Section {
  if (under(loc, "/app/platform")) return "admin";
  return locate(loc)?.section.key ?? "main";
}

export type Crumb = { label: string; href?: string };

/** "Settings / Integrations / Alerts", "Admin / Routing / Proxies", "Build / API keys". */
export function crumbsFor(loc: string): Crumb[] {
  if (loc === accountItem.href) return [{ label: "Account" }];
  const c = locate(loc);
  if (!c) return [];
  const out: Crumb[] = [];
  if (c.section.root) out.push({ label: c.section.root.label, href: loc === c.section.root.href ? undefined : c.section.root.href });
  if (c.group.label) out.push({ label: c.group.label });
  out.push({ label: c.item.label, href: loc === c.item.href ? undefined : c.item.href });
  return out;
}

/** Returns a predicate for whether the signed-in user may open a nav item of the given section. */
export function useAllowed() {
  const { user, org, can, wsCan } = useSession();
  return (section: Section, i: Item) => {
    if (section === "admin") return !!user?.operator;
    if (!org) return false;
    return !i.min || (i.ws ? wsCan(i.min) : can(i.min));
  };
}

/** Groups of a section with the items the user may open; empty groups dropped. */
export function useVisibleGroups(section: Section) {
  const allowed = useAllowed();
  const groups = section === "main" ? mainGroups : section === "settings" ? settingsGroups : adminGroups;
  return groups.map((g) => ({ ...g, items: g.items.filter((i) => allowed(section, i)) })).filter((g) => g.items.length);
}

/** Every page the user may open, for the command palette. group is the label shown beside it. */
export function usePaletteEntries() {
  const allowed = useAllowed();
  const out: { item: Item; group: string }[] = [];
  for (const s of sections)
    for (const g of s.groups)
      for (const i of g.items)
        if (allowed(s.key, i)) out.push({ item: i, group: [s.root?.label, g.label].filter(Boolean).join(" / ") || "Organization" });
  out.push({ item: accountItem, group: "You" }, { item: docsItem, group: "Help" });
  return out;
}
