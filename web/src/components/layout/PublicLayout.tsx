import type { ReactNode } from "react";
import { Link, useLocation } from "wouter";
import { Button } from "@/components/ui/button";
import { useSession } from "@/lib/session";
import { ThemeToggle } from "./ThemeToggle";
import { cn } from "@/lib/utils";

const nav = [
  { href: "/models", label: "Models" },
  { href: "/status", label: "Status" },
  { href: "/docs", label: "Docs" },
];

export function Wordmark() {
  return (
    <Link href="/" className="flex items-center gap-2 font-semibold tracking-tight">
      <span aria-hidden className="grid size-6 place-items-center rounded-md bg-foreground text-[11px] font-bold text-background">
        ag
      </span>
      <span className="hidden sm:inline">agent-gateway</span>
    </Link>
  );
}

export function PublicLayout({ children }: { children: ReactNode }) {
  const [loc] = useLocation();
  const { user } = useSession();
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="sticky top-0 z-40 border-b bg-background/90 backdrop-blur">
        <div className="mx-auto flex h-14 max-w-7xl items-center gap-3 px-4 sm:gap-6">
          <Wordmark />
          <nav aria-label="Main" className="flex items-center gap-1 text-sm">
            {nav.map((n) => (
              <Link
                key={n.href}
                href={n.href}
                aria-current={loc.startsWith(n.href) ? "page" : undefined}
                className={cn(
                  "rounded-md px-3 py-1.5 text-muted-foreground hover:text-foreground",
                  loc.startsWith(n.href) && "bg-accent text-foreground",
                )}
              >
                {n.label}
              </Link>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <ThemeToggle />
            {user ? (
              <Button asChild size="sm">
                <Link href="/app">Console</Link>
              </Button>
            ) : (
              <Button asChild size="sm" variant="outline">
                <Link href="/login">Sign in</Link>
              </Button>
            )}
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-8">{children}</main>
      <footer className="border-t">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-4 px-4 py-6 text-sm text-muted-foreground">
          <span>agent-gateway, open source under Apache-2.0</span>
          <Link href="/models" className="hover:text-foreground">Models</Link>
          <Link href="/status" className="hover:text-foreground">Status</Link>
          <Link href="/docs" className="hover:text-foreground">Docs</Link>
        </div>
      </footer>
    </div>
  );
}
