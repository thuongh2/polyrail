import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { useLocation } from "wouter";
import { CornerDownLeft, KeyRound, Plug, Plus, Server } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { usePaletteEntries, type Item } from "./nav";

type Entry = { id: string; label: string; group: string; icon: Item["icon"]; href: string; keywords?: string };

/**
 * Subsequence match score of q in text (0 = no match). Word starts and consecutive letters score more,
 * so "apk" finds "API keys" and "prox" ranks "Proxies" above "Providers".
 */
export function score(q: string, text: string) {
  const t = text.toLowerCase();
  if (!q) return 1;
  if (t.startsWith(q)) return 1000 - t.length;
  const at = t.indexOf(q);
  if (at >= 0) return 500 - at;
  let s = 0, j = 0, prev = -2;
  for (let i = 0; i < t.length && j < q.length; i++) {
    if (t[i] !== q[j]) continue;
    s += i === prev + 1 ? 3 : i === 0 || t[i - 1] === " " ? 2 : 1;
    prev = i;
    j++;
  }
  return j === q.length ? s : 0;
}

/** Ctrl/Cmd+K: every console page the user may open plus a few create actions, filtered as they type. */
export default function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const [, navigate] = useLocation();
  const { org, wsCan, user } = useSession();
  const pages = usePaletteEntries();
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const list = useRef<HTMLUListElement>(null);

  const entries = useMemo<Entry[]>(() => {
    const actions: Entry[] = [];
    // Quick actions open the page's create dialog through ?new=1 (useNewParam on the page).
    if (org && wsCan("member")) actions.push({ id: "a-key", label: "Create API key", group: "Action", icon: KeyRound, href: "/app/keys?new=1" });
    if (org && wsCan("admin")) actions.push({ id: "a-prov", label: "Add provider", group: "Action", icon: Plug, href: "/app/providers?new=1" });
    if (user?.operator)
      actions.push({ id: "a-pprov", label: "Add public provider", group: "Action", icon: Server, href: "/app/platform/providers?new=1" });
    return [
      ...pages.map(({ item, group }) => ({ id: item.href, label: item.label, group, icon: item.icon, href: item.href, keywords: item.keywords })),
      ...actions,
    ];
  }, [pages, org, wsCan, user]);

  const results = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return entries;
    return entries
      .map((e) => ({ e, s: Math.max(score(needle, e.label) * 2, score(needle, `${e.group} ${e.label} ${e.keywords ?? ""}`)) }))
      .filter((r) => r.s > 0)
      .sort((a, b) => b.s - a.s)
      .map((r) => r.e);
  }, [q, entries]);

  useEffect(() => setActive(0), [q]);
  useEffect(() => {
    if (open) setQ("");
  }, [open]);
  useEffect(() => {
    list.current?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const go = (e: Entry | undefined) => {
    if (!e) return;
    onOpenChange(false);
    if (e.href.startsWith("/app/")) navigate(e.href);
    else location.assign(e.href);
  };

  const onKey = (ev: KeyboardEvent<HTMLInputElement>) => {
    const n = results.length;
    if (ev.key === "ArrowDown") setActive((a) => (n ? (a + 1) % n : 0));
    else if (ev.key === "ArrowUp") setActive((a) => (n ? (a - 1 + n) % n : 0));
    else if (ev.key === "Home") setActive(0);
    else if (ev.key === "End") setActive(Math.max(n - 1, 0));
    else if (ev.key === "Enter") go(results[active]);
    else return;
    ev.preventDefault();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="top-[15%] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-xl" showCloseButton={false}>
        <DialogTitle className="sr-only">Go to a page</DialogTitle>
        <DialogDescription className="sr-only">Type to filter, arrow keys to move, Enter to open, Escape to close.</DialogDescription>
        <input
          autoFocus
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
          placeholder="Search pages and actions…"
          aria-label="Search pages and actions"
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-list"
          aria-autocomplete="list"
          aria-activedescendant={results[active] ? `palette-${active}` : undefined}
          className="h-12 w-full border-b bg-transparent px-4 text-sm outline-none placeholder:text-muted-foreground"
        />
        <ul ref={list} id="palette-list" role="listbox" aria-label="Pages" className="scrollbar-thin max-h-[min(60vh,420px)] overflow-y-auto p-1.5">
          {results.map((e, i) => (
            <li
              key={e.id}
              id={`palette-${i}`}
              data-index={i}
              role="option"
              aria-selected={i === active}
              onMouseMove={() => i !== active && setActive(i)}
              onClick={() => go(e)}
              className={cn(
                "flex h-9 cursor-pointer items-center gap-3 rounded-md px-2.5 text-sm",
                i === active && "bg-accent text-accent-foreground",
              )}
            >
              {e.group === "Action" ? <Plus className="size-4 shrink-0 text-muted-foreground" aria-hidden /> : <e.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />}
              <span className="truncate">{e.label}</span>
              <span className="ml-auto shrink-0 text-xs text-muted-foreground">{e.group}</span>
              {i === active && <CornerDownLeft className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />}
            </li>
          ))}
          {!results.length && <li className="px-2.5 py-6 text-center text-sm text-muted-foreground">No page matches “{q}”.</li>}
        </ul>
      </DialogContent>
    </Dialog>
  );
}
