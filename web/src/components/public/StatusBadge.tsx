import { fmt, type HealthStats, type HealthStatus } from "@/lib/api";
import { cn } from "@/lib/utils";

const look: Record<HealthStatus, { label: string; className: string }> = {
  operational: { label: "Operational", className: "border-success/40 bg-success/10 text-success" },
  degraded: { label: "Degraded", className: "border-warning/50 bg-warning/15 text-foreground" },
  down: { label: "Down", className: "border-danger/40 bg-danger/10 text-danger" },
  no_data: { label: "No data", className: "border-border bg-muted text-muted-foreground" },
};

/** Provider or model health as text with a tinted background (never color alone). */
export function StatusBadge({ status, className }: { status: HealthStatus; className?: string }) {
  const l = look[status] ?? look.no_data;
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        l.className,
        className,
      )}
    >
      {l.label}
    </span>
  );
}

/** Latency is averaged over successful attempts only, so it means nothing without any. */
export const latency = (s: HealthStats) =>
  !s.requests || !s.uptime ? "–" : s.avg_latency_ms < 1 ? "<1 ms" : `${fmt(Math.round(s.avg_latency_ms))} ms`;
