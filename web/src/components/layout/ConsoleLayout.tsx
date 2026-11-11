import { useEffect, useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter";
import {
  Activity, BarChart3, BookOpen, Boxes, Building2, CreditCard, KeyRound, LogOut, Menu, MessageSquare, Plug,
  ScrollText, Server, Settings, ShieldCheck, UserRound, Users, Webhook, Wrench,
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
      { href: "/app/platform/models", label: "Models", icon: Boxes },
      { href: "/app/platform/users", label: "Users", icon: UserRound },
      { href: "/app/platform/mcp", label: "MCP servers", icon: Wrench },
      { href: "/app/platform/audit", label: "Audit log", icon: ShieldCheck },
    ],
  },
];

function Nav({ onNavigate }: { onNavigate?: () => void }) {
  const [loc] = useLocation();
  const { user, org, can } = useSession();
  return (
    <nav aria-label="Console" className="flex flex-col gap-5 p-3 text-sm">
      {groups
        .filter((g) => (g.operator ? user?.operator : !!org))
        .map((g) => (
          <div key={g.label}>
            <p className="px-2 pb-1 text-xs text-muted-foreground">{g.label}</p>
            {g.items
              .filter((i) => !i.min || can(i.min))
              .map((i) => {
                const active = loc === i.href || loc.startsWith(i.href + "/");
                return (
                  <Link
                    key={i.href}
                    href={i.href}
                    onClick={onNavigate}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "flex items-center gap-2 rounded-md px-2 py-1.5 text-muted-foreground hover:bg-accent hover:text-foreground",
                      active && "bg-accent font-medium text-foreground",
                    )}
                  >
                    <i.icon className="size-4" aria-hidden />
                    {i.label}
                  </Link>
                );
              })}
          </div>
        ))}
    </nav>
  );
}

export function ConsoleLayout({ children }: { children: ReactNode }) {
  const { user, orgs, org, setOrg, refresh, loading } = useSession();
  const [, navigate] = useLocation();
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!loading && !user) navigate(`/login?next=${encodeURIComponent(location.pathname)}`);
  }, [loading, user, navigate]);
  if (!user) return null;

  return (
    <div className="flex min-h-dvh flex-col">
      <header className="sticky top-0 z-40 flex h-14 items-center gap-3 border-b bg-background px-4">
        <Button variant="ghost" size="icon" className="lg:hidden" aria-label="Open menu" onClick={() => setOpen(true)}>
          <Menu />
        </Button>
        <Wordmark />
        {orgs.length > 0 && (
          <Select value={org?.id} onValueChange={setOrg}>
            <SelectTrigger size="sm" className="ml-1 w-32 sm:ml-2 sm:w-48" aria-label="Organization">
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
      <div className="flex flex-1">
        <aside className="hidden w-60 shrink-0 border-r bg-sidebar lg:block">
          <div className="sticky top-14">
            <Nav />
          </div>
        </aside>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetContent side="left" className="w-64 p-0">
            <SheetTitle className="px-5 pt-4">Menu</SheetTitle>
            <Nav onNavigate={() => setOpen(false)} />
          </SheetContent>
        </Sheet>
        <main className="min-w-0 flex-1 px-4 py-6 md:px-8">
          <div className="mx-auto max-w-6xl">{children}</div>
        </main>
      </div>
    </div>
  );
}
