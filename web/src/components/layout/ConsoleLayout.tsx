import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter";
import {
  Activity, BarChart3, BookOpen, Boxes, Building2, CircleUserRound, CreditCard, FileText, KeyRound, LogOut, Menu,
  MessageSquare, Network, PanelLeftClose, PanelLeftOpen, Plug, ScrollText, Server, Settings, ShieldCheck, UserRound,
  Users, Webhook, Wrench,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { authApi, type Role } from "@/lib/api";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "./ThemeToggle";
import { Wordmark } from "./PublicLayout";

type Item = { href: string; label: string; icon: typeof Activity; min?: Role };
const groups: { label: string; items: Item[]; operator?: boolean }[] = [
  {
    label: "Usage",
    items: [
      { href: "/app/activity", label: "Activity", icon: BarChart3 },
      { href: "/app/logs", label: "Logs", icon: ScrollText },
      { href: "/app/playground", label: "Playground", icon: MessageSquare },
    ],
  },
  {
    label: "Organization",
    items: [
      { href: "/app/keys", label: "API keys", icon: KeyRound },
      { href: "/app/credits", label: "Credits", icon: CreditCard },
      { href: "/app/models", label: "Models", icon: Boxes },
      { href: "/app/kbs", label: "Knowledge bases", icon: BookOpen },
      { href: "/app/providers", label: "Providers", icon: Plug, min: "admin" },
      { href: "/app/members", label: "Members", icon: Users },
      { href: "/app/settings", label: "Settings", icon: Settings },
      { href: "/app/webhooks", label: "Webhooks", icon: Webhook, min: "admin" },
      { href: "/app/audit", label: "Audit log", icon: ShieldCheck, min: "admin" },
    ],
  },
  {
    label: "Platform",
    operator: true,
    items: [
      { href: "/app/platform/activity", label: "Activity", icon: Activity },
      { href: "/app/platform/orgs", label: "Organizations", icon: Building2 },
      { href: "/app/platform/providers", label: "Providers", icon: Server },
      { href: "/app/platform/proxies", label: "Proxies", icon: Network },
      { href: "/app/platform/models", label: "Models", icon: Boxes },
      { href: "/app/platform/users", label: "Users", icon: UserRound },
      { href: "/app/platform/mcp", label: "MCP servers", icon: Wrench },
      { href: "/app/platform/audit", label: "Audit log", icon: ShieldCheck },
    ],
  },
];

// Longest nav href that the location is on or under; drives the breadcrumb.
function crumbFor(loc: string) {
  let best: { group: string; item: Item } | undefined;
  for (const g of groups)
    for (const i of g.items)
      if ((loc === i.href || loc.startsWith(i.href + "/")) && (!best || i.href.length > best.item.href.length))
        best = { group: g.label, item: i };
  return best;
}

const SIDEBAR_KEY = "console.sidebar";
function readCollapsed() {
  try {
    return localStorage.getItem(SIDEBAR_KEY) === "collapsed";
  } catch {
    return false;
  }
}
function writeCollapsed(v: boolean) {
  try {
    localStorage.setItem(SIDEBAR_KEY, v ? "collapsed" : "expanded");
  } catch {
    /* private mode: the choice just won't persist */
  }
}

const itemClass = (active: boolean, collapsed?: boolean) =>
  cn(
    "relative flex h-8 items-center gap-2.5 rounded-md px-2 text-foreground/80 hover:bg-frame-active/60 hover:text-foreground",
    "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
    collapsed && "justify-center px-0",
    active &&
      "bg-frame-active font-semibold text-foreground before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-signal",
  );

function NavLink({ href, label, icon: Icon, collapsed, onNavigate, active }: {
  href: string; label: string; icon: typeof Activity; collapsed?: boolean; onNavigate?: () => void; active: boolean;
}) {
  return (
    <Link
      href={href}
      onClick={onNavigate}
      aria-current={active ? "page" : undefined}
      aria-label={collapsed ? label : undefined}
      title={collapsed ? label : undefined}
      className={itemClass(active, collapsed)}
    >
      <Icon className="size-4 shrink-0" aria-hidden />
      {!collapsed && <span className="truncate">{label}</span>}
    </Link>
  );
}

function Nav({ onNavigate, collapsed }: { onNavigate?: () => void; collapsed?: boolean }) {
  const [loc] = useLocation();
  const { user, org, can } = useSession();
  const current = crumbFor(loc)?.item.href;
  return (
    <nav aria-label="Console" className="flex flex-col gap-4 p-2 text-sm">
      {groups
        .filter((g) => (g.operator ? user?.operator : !!org))
        .map((g, n) => (
          <div key={g.label} className="flex flex-col gap-0.5">
            {collapsed ? (
              n > 0 && <hr className="mx-2 mb-1 border-frame-active" aria-hidden />
            ) : (
              <p className="px-2 pb-1 text-xs text-muted-foreground">{g.label}</p>
            )}
            {g.items
              .filter((i) => !i.min || can(i.min))
              .map((i) => (
                <NavLink key={i.href} {...i} collapsed={collapsed} onNavigate={onNavigate} active={current === i.href} />
              ))}
          </div>
        ))}
    </nav>
  );
}

/** Secondary actions pinned under the nav. */
function NavFooter({ collapsed, onNavigate, onToggle }: { collapsed?: boolean; onNavigate?: () => void; onToggle?: () => void }) {
  const [loc] = useLocation();
  return (
    <div className="mx-2 flex flex-col gap-0.5 border-t border-frame-active py-2 text-sm">
      <NavLink href="/app/account" label="Account" icon={CircleUserRound} collapsed={collapsed} onNavigate={onNavigate} active={loc === "/app/account"} />
      <NavLink href="/docs" label="Docs" icon={FileText} collapsed={collapsed} onNavigate={onNavigate} active={false} />
      {onToggle && (
        <button
          type="button"
          onClick={onToggle}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          aria-expanded={!collapsed}
          title={collapsed ? "Expand sidebar" : undefined}
          className={cn(itemClass(false, collapsed), "text-muted-foreground")}
        >
          {collapsed ? <PanelLeftOpen className="size-4 shrink-0" aria-hidden /> : <PanelLeftClose className="size-4 shrink-0" aria-hidden />}
          {!collapsed && <span>Collapse sidebar</span>}
        </button>
      )}
    </div>
  );
}

function Breadcrumb() {
  const [loc] = useLocation();
  const c = crumbFor(loc);
  const parts: { label: string; href?: string }[] =
    loc === "/app/account" ? [{ label: "Account" }] : c ? [{ label: c.group }, { label: c.item.label, href: loc === c.item.href ? undefined : c.item.href }] : [];
  if (!parts.length) return null;
  return (
    <nav aria-label="Breadcrumb" className="mb-4 text-sm">
      <ol className="flex flex-wrap items-center gap-1.5 text-muted-foreground">
        {parts.map((p, i) => (
          <Fragment key={i}>
            {i > 0 && (
              <li aria-hidden className="text-muted-foreground/60">
                /
              </li>
            )}
            <li>
              {p.href ? (
                <Link href={p.href} className="hover:text-foreground hover:underline">
                  {p.label}
                </Link>
              ) : i === parts.length - 1 ? (
                <span aria-current="page" className="text-foreground">
                  {p.label}
                </span>
              ) : (
                p.label
              )}
            </li>
          </Fragment>
        ))}
      </ol>
    </nav>
  );
}

/**
 * GitLab-style frame: the browser page never scrolls. Top bar and sidebar sit on the muted frame;
 * content lives in a raised panel that is the only scroll container (scrollbar hidden, still
 * scrollable by wheel, touch and keyboard since the panel is focusable).
 */
export function ConsoleLayout({ children }: { children: ReactNode }) {
  const { user, orgs, org, setOrg, refresh, loading } = useSession();
  const [loc, navigate] = useLocation();
  const [open, setOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const panel = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!loading && !user) navigate(`/login?next=${encodeURIComponent(location.pathname)}`);
  }, [loading, user, navigate]);
  // The panel replaces window scrolling, so a new page starts at its top.
  useEffect(() => {
    panel.current?.scrollTo({ top: 0 });
  }, [loc]);
  if (!user) return null;

  const toggle = () => {
    writeCollapsed(!collapsed);
    setCollapsed(!collapsed);
  };

  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-frame">
      <header className="flex h-12 shrink-0 items-center gap-2 px-3">
        <Button variant="ghost" size="icon" className="md:hidden" aria-label="Open menu" onClick={() => setOpen(true)}>
          <Menu />
        </Button>
        <div className="px-1">
          <Wordmark />
        </div>
        {orgs.length > 0 && (
          <Select value={org?.id} onValueChange={setOrg}>
            <SelectTrigger size="sm" className="ml-1 w-32 bg-background sm:ml-2 sm:w-48" aria-label="Organization">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {orgs.map((o) => (
                <SelectItem key={o.id} value={o.id}>
                  {o.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <div className="ml-auto flex items-center gap-1">
          <Button variant="ghost" size="sm" className="hidden sm:inline-flex" asChild>
            <Link href="/models">Models</Link>
          </Button>
          <ThemeToggle />
          <Button variant="ghost" size="sm" className="hidden md:inline-flex" asChild>
            <Link href="/app/account">{user.email}</Link>
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label="Sign out"
            title="Sign out"
            onClick={async () => {
              await authApi.logout().catch(() => {});
              await refresh();
              navigate("/login");
            }}
          >
            <LogOut />
          </Button>
        </div>
      </header>
      <div className="flex min-h-0 flex-1">
        <aside className={cn("hidden shrink-0 flex-col md:flex", collapsed ? "w-14" : "w-60")}>
          <div className="no-scrollbar min-h-0 flex-1 overflow-y-auto">
            <Nav collapsed={collapsed} />
          </div>
          <NavFooter collapsed={collapsed} onToggle={toggle} />
        </aside>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="left" className="w-64 gap-0 bg-frame p-0">
            <SheetTitle className="px-4 pt-4 pb-2">Menu</SheetTitle>
            <div className="no-scrollbar min-h-0 flex-1 overflow-y-auto">
              <Nav onNavigate={() => setOpen(false)} />
            </div>
            <NavFooter onNavigate={() => setOpen(false)} />
          </SheetContent>
        </Sheet>
        <main
          ref={panel}
          tabIndex={0}
          aria-label="Content"
          className={cn(
            "no-scrollbar min-w-0 flex-1 overflow-y-auto bg-background",
            "border-t md:mr-2 md:mb-2 md:rounded-xl md:border md:shadow-xs",
            "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
          )}
        >
          <div className="mx-auto max-w-[1200px] px-4 pt-4 pb-10 md:px-8 md:pt-5">
            <Breadcrumb />
            {children}
          </div>
        </main>
      </div>
    </div>
  );
}
