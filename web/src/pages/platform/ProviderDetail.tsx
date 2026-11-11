// Provider detail: its stored keys with live status, bulk actions, tests and per-key charts (docs/KEYS.md "UI").
import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "wouter";
import {
  ArrowLeft, BarChart3, Check, Ellipsis, Loader2, Pencil, Plus, Search, Stethoscope, Trash2, X,
} from "lucide-react";
import type { Scope } from "@/App";
import { BarChart } from "@/components/console/BarChart";
import { CopyButton, ms } from "@/components/console/bits";
import { ConfirmDialog, Field, FormDialog, NoAccess, splitList } from "@/components/platform/kit";
import { ProviderLogo } from "@/components/ProviderLogo";
import { DataState, EmptyState, ErrorState, LoadingRows } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import {
  ago, compact, fmt, get, keysApi, type KeyBulkAction, type KeyStatus, type Provider, type ProviderKeyView,
} from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { providerLabel } from "@/lib/providers";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { isCloud, ProviderDialog, providersPath } from "./ProvidersAdmin";

const POLL_MS = 10_000;
const PAGE = 60;
const MAX_ADD = 1000;

type Tab = "all" | KeyStatus;
const tabs: [Tab, string][] = [
  ["all", "All"],
  ["available", "Available"],
  ["cooldown", "Cooldown"],
  ["quarantined", "Quarantined"],
  ["disabled", "Disabled"],
];
type Sort = "attention" | "recent" | "name";
const sorts: [Sort, string][] = [
  ["attention", "Needs attention first"],
  ["recent", "Recently used"],
  ["name", "Name"],
];
const attentionRank: Record<KeyStatus, number> = { quarantined: 0, cooldown: 1, available: 2, disabled: 3 };

const statusText: Record<KeyStatus, string> = {
  available: "Available",
  cooldown: "Cooldown",
  quarantined: "Quarantined",
  disabled: "Disabled",
};
const statusDot: Record<KeyStatus, string> = {
  available: "bg-success",
  cooldown: "bg-warning",
  quarantined: "bg-danger",
  disabled: "bg-nodata",
};
const statusInk: Record<KeyStatus, string> = {
  available: "text-success",
  cooldown: "text-foreground",
  quarantined: "text-danger",
  disabled: "text-muted-foreground",
};

/**
 * Loads fn now and every `ms` while the tab is visible (and again when it becomes visible).
 * A failed refresh keeps the last data and reports the error separately.
 */
function usePolled<T>(fn: () => Promise<T>, deps: unknown[], ms: number) {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<Error>();
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    let seq = 0;
    const run = () => {
      if (document.hidden) return;
      const n = ++seq;
      fn().then(
        (d) => live && n === seq && (setData(d), setError(undefined)),
        (e: Error) => live && n === seq && setError(e),
      );
    };
    run();
    const timer = setInterval(run, ms);
    const onVisible = () => !document.hidden && run();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      live = false;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  return { data, error, setData, reload: () => setTick((t) => t + 1) };
}

/** Current time, ticking every second while `on` (for cooldown countdowns). */
function useNow(on: boolean) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

const clock = (t: string) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });

export default function ProviderDetail({ scope = "org", id }: { scope?: Scope; id: string }) {
  const { org, can, user } = useSession();
  const base = providersPath(scope, org?.id);
  const allowed = scope === "platform" ? !!user?.operator : can("admin");
  const back = scope === "platform" ? "/platform/providers" : "/providers";

  if (!allowed || !base)
    return (
      <>
        <BackLink href={back} />
        <NoAccess>{scope === "platform" ? "Only operators manage public providers." : "Only organization admins manage providers."}</NoAccess>
      </>
    );
  return <Detail key={base + id} scope={scope} base={base} id={id} back={back} />;
}

function BackLink({ href }: { href: string }) {
  return (
    <Link href={href} className="mb-3 inline-flex items-center gap-1 rounded-sm text-sm text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring">
      <ArrowLeft className="size-4" aria-hidden /> Providers
    </Link>
  );
}

function Detail({ scope, base, id, back }: { scope: Scope; base: string; id: string; back: string }) {
  const provider = useAsync(() => get<Provider>(`${base}/${encodeURIComponent(id)}`).then((p) => {
    if (!p?.id) throw new Error("This provider was not found.");
    return p;
  }), [base, id]);
  const kapi = useMemo(() => keysApi(base, id), [base, id]);
  const keys = usePolled(() => kapi.list(), [kapi], POLL_MS);

  const [tab, setTab] = useQueryParam("status", "all");
  const [q, setQ] = useQueryParam("q");
  const [sort, setSort] = useQueryParam("sort", "attention");
  const [page, setPage] = useState(0);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [adding, setAdding] = useState(false);
  const [editingProvider, setEditingProvider] = useState(false);
  const [editing, setEditing] = useState<ProviderKeyView | null>(null);
  const [deleting, setDeleting] = useState<ProviderKeyView | null>(null);
  const [charting, setCharting] = useState<ProviderKeyView | null>(null);
  const [bulkDelete, setBulkDelete] = useState(false);
  const [testing, setTesting] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);

  const all = keys.data ?? [];
  const now = useNow(all.some((k) => k.status === "cooldown"));
  const counts = useMemo(() => {
    const c: Record<Tab, number> = { all: all.length, available: 0, cooldown: 0, quarantined: 0, disabled: 0 };
    for (const k of all) c[k.status] = (c[k.status] ?? 0) + 1;
    return c;
  }, [all]);
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const list = all.filter(
      (k) => (tab === "all" || k.status === tab) && (!needle || k.name.toLowerCase().includes(needle) || k.masked.toLowerCase().includes(needle)),
    );
    const byName = (a: ProviderKeyView, b: ProviderKeyView) => (a.name || a.masked).localeCompare(b.name || b.masked);
    const used = (k: ProviderKeyView) => (k.last_used_at ? new Date(k.last_used_at).getTime() : 0);
    return list.sort(
      sort === "name"
        ? byName
        : sort === "recent"
          ? (a, b) => used(b) - used(a) || byName(a, b)
          : (a, b) => attentionRank[a.status] - attentionRank[b.status] || b.stats_24h.errors - a.stats_24h.errors || byName(a, b),
    );
  }, [all, tab, q, sort]);
  const pages = Math.max(1, Math.ceil(shown.length / PAGE));
  const pageNo = Math.min(page, pages - 1);
  const visible = shown.slice(pageNo * PAGE, pageNo * PAGE + PAGE);
  useEffect(() => setPage(0), [tab, q, sort]);
  // Drop selected ids that no longer exist (deleted elsewhere).
  useEffect(() => {
    if (!keys.data) return;
    const ids = new Set(keys.data.map((k) => k.id));
    setSelected((s) => (Array.from(s).every((x) => ids.has(x)) ? s : new Set(Array.from(s).filter((x) => ids.has(x)))));
  }, [keys.data]);

  if (provider.error) {
    return (
      <>
        <BackLink href={back} />
        <ErrorState error={provider.error} onRetry={provider.reload} />
      </>
    );
  }
  const p = provider.data;
  const cloud = !!p && isCloud(p.type);
  const available = counts.available;
  const pageAllSelected = visible.length > 0 && visible.every((k) => selected.has(k.id));
  const pageSomeSelected = visible.some((k) => selected.has(k.id));

  const toggle = (kid: string, on: boolean) =>
    setSelected((s) => {
      const n = new Set(s);
      if (on) n.add(kid);
      else n.delete(kid);
      return n;
    });

  const setEnabled = async (k: ProviderKeyView, enabled: boolean) => {
    const patch = (e: boolean) =>
      keys.setData((ks) =>
        ks?.map((x) => (x.id === k.id ? { ...x, enabled: e, status: e ? (x.status === "disabled" ? "available" : x.status) : "disabled" } : x)),
      );
    patch(enabled);
    try {
      await kapi.update(k.id, { enabled });
      toast(enabled ? "Key enabled" : "Key disabled");
    } catch (e) {
      keys.setData((ks) => ks?.map((x) => (x.id === k.id ? k : x)));
      toast((e as Error).message, { error: true });
    }
    keys.reload();
  };

  const test = async (k: ProviderKeyView) => {
    setTesting((s) => new Set(s).add(k.id));
    try {
      const r = await kapi.test(k.id);
      if (r.ok) toast(`${k.masked}: OK in ${ms(r.latency_ms)}`);
      else toast(`${k.masked}: ${r.error || `HTTP ${r.status}`}`, { error: true });
    } catch (e) {
      toast((e as Error).message, { error: true });
    } finally {
      setTesting((s) => {
        const n = new Set(s);
        n.delete(k.id);
        return n;
      });
      keys.reload();
    }
  };

  const runBulk = async (action: KeyBulkAction) => {
    const ids = Array.from(selected);
    setBulkBusy(true);
    try {
      const results = await kapi.bulk(action, ids);
      const failed = results.filter((r) => !r.ok).length;
      const verb = { enable: "Enabled", disable: "Disabled", delete: "Deleted", test: "Tested", clear: "Cleared status of" }[action];
      const n = results.length || ids.length;
      if (action === "test") toast(`Tested ${n} key${n === 1 ? "" : "s"}: ${n - failed} OK, ${failed} failed`, { error: failed > 0 });
      else toast(failed ? `${verb} ${n - failed} of ${n} keys; ${failed} failed` : `${verb} ${n} key${n === 1 ? "" : "s"}`, { error: failed > 0 });
      if (action === "delete") setSelected(new Set());
    } catch (e) {
      if (action === "delete") throw e; // shown in the confirm dialog
      toast((e as Error).message, { error: true });
    } finally {
      setBulkBusy(false);
      keys.reload();
    }
  };

  const addButton = !cloud && (
    <Button onClick={() => setAdding(true)}>
      <Plus aria-hidden /> Add keys
    </Button>
  );

  return (
    <div className={cn(selected.size > 0 && "pb-20")}>
      <BackLink href={back} />
      {/* header */}
      <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
        {p ? (
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <ProviderLogo type={p.type} size={24} />
              <h1 className="truncate text-2xl font-semibold tracking-tight">{p.name}</h1>
              <ProviderStatus enabled={p.enabled} available={keys.data ? available : undefined} />
              <Badge variant="secondary" title={p.type}>{providerLabel(p.type)}</Badge>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
              {!cloud && (
                <span className="flex min-w-0 items-center gap-2">
                  <span className="truncate font-mono text-xs" title={p.base_url}>{p.base_url || "Provider default URL"}</span>
                  {p.base_url && <CopyButton text={p.base_url} />}
                </span>
              )}
              <span className="tabular-nums">{fmt(all.length || p.keys?.length)} keys</span>
              {p.timeout_seconds ? <span className="tabular-nums">Timeout {p.timeout_seconds} s</span> : null}
            </div>
          </div>
        ) : (
          <div className="w-full max-w-md"><LoadingRows rows={2} /></div>
        )}
        <div className="flex gap-2">
          {p && (
            <Button variant="outline" onClick={() => setEditingProvider(true)}>
              <Pencil aria-hidden /> Edit provider
            </Button>
          )}
          {addButton}
        </div>
      </div>
      {cloud && (
        <p className="mb-4 rounded-lg border px-4 py-3 text-sm text-muted-foreground">
          {providerLabel(p!.type)} keys hold cloud credentials: add or change them with Edit provider. They are listed here by name.
        </p>
      )}

      {/* toolbar */}
      {/* On narrow screens the tab row scrolls sideways instead of wrapping under its fixed height. */}
      <Tabs value={tab} onValueChange={setTab} className="mb-3 max-w-full overflow-x-auto no-scrollbar">
        <TabsList aria-label="Key status" className="w-max">
          {tabs.map(([v, label]) => (
            <TabsTrigger key={v} value={v} className="flex-none">
              {label} <span className="tabular-nums text-muted-foreground">{counts[v]}</span>
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={pageAllSelected ? true : pageSomeSelected ? "indeterminate" : false}
            disabled={visible.length === 0}
            onCheckedChange={(c) =>
              setSelected((s) => {
                const n = new Set(s);
                for (const k of visible) (c === true ? n.add(k.id) : n.delete(k.id));
                return n;
              })
            }
          />
          Select this page
        </label>
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input
            type="search"
            aria-label="Search keys by name or masked value"
            placeholder="Search name or key"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="pl-8"
          />
        </div>
        <Select value={sort} onValueChange={setSort}>
          <SelectTrigger className="w-full sm:w-56" aria-label="Sort keys">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {sorts.map(([v, label]) => (
              <SelectItem key={v} value={v}>{label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {keys.error && keys.data && (
        <p role="status" className="mb-3 text-sm text-danger">Could not refresh the key list: {keys.error.message}. Showing the last loaded state.</p>
      )}

      <DataState
        state={{ data: keys.data, error: keys.data ? undefined : keys.error, loading: !keys.data, reload: keys.reload }}
        rows={4}
        empty={
          <EmptyState title="No keys yet" action={addButton || undefined}>
            {cloud ? "Add credentials with Edit provider." : "Paste one or many API keys. Requests spread across them by weight."}
          </EmptyState>
        }
      >
        {() =>
          shown.length === 0 ? (
            <p className="rounded-lg border px-4 py-8 text-center text-sm text-muted-foreground">No keys match these filters.</p>
          ) : (
            <>
              <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {visible.map((k) => (
                  <KeyCard
                    key={k.id}
                    k={k}
                    now={now}
                    selected={selected.has(k.id)}
                    testing={testing.has(k.id)}
                    onSelect={(on) => toggle(k.id, on)}
                    onEnabled={(on) => setEnabled(k, on)}
                    onTest={() => test(k)}
                    onChart={() => setCharting(k)}
                    onEdit={() => setEditing(k)}
                    onDelete={() => setDeleting(k)}
                  />
                ))}
              </ul>
              {pages > 1 && (
                <div className="mt-4 flex items-center justify-between text-sm text-muted-foreground">
                  <span className="tabular-nums">
                    {pageNo * PAGE + 1}–{Math.min(shown.length, (pageNo + 1) * PAGE)} of {shown.length}
                  </span>
                  <div className="flex gap-2">
                    <Button variant="outline" size="sm" disabled={pageNo === 0} onClick={() => setPage(pageNo - 1)}>Previous</Button>
                    <Button variant="outline" size="sm" disabled={pageNo >= pages - 1} onClick={() => setPage(pageNo + 1)}>Next</Button>
                  </div>
                </div>
              )}
            </>
          )
        }
      </DataState>

      {selected.size > 0 && (
        <div
          role="region"
          aria-label="Bulk actions"
          className="sticky bottom-3 z-10 mt-4 flex flex-wrap items-center gap-2 rounded-lg border bg-background px-3 py-2 shadow-lg"
        >
          <span className="mr-auto text-sm font-medium tabular-nums">{selected.size} selected</span>
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => runBulk("enable")}>Enable</Button>
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => runBulk("disable")}>Disable</Button>
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => runBulk("test")}>
            {bulkBusy ? <Loader2 className="motion-safe:animate-spin" aria-hidden /> : <Stethoscope aria-hidden />} Test
          </Button>
          <Button size="sm" variant="outline" disabled={bulkBusy} onClick={() => runBulk("clear")}>Clear status</Button>
          <Button size="sm" variant="destructive" disabled={bulkBusy} onClick={() => setBulkDelete(true)}>
            <Trash2 aria-hidden /> Delete
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="Clear selection" onClick={() => setSelected(new Set())}>
            <X aria-hidden />
          </Button>
        </div>
      )}

      {adding && <AddKeysDialog onClose={() => setAdding(false)} add={kapi.add} onAdded={keys.reload} />}
      {editing && <EditKeyDialog key={editing.id} k={editing} onClose={() => setEditing(null)} save={(b) => kapi.update(editing.id, b)} onSaved={keys.reload} />}
      {editingProvider && p && (
        <ProviderDialog
          scope={scope}
          base={base}
          provider={p}
          onClose={() => setEditingProvider(false)}
          onSaved={() => {
            provider.reload();
            keys.reload();
          }}
        />
      )}
      <KeyChartSheet k={charting} onClose={() => setCharting(null)} stats={kapi.stats} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete key ${deleting?.masked ?? ""}?`}
        description="The key is removed from this provider immediately and can't be recovered. Requests use the remaining keys."
        confirmLabel="Delete key"
        success="Key deleted"
        onConfirm={async () => {
          await kapi.remove(deleting!.id);
          toggle(deleting!.id, false);
          keys.reload();
        }}
      />
      <ConfirmDialog
        open={bulkDelete}
        onOpenChange={setBulkDelete}
        title={`Delete ${selected.size} key${selected.size === 1 ? "" : "s"}?`}
        description="The selected keys are removed from this provider immediately and can't be recovered."
        confirmLabel="Delete keys"
        success="Delete finished"
        onConfirm={() => runBulk("delete")}
      />
    </div>
  );
}

function ProviderStatus({ enabled, available }: { enabled: boolean; available?: number }) {
  const [text, cls] = !enabled
    ? ["Disabled", "text-muted-foreground"]
    : available === undefined
      ? ["Enabled", "text-muted-foreground"]
      : available > 0
        ? ["Ready", "border-success/50 text-success"]
        : ["No available key", "border-danger/50 text-danger"];
  return <Badge variant="outline" className={cls}>{text}</Badge>;
}

function KeyCard({
  k, now, selected, testing, onSelect, onEnabled, onTest, onChart, onEdit, onDelete,
}: {
  k: ProviderKeyView;
  now: number;
  selected: boolean;
  testing: boolean;
  onSelect: (on: boolean) => void;
  onEnabled: (on: boolean) => void;
  onTest: () => void;
  onChart: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const s = k.stats_24h ?? { requests: 0, errors: 0, avg_latency_ms: 0 };
  const left = k.cooldown_until ? Math.max(0, Math.round((new Date(k.cooldown_until).getTime() - now) / 1000)) : 0;
  const label = k.masked || k.name;
  return (
    <li className={cn("flex flex-col gap-3 rounded-lg border bg-background p-4", selected && "border-ring")}>
      <div className="flex items-start gap-3">
        <Checkbox className="mt-1" checked={selected} onCheckedChange={(c) => onSelect(c === true)} aria-label={`Select key ${label}`} />
        <div className="min-w-0 flex-1">
          <p className="truncate font-mono text-sm" title={label}>{label}</p>
          {k.name && k.name !== k.masked && <p className="truncate text-xs text-muted-foreground" title={k.name}>{k.name}</p>}
        </div>
        <Switch checked={k.enabled} onCheckedChange={onEnabled} aria-label={`${k.enabled ? "Disable" : "Enable"} key ${label}`} />
      </div>
      <div className="text-sm">
        <span className={cn("inline-flex items-center gap-1.5 font-medium", statusInk[k.status])}>
          <span className={cn("size-2 rounded-full", statusDot[k.status])} aria-hidden />
          {statusText[k.status] ?? k.status}
          {k.status === "cooldown" && k.cooldown_until && (
            <span className="font-normal text-muted-foreground tabular-nums">
              until {clock(k.cooldown_until)}{left > 0 ? ` (${left} s)` : ""}
            </span>
          )}
        </span>
        {k.status_reason && (k.status === "quarantined" || k.status === "cooldown") && (
          <p className={cn("mt-1 line-clamp-2 text-xs", k.status === "quarantined" ? "text-danger" : "text-muted-foreground")} title={k.status_reason}>
            {k.status_reason}
          </p>
        )}
      </div>
      <dl className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <dt className="text-muted-foreground">Last used</dt>
          <dd title={k.last_used_at ? new Date(k.last_used_at).toLocaleString() : undefined}>{k.last_used_at ? ago(k.last_used_at, now) : "Never"}</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">Weight</dt>
          <dd className="tabular-nums">{k.weight}</dd>
        </div>
      </dl>
      <div className="mt-auto flex items-center justify-between gap-2 border-t pt-3">
        <p className="flex items-center gap-2 text-xs tabular-nums text-muted-foreground">
          <span>24 h</span>
          <span className="inline-flex items-center gap-0.5 text-success" title="Successful requests in 24 hours">
            <Check className="size-3.5" aria-hidden /><span className="sr-only">succeeded</span>{compact(Math.max(0, s.requests - s.errors))}
          </span>
          <span className={cn("inline-flex items-center gap-0.5", s.errors ? "text-danger" : "")} title="Failed requests in 24 hours">
            <X className="size-3.5" aria-hidden /><span className="sr-only">failed</span>{compact(s.errors)}
          </span>
          {s.requests > 0 && <span title="Average latency in 24 hours">{ms(s.avg_latency_ms)}</span>}
        </p>
        <div className="flex gap-0.5">
          <Button size="icon-sm" variant="ghost" aria-label={`Usage chart for ${label}`} title="Usage chart" onClick={onChart}>
            <BarChart3 aria-hidden />
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label={`Test ${label}`} title="Test" disabled={testing} onClick={onTest}>
            {testing ? <Loader2 className="motion-safe:animate-spin" aria-hidden /> : <Stethoscope aria-hidden />}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="icon-sm" variant="ghost" aria-label={`More actions for ${label}`} title="More">
                <Ellipsis aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem onSelect={onEdit}>
                <Pencil aria-hidden /> Edit
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={onDelete}>
                <Trash2 aria-hidden /> Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </li>
  );
}

/** Lines or commas → distinct non-empty values. */
const parseKeys = (text: string) => Array.from(new Set(splitList(text)));

function AddKeysDialog({
  onClose, add, onAdded,
}: {
  onClose: () => void;
  add: (keys: string, opts: { weight?: number; models?: string[] }) => Promise<{ added: number; duplicates: number; invalid: number }>;
  onAdded: () => void;
}) {
  const [text, setText] = useState("");
  const [weight, setWeight] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<{ added: number; duplicates: number; invalid: number } | null>(null);
  const n = parseKeys(text).length;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (n === 0) return setErr("Paste at least one key.");
    if (n > MAX_ADD) return setErr(`At most ${MAX_ADD} keys at a time; this has ${n}.`);
    setBusy(true);
    setErr("");
    try {
      const r = await add(parseKeys(text).join("\n"), weight ? { weight: Number(weight) } : {});
      setResult(r);
      setText("");
      onAdded();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto scrollbar-thin sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add keys</DialogTitle>
          <DialogDescription>One key per line, or separated by commas. Duplicates of stored keys are skipped. Keys are encrypted and never shown again.</DialogDescription>
        </DialogHeader>
        {result ? (
          <>
            <p role="status" className="rounded-md border px-3 py-3 text-sm tabular-nums">
              Added {fmt(result.added)} · {fmt(result.duplicates)} duplicate{result.duplicates === 1 ? "" : "s"} · {fmt(result.invalid)} invalid
            </p>
            <DialogFooter>
              <Button variant="outline" onClick={() => setResult(null)}>Add more</Button>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="grid gap-4">
            <Field label="Keys" hint={`${fmt(n)} key${n === 1 ? "" : "s"} detected (at most ${MAX_ADD}).`}>
              {(id, hint) => (
                <Textarea
                  id={id}
                  aria-describedby={hint}
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  rows={8}
                  className="font-mono text-xs"
                  placeholder={"sk-...\nsk-..."}
                  autoComplete="off"
                  spellCheck={false}
                  autoFocus
                />
              )}
            </Field>
            <Field label="Weight" hint="Optional. Higher weight gets a larger share of requests.">
              {(id, hint) => (
                <Input id={id} aria-describedby={hint} type="number" min={1} max={1000} value={weight} onChange={(e) => setWeight(e.target.value)} className="w-32" />
              )}
            </Field>
            {err && <p role="alert" className="text-sm text-destructive">{err}</p>}
            <DialogFooter>
              <Button type="button" variant="outline" disabled={busy} onClick={onClose}>Cancel</Button>
              <Button type="submit" disabled={busy}>{busy ? "Adding…" : n > 0 ? `Add ${fmt(n)} key${n === 1 ? "" : "s"}` : "Add keys"}</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function EditKeyDialog({
  k, onClose, save, onSaved,
}: {
  k: ProviderKeyView;
  onClose: () => void;
  save: (b: { name?: string; weight?: number; models?: string[] }) => Promise<unknown>;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(true);
  const [name, setName] = useState(k.name ?? "");
  const [weight, setWeight] = useState(String(k.weight ?? 1));
  const [models, setModels] = useState((k.models ?? []).join(", "));
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title={`Edit key ${k.masked}`}
      submitLabel="Save changes"
      success="Key saved"
      onSubmit={async () => {
        await save({ name: name.trim(), weight: Number(weight) || 1, models: splitList(models) });
        onSaved();
      }}
    >
      <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />}</Field>
      <Field label="Weight" hint="Share of requests relative to the provider's other keys.">
        {(id, hint) => <Input id={id} aria-describedby={hint} type="number" min={1} max={1000} required value={weight} onChange={(e) => setWeight(e.target.value)} className="w-32" />}
      </Field>
      <Field label="Models" hint="Comma separated upstream model names this key serves. Empty serves every model.">
        {(id, hint) => <Input id={id} aria-describedby={hint} value={models} onChange={(e) => setModels(e.target.value)} placeholder="All models" autoComplete="off" spellCheck={false} />}
      </Field>
    </FormDialog>
  );
}

/** Hour label for axis ticks ("Tue 14h"); RFC3339 or yyyymmddhh. */
function hourDate(h: string) {
  if (/^\d{10}$/.test(h)) return new Date(Date.UTC(+h.slice(0, 4), +h.slice(4, 6) - 1, +h.slice(6, 8), +h.slice(8, 10)));
  return new Date(h);
}
const hourTick = (d: Date) => `${d.toLocaleDateString([], { weekday: "short" })} ${String(d.getHours()).padStart(2, "0")}h`;

function KeyChartSheet({
  k, onClose, stats,
}: {
  k: ProviderKeyView | null;
  onClose: () => void;
  stats: (id: string, hours?: number) => Promise<{ hour: string; requests: number; errors: number; avg_latency_ms: number; in_tokens: number; out_tokens: number }[]>;
}) {
  const last = useRef(k);
  if (k) last.current = k;
  const shown = k ?? last.current;
  const data = useAsync(() => (k ? stats(k.id, 72) : Promise.resolve([])), [k?.id]);
  const pts = (data.data ?? []).map((p) => ({ ...p, d: hourDate(p.hour) }));
  const total = pts.reduce((a, p) => ({ r: a.r + p.requests, e: a.e + p.errors, lat: a.lat + p.avg_latency_ms * p.requests, i: a.i + p.in_tokens, o: a.o + p.out_tokens }), { r: 0, e: 0, lat: 0, i: 0, o: 0 });
  const title = (p: (typeof pts)[number], v: string) => `${p.d.toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })}: ${v}`;
  const int = (v: number) => compact(v);

  return (
    <Sheet open={!!k} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto scrollbar-thin sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle className="font-mono">{shown?.masked}</SheetTitle>
          <SheetDescription>Hourly requests, errors and latency over the last 72 hours.</SheetDescription>
        </SheetHeader>
        <div className="grid gap-6 px-4 pb-6">
          {data.error ? (
            <ErrorState error={data.error} onRetry={data.reload} />
          ) : !data.data ? (
            <LoadingRows rows={4} />
          ) : (
            <>
              <dl className="grid grid-cols-2 gap-3 text-sm sm:grid-cols-4">
                <div><dt className="text-xs text-muted-foreground">Requests</dt><dd className="font-semibold tabular-nums">{fmt(total.r)}</dd></div>
                <div><dt className="text-xs text-muted-foreground">Errors</dt><dd className="font-semibold tabular-nums">{fmt(total.e)}{total.r ? ` (${((total.e / total.r) * 100).toFixed(1)}%)` : ""}</dd></div>
                <div><dt className="text-xs text-muted-foreground">Avg latency</dt><dd className="font-semibold tabular-nums">{total.r ? ms(total.lat / total.r) : "–"}</dd></div>
                <div><dt className="text-xs text-muted-foreground">Tokens in / out</dt><dd className="font-semibold tabular-nums">{compact(total.i)} / {compact(total.o)}</dd></div>
              </dl>
              {pts.length === 0 ? (
                <p className="text-sm text-muted-foreground">No traffic recorded for this key yet.</p>
              ) : (
                <>
                  <Chart title="Requests">
                    <BarChart height={150} label="Requests per hour" format={int} bars={pts.map((p) => ({ label: hourTick(p.d), value: p.requests, title: title(p, `${fmt(p.requests)} requests`) }))} />
                  </Chart>
                  <Chart title="Errors">
                    <BarChart height={120} tone="fill-danger" label="Errors per hour" format={int} bars={pts.map((p) => ({ label: hourTick(p.d), value: p.errors, title: title(p, `${fmt(p.errors)} errors`) }))} />
                  </Chart>
                  <Chart title="Average latency">
                    <BarChart height={120} tone="fill-warning" label="Average latency per hour" format={(v) => ms(v)} bars={pts.map((p) => ({ label: hourTick(p.d), value: p.avg_latency_ms, title: title(p, p.requests ? ms(p.avg_latency_ms) : "no requests") }))} />
                  </Chart>
                </>
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function Chart({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h3 className="mb-2 text-sm font-medium">{title}</h3>
      {children}
    </section>
  );
}
