import { useState, type ReactNode } from "react";
import { ChevronDown, Download, Terminal } from "lucide-react";
import { CodeTabs, toolSamples } from "@/components/public/CodeTabs";
import type { Scope } from "@/App";
import { BarChart } from "@/components/console/BarChart";
import { Section, Stat, StatStrip, TableFrame, dayDate, money, ms, usdTick } from "@/components/console/bits";
import { DataState, EmptyState, ErrorState, LoadingRows, PageHeader } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PLATFORM, publicApi, compact, fmt, get, orgBase, pct, usd, type APIKey, type Org, type UsageReport, type UsageRow } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { Link } from "wouter";

// "1h"/"3h"/"6h" read hourly rollups (?hours=); the rest are days.
const PERIODS = ["1h", "3h", "6h", "7", "30", "90"];
const periodLabel = (p: string) => (p.endsWith("h") ? `Last ${p.slice(0, -1)} hour${p === "1h" ? "" : "s"}` : `Last ${p} days`);
/** Query for a period: hourly periods ask for hours, and the end-user breakdown (daily only) for the day. */
const periodQuery = (p: string, daily = false) => (p.endsWith("h") ? (daily ? "days=1" : `hours=${p.slice(0, -1)}`) : `days=${p}`);
type Metric = "spend" | "requests" | "tokens";

const errRate = (r: UsageRow) => (r.requests ? r.errors / r.requests : null);

export default function Activity({ scope = "org" }: { scope?: Scope }) {
  const { org, ws, wsList, wsName, canAllWs } = useSession();
  const platform = scope === "platform";
  const base = platform ? PLATFORM : org ? orgBase(org.id) : "";
  // Organization usage is limited to the current workspace (?workspace=); "All workspaces" shows the whole org.
  const inWS = (path: string) => (platform ? path : wsList(path));
  const [days, setDays] = useQueryParam("days", "30");
  const [metric, setMetric] = useQueryParam("metric", "spend") as [Metric, (v: string) => void];
  const period = PERIODS.includes(days) ? days : "30";

  const usage = useAsync(() => get<UsageReport>(inWS(`${base}/usage?${periodQuery(period)}`)), [base, period]);
  const orgInfo = useAsync(() => (platform || !base ? Promise.resolve(null) : get<Org>(base)), [base, platform]);
  const keys = useAsync(() => (platform || !base ? Promise.resolve([] as APIKey[]) : get<APIKey[]>(inWS(`${base}/keys`))), [base, platform]);
  // Model rows are org-wide rollups, so one workspace starts on its keys.
  const [by, setBy] = useQueryParam("by", ws ? "key" : "model");
  const breakdown = platform ? "model" : by;
  const endUsers = useAsync(
    () => (breakdown === "end_user" && base ? get<UsageReport>(`${base}/usage?${periodQuery(period, true)}&group=end_user`).then((r) => r.end_users ?? []) : Promise.resolve(null)),
    [base, period, breakdown],
  );
  const byWorkspace = useAsync(
    () =>
      breakdown === "workspace" && canAllWs && base
        ? get<UsageReport>(`${base}/usage?${periodQuery(period)}&group=workspace`).then((r) => r.workspaces ?? [])
        : Promise.resolve(null),
    [base, period, breakdown, canAllWs],
  );
  const [exporting, setExporting] = useState(false);

  if (!platform && !org) return <EmptyState title="You are not in an organization yet">Ask an admin to add you, then sign in again.</EmptyState>;

  const keyName = (id: string) => keys.data?.find((k) => k.id === id)?.name;

  return (
    <>
      <PageHeader
        title={platform ? "Platform activity" : "Activity"}
        description={
          platform
            ? "Traffic across every organization, by public model."
            : ws
              ? `Requests, spend and tokens of the ${ws.name} workspace's API keys.`
              : "Requests, spend and tokens for this organization, all workspaces."
        }
        actions={
          <>
          <Button variant="outline" size="sm" onClick={() => setExporting(true)}>
            <Download aria-hidden />
            Export CSV
          </Button>
          <Select value={period} onValueChange={setDays}>
            <SelectTrigger size="sm" className="w-36" aria-label="Period">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PERIODS.map((p) => (
                <SelectItem key={p} value={p}>
                  {periodLabel(p)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          </>
        }
      />
      <ExportDialog open={exporting} onOpenChange={setExporting} base={base} platform={platform} workspace={platform ? undefined : ws?.id} allWs={canAllWs && !ws} />

      {!platform && <ConnectTools firstRun={usage.data ? usage.data.total.requests === 0 : false} />}

      <DataState state={usage} rows={3}>
        {(u) => (
          <>
            <StatStrip>
              <Stat label="Requests" value={fmt(u.total.requests)} />
              <Stat label="Spend" value={money(u.total.cost_usd)} />
              <Stat label="Tokens in / out" value={`${compact(u.total.in_tokens)} / ${compact(u.total.out_tokens)}`} />
              <Stat label="Error rate" value={pct(errRate(u.total), 1)} hint={`${fmt(u.total.errors)} errors`} />
              <Stat label="Avg latency" value={u.total.requests ? ms(u.total.avg_latency_ms) : "–"} />
              {platform ? (
                <Stat label="Cache hits" value={fmt(u.total.cache_hits)} />
              ) : (
                <Stat
                  label="Credit balance"
                  value={
                    orgInfo.error ? "–" : !orgInfo.data ? "…" : orgInfo.data.prepaid ? usd(orgInfo.data.balance_micros) : "No credit limit"
                  }
                  hint={orgInfo.data?.prepaid ? <Link className="underline underline-offset-2" href="/credits">Credits</Link> : undefined}
                />
              )}
            </StatStrip>

            <Section
              title={period.endsWith("h") ? "Hourly" : "Daily"}
              actions={
                <Tabs value={metric} onValueChange={setMetric}>
                  <TabsList aria-label="Chart metric">
                    <TabsTrigger value="spend">Spend</TabsTrigger>
                    <TabsTrigger value="requests">Requests</TabsTrigger>
                    <TabsTrigger value="tokens">Tokens</TabsTrigger>
                  </TabsList>
                </Tabs>
              }
            >
              <div className="rounded-lg border p-4">
                {u.total.requests === 0 ? (
                  <EmptyState title="No requests in this period">
                    Send a request with an API key, or try a model in the <Link className="underline" href="/playground">Playground</Link>.
                  </EmptyState>
                ) : (
                  <DailyChart rows={u.daily} metric={metric} period={period} />
                )}
              </div>
            </Section>

            <Section
              title="Breakdown"
              actions={
                !platform && (
                  <Tabs value={breakdown} onValueChange={setBy}>
                    <TabsList aria-label="Group usage by">
                      <TabsTrigger value="model">Models</TabsTrigger>
                      <TabsTrigger value="key">API keys</TabsTrigger>
                      <TabsTrigger value="end_user">End users</TabsTrigger>
                      {canAllWs && <TabsTrigger value="workspace">Workspaces</TabsTrigger>}
                    </TabsList>
                  </Tabs>
                )
              }
            >
              {breakdown === "key" ? (
                keys.error ? (
                  <ErrorState error={keys.error} onRetry={keys.reload} />
                ) : keys.loading && !keys.data ? (
                  <LoadingRows rows={2} />
                ) : (
                  <UsageTable
                    rows={u.keys}
                    first="API key"
                    empty="No key usage in this period."
                    name={(id) => keyName(id) ?? <span className="text-muted-foreground">Deleted key ({id.slice(0, 6)})</span>}
                  />
                )
              ) : breakdown === "workspace" && canAllWs ? (
                <>
                  <p className="mb-2 text-sm text-muted-foreground">Every workspace of the organization, by the workspace of each request's API key.</p>
                  <DataState state={byWorkspace} rows={3}>
                    {(rows) => <UsageTable rows={rows ?? []} first="Workspace" empty="No workspace usage in this period." name={(id) => wsName(id)} />}
                  </DataState>
                </>
              ) : breakdown === "end_user" ? (
                <>
                  <p className="mb-2 text-sm text-muted-foreground">
                    Requests grouped by the <code className="font-mono">user</code> field your application sends, top 1,000 by spend
                    {ws ? ", across all workspaces" : ""}.
                  </p>
                  <DataState state={endUsers} rows={3}>
                    {(rows) => <UsageTable rows={rows ?? []} first="End user" empty={period.endsWith("h") ? "No requests with a user field today (end users are counted per day)." : "No requests with a user field in this period."} name={(id) => <span className="font-mono text-xs">{id}</span>} />}
                  </DataState>
                </>
              ) : ws && !platform ? (
                <p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
                  Model totals are kept for the whole organization, not per workspace.{" "}
                  {canAllWs ? "Choose All workspaces in the workspace switcher to see them." : "See the API keys tab for this workspace."}
                </p>
              ) : (
                <UsageTable rows={u.models} first="Model" empty="No model usage in this period." />
              )}
            </Section>
          </>
        )}
      </DataState>
    </>
  );
}

function DailyChart({ rows, metric, period }: { rows: UsageRow[]; metric: Metric; period: string }) {
  const value = (r: UsageRow) => (metric === "spend" ? r.cost_usd : metric === "requests" ? r.requests : r.in_tokens + r.out_tokens);
  const format = (v: number) => (metric === "spend" ? money(v) : compact(v));
  const tick = (v: number, max: number) => (metric === "spend" ? usdTick(v, max) : compact(v));
  // Hourly buckets are UTC yyyymmddhh; daily ones yyyymmdd.
  const label = (k: string) =>
    k.length === 10
      ? new Date(Date.UTC(+k.slice(0, 4), +k.slice(4, 6) - 1, +k.slice(6, 8), +k.slice(8, 10))).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
      : dayDate(k).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const total = rows.reduce((s, r) => s + value(r), 0);
  const peak = rows.reduce((a, r) => (value(r) > value(a) ? r : a), rows[0]);
  const name = metric === "spend" ? "spend" : metric === "requests" ? "requests" : "tokens";
  return (
    <BarChart
      label={`${period.endsWith("h") ? "Hourly" : "Daily"} ${name} over the ${periodLabel(period).toLowerCase()}: ${format(total)} in total, highest on ${label(peak.key)} at ${format(value(peak))}.`}
      format={tick}
      bars={rows.map((r) => ({
        label: label(r.key),
        value: value(r),
        title: `${label(r.key)}: ${metric === "spend" ? money(r.cost_usd) : fmt(value(r))} ${metric === "spend" ? "" : name}`.trim(),
      }))}
    />
  );
}

function UsageTable({ rows, first, empty, name }: { rows: UsageRow[]; first: string; empty: string; name?: (k: string) => ReactNode }) {
  if (rows.length === 0) return <p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">{empty}</p>;
  return (
    <TableFrame>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{first}</TableHead>
            <TableHead className="text-right">Requests</TableHead>
            <TableHead className="text-right">Errors</TableHead>
            <TableHead className="text-right">Tokens in</TableHead>
            <TableHead className="text-right">Cached</TableHead>
            <TableHead className="text-right">Tokens out</TableHead>
            <TableHead className="text-right">Avg latency</TableHead>
            <TableHead className="text-right">Spend</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => (
            <TableRow key={r.key}>
              <TableCell className="font-medium">{name ? name(r.key) : <span className="font-mono text-xs">{r.key}</span>}</TableCell>
              <TableCell className="text-right tabular-nums">{fmt(r.requests)}</TableCell>
              <TableCell className={"text-right tabular-nums " + (r.errors ? "text-danger" : "text-muted-foreground")}>{fmt(r.errors)}</TableCell>
              <TableCell className="text-right tabular-nums">{fmt(r.in_tokens)}</TableCell>
              <TableCell className="text-right tabular-nums text-muted-foreground" title="Prompt tokens read from the provider's prompt cache">
                {fmt(r.cache_read_tokens ?? 0)}
              </TableCell>
              <TableCell className="text-right tabular-nums">{fmt(r.out_tokens)}</TableCell>
              <TableCell className="text-right tabular-nums">{ms(r.avg_latency_ms)}</TableCell>
              <TableCell className="text-right tabular-nums">{money(r.cost_usd)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableFrame>
  );
}


const isoDay = (d: Date) => d.toISOString().slice(0, 10);
const daysAgo = (n: number) => isoDay(new Date(Date.now() - n * 86400000));

/** CSV downloads: plain same-origin links, so the session cookie authenticates them. */
function ExportDialog({
  open, onOpenChange, base, platform, workspace, allWs,
}: { open: boolean; onOpenChange: (o: boolean) => void; base: string; platform: boolean; workspace?: string; allWs: boolean }) {
  const [what, setWhat] = useState("usage");
  // One workspace's usage has no model rows (the server answers 400), so it starts on keys.
  const [group, setGroup] = useState(workspace ? "key" : "model");
  const [from, setFrom] = useState(daysAgo(30));
  const [to, setTo] = useState(isoDay(new Date()));
  const logs = what === "logs" && !platform;
  const bad = !from || !to || from > to;
  const qs = new URLSearchParams({ from, to, ...(logs ? {} : { group }), ...(workspace ? { workspace } : {}) });
  const href = `${base}/${logs ? "logs" : "usage"}/export.csv?${qs}`;
  const groups = platform ? ["model", "day"] : workspace ? ["key", "creator", "day"] : ["model", "key", "creator", "day", ...(allWs ? ["workspace"] : [])];
  const groupLabel: Record<string, string> = { model: "Model", key: "API key", creator: "Key creator", day: "Day", workspace: "Workspace" };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Export CSV</DialogTitle>
          <DialogDescription>
            {logs ? "One row per request, newest first, at most 100,000 rows." : "Daily rows from the usage rollups."} Dates are UTC and include both ends.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          {!platform && (
            <div className="grid gap-1.5">
              <Label htmlFor="exp-what">Data</Label>
              <Select value={what} onValueChange={(v) => (setWhat(v), setFrom(daysAgo(v === "logs" ? 7 : 30)))}>
                <SelectTrigger id="exp-what" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="usage">Usage</SelectItem>
                  <SelectItem value="logs">Request logs</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}
          {!logs && (
            <div className="grid gap-1.5">
              <Label htmlFor="exp-group">Group by</Label>
              <Select value={group} onValueChange={setGroup}>
                <SelectTrigger id="exp-group" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {groups.map((g) => (
                    <SelectItem key={g} value={g}>
                      {groupLabel[g]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="grid grid-cols-2 gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="exp-from">From</Label>
              <Input id="exp-from" type="date" value={from} max={to} onChange={(e) => setFrom(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="exp-to">To</Label>
              <Input id="exp-to" type="date" value={to} min={from} onChange={(e) => setTo(e.target.value)} />
            </div>
          </div>
          {bad && <p role="alert" className="text-sm text-destructive">Choose a start date on or before the end date.</p>}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
          {bad ? (
            <Button disabled>
              <Download aria-hidden />
              Download
            </Button>
          ) : (
            <Button asChild>
              <a href={href} download>
                <Download aria-hidden />
                Download
              </a>
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

const CONNECT_KEY = "polyrail.activity.connect";

/** Setup for Claude Code, Codex, OpenCode and curl. Open on a first run (no requests yet); the
 *  viewer's open/closed choice is remembered in this browser. */
function ConnectTools({ firstRun }: { firstRun: boolean }) {
  const [pref, setPref] = useState<string | null>(() => {
    try {
      return localStorage.getItem(CONNECT_KEY);
    } catch {
      return null;
    }
  });
  const open = pref ? pref === "open" : firstRun;
  const toggle = () => {
    const next = open ? "closed" : "open";
    setPref(next);
    try {
      localStorage.setItem(CONNECT_KEY, next);
    } catch {
      /* private mode: keep it for this visit only */
    }
  };
  const models = useAsync(() => publicApi.models(), []);
  const example = models.data?.find((m) => m.kind === "chat")?.id ?? "openai/gpt-4o-mini";
  return (
    <section aria-labelledby="connect-h" className="mb-6 rounded-lg border">
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        aria-controls="connect-body"
        className="flex w-full items-center gap-3 rounded-lg px-4 py-3 text-left hover:bg-muted/50 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring"
      >
        <Terminal className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <span className="min-w-0 flex-1">
          <span id="connect-h" className="block text-sm font-medium">
            Connect Claude Code, Codex, OpenCode or curl
          </span>
          <span className="block text-xs text-muted-foreground">Point your tool at this gateway with an API key from API keys.</span>
        </span>
        <ChevronDown className={"size-4 shrink-0 text-muted-foreground " + (open ? "rotate-180" : "")} aria-hidden />
      </button>
      {open && (
        <div id="connect-body" className="border-t px-4 pt-3 pb-4">
          <p className="mb-3 text-sm text-muted-foreground">
            Set <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">GATEWAY_API_KEY</code> to a key from{" "}
            <Link href="/keys" className="underline underline-offset-4">
              API keys
            </Link>
            . Any model id from Models works; Claude Code uses the Anthropic-compatible endpoint, the others the OpenAI-compatible one.
            More tools (Gemini CLI, Cline, Open WebUI, SDKs) and a key picker are on{" "}
            <Link href="/connect" className="underline underline-offset-4">
              Connect
            </Link>
            .
          </p>
          <CodeTabs samples={toolSamples(example)} />
        </div>
      )}
    </section>
  );
}
