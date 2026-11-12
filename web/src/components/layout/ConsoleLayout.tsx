import { Fragment, lazy, Suspense, useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter";
import { ArrowLeft, FolderKanban, LogOut, Menu, PanelLeftClose, PanelLeftOpen, Search, Settings, ShieldEllipsis } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { authApi } from "@/lib/api";
import { ALL_WS, syncWsQuery, useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { ThemeToggle } from "./ThemeToggle";
import { Wordmark } from "./PublicLayout";
import {
  ADMIN_HREF, SETTINGS_HREF, accountItem, crumbsFor, docsItem, locate, sectionOf, useVisibleGroups, type Item, type Section,
} from "./nav";

// The palette is opened rarely, so its chunk loads on first use (Ctrl/Cmd+K or the Search button).
const CommandPalette = lazy(() => import("./CommandPalette"));

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

export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

const itemClass = (active: boolean, collapsed?: boolean) =>
  cn(
    "relative flex h-8 items-center gap-2.5 rounded-md px-2 text-foreground/80 hover:bg-frame-active/60 hover:text-foreground",
    "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
    collapsed && "justify-center px-0",
    active &&
      "bg-frame-active font-semibold text-foreground before:absolute before:inset-y-1.5 before:left-0 before:w-0.5 before:rounded-full before:bg-signal",
  );

function NavLink({ href, label, icon: Icon, collapsed, onNavigate, active }: {
  href: string; label: string; icon: Item["icon"]; collapsed?: boolean; onNavigate?: () => void; active: boolean;
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

/** Sidebar of the current section: the organization nav, or the Admin area nav inside /app/platform. */
function Nav({ section, onNavigate, collapsed }: { section: Section; onNavigate?: () => void; collapsed?: boolean }) {
  const [loc] = useLocation();
  const { org } = useSession();
  const admin = section === "admin";
  const groups = useVisibleGroups(admin ? "admin" : "main");
  const current = locate(loc)?.item.href;
  return (
    <div className="flex flex-col gap-4 p-2 text-sm">
      {admin && (
        <div className="flex flex-col gap-0.5">
          <NavLink
            href="/app/activity"
            label={org ? `Back to ${org.name}` : "Back to console"}
            icon={ArrowLeft}
            collapsed={collapsed}
            onNavigate={onNavigate}
            active={false}
          />
        </div>
      )}
      {groups.map((g, n) => (
        <div key={g.label ?? n} className="flex flex-col gap-0.5">
          {collapsed
            ? (n > 0 || admin) && <hr className="mx-2 mb-1 border-frame-active" aria-hidden />
            : g.label && <p className="px-2 pb-1 text-xs text-muted-foreground">{g.label}</p>}
          {g.items.map((i) => (
            <NavLink key={i.href} {...i} collapsed={collapsed} onNavigate={onNavigate} active={current === i.href} />
          ))}
        </div>
      ))}
    </div>
  );
}

/** Entries pinned under the nav: Settings (org context), Docs, Admin area (operators), Account, Collapse. */
function NavFooter({ section, collapsed, onNavigate, onToggle }: {
  section: Section; collapsed?: boolean; onNavigate?: () => void; onToggle?: () => void;
}) {
  const [loc] = useLocation();
  const { user, org } = useSession();
  const link = { collapsed, onNavigate };
  return (
    <div className="mx-2 flex flex-col gap-0.5 border-t border-frame-active py-2 text-sm">
      {section !== "admin" && org && <NavLink href={SETTINGS_HREF} label="Settings" icon={Settings} active={section === "settings"} {...link} />}
      <NavLink {...docsItem} active={false} {...link} />
      {user?.operator && section !== "admin" && <NavLink href={ADMIN_HREF} label="Admin area" icon={ShieldEllipsis} active={false} {...link} />}
      <NavLink {...accountItem} active={loc === accountItem.href} {...link} />
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
  const parts = crumbsFor(loc);
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
 * Settings hub: a secondary nav inside the panel (left column from lg, a horizontally scrolling tab
 * row below) around the organization's settings pages, which keep their own URLs.
 */
function SettingsHub({ children }: { children: ReactNode }) {
  const [loc] = useLocation();
  const groups = useVisibleGroups("settings");
  const current = locate(loc)?.item.href;
  const nav = useRef<HTMLElement>(null);
  // Below lg the sub-nav is a horizontal tab row: keep the current tab in view.
  useEffect(() => {
    const el = nav.current, a = el?.querySelector<HTMLElement>('[aria-current="page"]');
    if (el && a && el.scrollWidth > el.clientWidth) el.scrollLeft = a.offsetLeft - (el.clientWidth - a.offsetWidth) / 2;
  }, [current]);
  return (
    <div className="flex flex-col gap-6 lg:flex-row lg:gap-8">
      {/* Sticky in the scrolling panel: the tab row on top below lg, the side column from lg. */}
      <nav
        ref={nav}
        aria-label="Settings"
        className="no-scrollbar sticky top-0 z-10 -mx-4 shrink-0 overflow-x-auto bg-background px-4 lg:top-4 lg:mx-0 lg:max-h-[calc(100dvh-7rem)] lg:w-48 lg:self-start lg:overflow-y-auto lg:bg-transparent lg:px-0"
      >
        <ul className="flex gap-1 border-b text-sm lg:flex-col lg:gap-3 lg:border-b-0">
          {groups.map((g, n) => (
            <li key={g.label ?? n} className="contents lg:block">
              {g.label && <p className="hidden px-2 pb-1 text-xs text-muted-foreground lg:block">{g.label}</p>}
              <ul className="contents lg:flex lg:flex-col lg:gap-0.5">
                {g.items.map((i) => {
                  const active = current === i.href;
                  return (
                    <li key={i.href} className="shrink-0">
                      <Link
                        href={i.href}
                        aria-current={active ? "page" : undefined}
                        className={cn(
                          "flex h-9 items-center gap-2 px-2 whitespace-nowrap text-muted-foreground hover:text-foreground",
                          "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
                          "-mb-px border-b-2 border-transparent lg:mb-0 lg:h-8 lg:rounded-md lg:border-b-0 lg:hover:bg-muted",
                          active && "border-signal font-medium text-foreground lg:bg-muted",
                        )}
                      >
                        <i.icon className="hidden size-4 shrink-0 lg:block" aria-hidden />
                        {i.label}
                      </Link>
                    </li>
                  );
                })}
              </ul>
            </li>
          ))}
        </ul>
      </nav>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

/** Workspace switcher (GitLab/OpenRouter style): the workspaces the user is in; org admins also get "All workspaces". */
function WorkspaceSwitcher() {
  const { workspaces, wsId, setWs, canAllWs } = useSession();
  if (!workspaces.length) return null;
  return (
    <>
      <span aria-hidden className="hidden text-muted-foreground/60 sm:inline">
        /
      </span>
      <Select value={wsId} onValueChange={setWs}>
        <SelectTrigger size="sm" className="w-28 bg-background sm:w-44" aria-label="Workspace">
          <span className="flex min-w-0 items-center gap-2">
            <FolderKanban className="hidden shrink-0 text-muted-foreground sm:block" aria-hidden />
            <span className="truncate">
              <SelectValue />
            </span>
          </span>
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          {workspaces.map((w) => (
            <SelectItem key={w.id} value={w.id}>
              {w.name}
            </SelectItem>
          ))}
          {canAllWs && (
            <>
              <SelectSeparator />
              <SelectItem value={ALL_WS}>All workspaces</SelectItem>
            </>
          )}
        </SelectContent>
      </Select>
    </>
  );
}

/**
 * GitLab-style frame: the browser page never scrolls. Top bar and sidebar sit on the muted frame;
 * content lives in a raised panel that is the only scroll container (scrollbar hidden, still
 * scrollable by wheel, touch and keyboard since the panel is focusable).
 */
export function ConsoleLayout({ children }: { children: ReactNode }) {
  // The console never scrolls the document: only the content panel and the sidebar scroll. Lock
  // html/body while the console is mounted, so focus moves, anchors or wide content can't scroll
  // the page (which slid the top bar and the Settings menu out of view).
  useEffect(() => {
    document.documentElement.classList.add("console-lock");
    return () => document.documentElement.classList.remove("console-lock");
  }, []);

  const { user, orgs, org, setOrg, refresh, loading, wsId } = useSession();
  const [loc, navigate] = useLocation();
  const [open, setOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(readCollapsed);
  // undefined until first opened, so the palette chunk is not fetched before it is wanted.
  const [palette, setPalette] = useState<boolean | undefined>(undefined);
  const panel = useRef<HTMLElement>(null);
  const section = sectionOf(loc);

  useEffect(() => {
    if (!loading && !user) navigate(`/login?next=${encodeURIComponent(location.pathname)}`);
  }, [loading, user, navigate]);
  // The panel replaces window scrolling, so a new page starts at its top.
  useEffect(() => {
    panel.current?.scrollTo({ top: 0 });
  }, [loc]);
  // Keep ?ws= on the URL so a link to a page opens it in the same workspace.
  useEffect(() => {
    if (org && section !== "admin") syncWsQuery(wsId);
  }, [loc, org, wsId, section]);
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPalette((v) => !v);
      }
    };
    addEventListener("keydown", on);
    return () => removeEventListener("keydown", on);
  }, []);
  if (!user) return null;

  const toggle = () => {
    writeCollapsed(!collapsed);
    setCollapsed(!collapsed);
  };
  const admin = section === "admin";
  const navLabel = admin ? "Admin area" : "Console";

  return (
    <div className="flex h-dvh flex-col overflow-clip bg-frame">
      <header className="flex h-12 shrink-0 items-center gap-2 px-3">
        <Button variant="ghost" size="icon" className="md:hidden" aria-label="Open menu" onClick={() => setOpen(true)}>
          <Menu />
        </Button>
        <div className="hidden px-1 sm:block">
          <Wordmark />
        </div>
        {admin ? (
          <Badge variant="outline" className="bg-background sm:ml-2">
            Admin
          </Badge>
        ) : (
          <>
            {orgs.length > 0 && (
              <Select value={org?.id} onValueChange={setOrg}>
                <SelectTrigger size="sm" className="w-28 bg-background sm:ml-2 sm:w-48" aria-label="Organization">
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
            <WorkspaceSwitcher />
          </>
        )}
        <div className="ml-auto flex items-center gap-1">
          <Button
            variant="outline"
            size="sm"
            className="hidden w-48 justify-between bg-background font-normal text-muted-foreground lg:inline-flex"
            onClick={() => setPalette(true)}
            aria-keyshortcuts={isMac ? "Meta+K" : "Control+K"}
          >
            <span className="flex items-center gap-2">
              <Search aria-hidden /> Search…
            </span>
            <kbd className="rounded border bg-muted px-1.5 font-mono text-[11px] text-muted-foreground">{isMac ? "⌘K" : "Ctrl K"}</kbd>
          </Button>
          <Button variant="ghost" size="icon" className="lg:hidden" aria-label="Search pages" onClick={() => setPalette(true)}>
            <Search />
          </Button>
          <Button variant="ghost" size="sm" className="hidden sm:inline-flex" asChild>
            <Link href="/models">Models</Link>
          </Button>
          <ThemeToggle />
          <Button variant="ghost" size="sm" className="hidden xl:inline-flex" asChild>
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
        <nav aria-label={navLabel} className={cn("hidden shrink-0 flex-col md:flex", collapsed ? "w-14" : "w-60")}>
          <div className="no-scrollbar min-h-0 flex-1 overflow-y-auto">
            <Nav section={section} collapsed={collapsed} />
          </div>
          <NavFooter section={section} collapsed={collapsed} onToggle={toggle} />
        </nav>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="left" className="w-64 gap-0 bg-frame p-0">
            <SheetTitle className="px-4 pt-4 pb-2">{admin ? "Admin area" : "Menu"}</SheetTitle>
            <nav aria-label={navLabel} className="flex min-h-0 flex-1 flex-col">
              <div className="no-scrollbar min-h-0 flex-1 overflow-y-auto">
                <Nav section={section} onNavigate={() => setOpen(false)} />
              </div>
              <NavFooter section={section} onNavigate={() => setOpen(false)} />
            </nav>
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
            {/* Pages remount on an org or workspace switch, so every list reloads in the new scope. */}
            {section === "settings" ? (
              <SettingsHub>
                <Fragment key={`${org?.id ?? ""}/${wsId}`}>{children}</Fragment>
              </SettingsHub>
            ) : (
              <Fragment key={`${org?.id ?? ""}/${wsId}`}>{children}</Fragment>
            )}
          </div>
        </main>
      </div>
      {palette !== undefined && (
        <Suspense fallback={null}>
          <CommandPalette open={palette} onOpenChange={setPalette} />
        </Suspense>
      )}
    </div>
  );
}
