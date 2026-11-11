import type { ReactNode } from "react";
import type { Scope } from "@/App";
import { BarChart } from "@/components/console/BarChart";
import { Section, Stat, StatStrip, TableFrame, dayDate, ms, usdTick } from "@/components/console/bits";
import { DataState, EmptyState, ErrorState, LoadingRows, PageHeader } from "@/components/states";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PLATFORM, compact, fmt, get, orgBase, pct, usd, type APIKey, type Org, type UsageReport, type UsageRow } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { Link } from "wouter";

const PERIODS = ["7", "30", "90"];
type Metric = "spend" | "requests" | "tokens";

const money = (v: number) => usd(Math.round(v * 1e6));
const errRate = (r: UsageRow) => (r.requests ? r.errors / r.requests : null);

export default function Activity({ scope = "org" }: { scope?: Scope }) {
  const { org } = useSession();
  const platform = scope === "platform";
  const base = platform ? PLATFORM : org ? orgBase(org.id) : "";
  const [days, setDays] = useQueryParam("days", "30");
  const [metric, setMetric] = useQueryParam("metric", "spend") as [Metric, (v: string) => void];
  const period = PERIODS.includes(days) ? days : "30";

  const usage = useAsync(() => get<UsageReport>(`${base}/usage?days=${period}`), [base, period]);
  const orgInfo = useAsync(() => (platform || !base ? Promise.resolve(null) : get<Org>(base)), [base, platform]);
  const keys = useAsync(() => (platform || !base ? Promise.resolve([] as APIKey[]) : get<APIKey[]>(`${base}/keys`)), [base, platform]);

  if (!platform && !org) return <EmptyState title="You are not in an organization yet">Ask an admin to add you, then sign in again.</EmptyState>;

  const keyName = (id: string) => keys.data?.find((k) => k.id === id)?.name;

  return (
    <>
      <PageHeader
        title={platform ? "Platform activity" : "Activity"}
        description={platform ? "Traffic across every organization, by public model." : "Requests, spend and tokens for this organization."}
        actions={
          <Select value={period} onValueChange={setDays}>
            <SelectTrigger size="sm" className="w-36" aria-label="Period">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PERIODS.map((p) => (
                <SelectItem key={p} value={p}>
                  Last {p} days
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />

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
              title="Daily"
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

            <Section title="By model">
              <UsageTable rows={u.models} first="Model" empty="No model usage in this period." />
            </Section>

            {!platform && (
              <Section title="By API key">
                {keys.error ? (
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
                )}
              </Section>
            )}
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
  const label = (k: string) => dayDate(k).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  const total = rows.reduce((s, r) => s + value(r), 0);
  const peak = rows.reduce((a, r) => (value(r) > value(a) ? r : a), rows[0]);
  const name = metric === "spend" ? "spend" : metric === "requests" ? "requests" : "tokens";
  return (
    <BarChart
      label={`Daily ${name} over the last ${period} days: ${format(total)} in total, highest on ${label(peak.key)} at ${format(value(peak))}.`}
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

