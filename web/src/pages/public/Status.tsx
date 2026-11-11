import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { StatusBadge, latency } from "@/components/public/StatusBadge";
import { compact, fmt, pct, publicApi, when, type StatusPage } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { ProviderLogo } from "@/components/ProviderLogo";
import { providerLabel } from "@/lib/providers";
import { cn } from "@/lib/utils";

function summary(p: StatusPage["providers"]) {
  const down = p.filter((x) => x.status === "down").length;
  const degraded = p.filter((x) => x.status === "degraded").length;
  const ok = p.filter((x) => x.status === "operational").length;
  if (down + degraded === 0)
    return ok === 0
      ? { text: "No traffic in the last 24 hours", tone: "border-border bg-muted" }
      : {
          text:
            ok === p.length
              ? `All ${p.length} providers are operational`
              : `${ok} providers operational, ${p.length - ok} without traffic`,
          tone: "border-success/40 bg-success/10",
        };
  const parts = [down && `${down} down`, degraded && `${degraded} degraded`, ok && `${ok} operational`].filter(Boolean);
  return {
    text: `${p.length} providers: ${parts.join(", ")}`,
    tone: down ? "border-danger/40 bg-danger/10" : "border-warning/50 bg-warning/15",
  };
}

export default function Status() {
  const state = useAsync(publicApi.status);
  return (
    <>
      <PageHeader
        title="Status"
        description="Health of the public providers over the last 24 hours, from the gateway's own request attempts."
        actions={
          <Button variant="outline" size="sm" onClick={state.reload} disabled={state.loading}>
            <RefreshCw className={cn(state.loading && "motion-safe:animate-spin")} />
            Refresh
          </Button>
        }
      />
      <DataState
        state={state}
        rows={4}
        isEmpty={(d: StatusPage) => d.providers.length === 0}
        empty={<EmptyState title="No public providers">An operator adds them in the console under Platform, Providers.</EmptyState>}
      >
        {(d: StatusPage) => {
          const s = summary(d.providers);
          return (
            <div className="space-y-4">
              <div role="status" className={cn("rounded-lg border px-4 py-3", s.tone)}>
                <p className="font-medium">{s.text}</p>
                <p className="text-sm text-muted-foreground">Updated {when(d.updated_at)}</p>
              </div>
              <div className="rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Provider</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead className="text-right">Models</TableHead>
                      <TableHead className="text-right">Requests 24h</TableHead>
                      <TableHead className="text-right">Uptime 24h</TableHead>
                      <TableHead className="text-right">Avg latency</TableHead>
                      <TableHead className="text-right">Throughput</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {d.providers.map((p) => (
                      <TableRow key={p.name}>
                        <TableCell>
                          <div className="flex items-center gap-2.5">
                            <ProviderLogo type={p.type} size={24} />
                            <div>
                              <div className="font-medium">{p.name}</div>
                              <div className="text-xs text-muted-foreground">{providerLabel(p.type)}</div>
                            </div>
                          </div>
                        </TableCell>
                        <TableCell>
                          <StatusBadge status={p.status} />
                        </TableCell>
                        <TableCell className="text-right tabular-nums">{fmt(p.models)}</TableCell>
                        <TableCell className="text-right tabular-nums">{fmt(p.stats_24h.requests)}</TableCell>
                        <TableCell className="text-right tabular-nums">{pct(p.stats_24h.uptime)}</TableCell>
                        <TableCell className="text-right tabular-nums">
                          {latency(p.stats_24h)}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">
                          {p.stats_24h.throughput_tps == null ? "–" : `${compact(Math.round(p.stats_24h.throughput_tps))} tok/s`}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              <p className="text-xs text-muted-foreground">
                Down: uptime under 90% or the circuit breaker is open. Degraded: uptime under 99%. Per-model, hour-by-hour uptime is on
                each model's Providers tab.
              </p>
            </div>
          );
        }}
      </DataState>
    </>
  );
}
