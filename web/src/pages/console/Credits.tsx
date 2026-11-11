import { Link } from "wouter";
import { BarChart } from "@/components/console/BarChart";
import { Section, Stat, StatStrip, TableFrame, dayDate, usdTick } from "@/components/console/bits";
import { DataState, EmptyState, ErrorState, LoadingRows, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { fmt, get, orgBase, usd, type Org, type UsageReport } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const money = (v: number) => usd(Math.round(v * 1e6));
const label = (k: string) => dayDate(k).toLocaleDateString(undefined, { month: "short", day: "numeric" });

export default function Credits() {
  const { org } = useSession();
  const base = org ? orgBase(org.id) : "";
  const info = useAsync(() => get<Org>(base), [base]);
  const usage = useAsync(() => get<UsageReport>(`${base}/usage?days=30`), [base]);
  if (!org) return <EmptyState title="You are not in an organization yet" />;

  return (
    <>
      <PageHeader title="Credits" description="What this organization has been granted, what it has spent, and how billing is enforced." />
      <DataState state={info} rows={2}>
        {(o) => (
          <>
            <StatStrip className="lg:grid-cols-4">
              <Stat label="Balance" value={o.prepaid ? usd(o.balance_micros) : "No credit limit"} />
              <Stat label="Granted" value={usd(o.credits_micros)} />
              <Stat label="Spent" value={usd(o.spent_micros)} hint="All time" />
              <Stat
                label="Billing mode"
                value={
                  <Badge variant="outline" className={o.prepaid ? "text-signal" : undefined}>
                    {o.prepaid ? "Prepaid" : "Postpaid"}
                  </Badge>
                }
              />
            </StatStrip>
            <div className="mt-4 rounded-lg border p-4 text-sm">
              <h2 className="font-medium">How billing works here</h2>
              {o.prepaid ? (
                <p className="mt-1 text-muted-foreground">
                  This organization is prepaid. Requests are refused with status 402 once the balance reaches zero. Credits are granted by a platform
                  operator; ask yours to add more.
                  {o.prepaid && (o.balance_micros ?? 0) <= 0 && <strong className="mt-2 block text-danger">The balance is used up: requests are being refused.</strong>}
                </p>
              ) : (
                <p className="mt-1 text-muted-foreground">
                  This organization is not prepaid, so requests are never refused for lack of credit. Spend is still metered at each model's price and
                  shown below; per-key monthly budgets on the <Link className="underline" href="/keys">API keys</Link> page still apply.
                </p>
              )}
              <p className="mt-2 text-muted-foreground">
                There is no payment integration: nobody can buy credits through this console. A platform operator grants them, and every grant is
                recorded in the audit log.
              </p>
            </div>
          </>
        )}
      </DataState>

      <Section title="Spend, last 30 days">
        {usage.error ? (
          <ErrorState error={usage.error} onRetry={usage.reload} />
        ) : !usage.data ? (
          <LoadingRows rows={3} />
        ) : usage.data.total.requests === 0 ? (
          <EmptyState title="No spend in the last 30 days" />
        ) : (
          <>
            <div className="rounded-lg border p-4">
              <BarChart
                label={`Daily spend over the last 30 days, ${money(usage.data.total.cost_usd)} in total.`}
                format={usdTick}
                height={160}
                bars={usage.data.daily.map((r) => ({ label: label(r.key), value: r.cost_usd, title: `${label(r.key)}: ${money(r.cost_usd)}` }))}
              />
            </div>
            <div className="mt-4">
              <TableFrame>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Day</TableHead>
                      <TableHead className="text-right">Requests</TableHead>
                      <TableHead className="text-right">Spend</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {usage.data.daily
                      .filter((r) => r.requests > 0)
                      .reverse()
                      .map((r) => (
                        <TableRow key={r.key}>
                          <TableCell>{dayDate(r.key).toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" })}</TableCell>
                          <TableCell className="text-right tabular-nums">{fmt(r.requests)}</TableCell>
                          <TableCell className="text-right tabular-nums">{money(r.cost_usd)}</TableCell>
                        </TableRow>
                      ))}
                  </TableBody>
                </Table>
              </TableFrame>
            </div>
          </>
        )}
      </Section>
    </>
  );
}
