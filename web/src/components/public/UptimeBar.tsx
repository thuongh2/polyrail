import type { HourPoint } from "@/lib/api";
import { cn } from "@/lib/utils";

/** Bucket one hour by error rate. Thresholds match the API's status rules (down < 90%, degraded < 99%). */
export function hourTone(h: HourPoint): "nodata" | "success" | "warning" | "danger" {
  if (!h.requests) return "nodata";
  const rate = h.errors / h.requests;
  if (rate >= 0.1) return "danger";
  if (rate >= 0.01) return "warning";
  return "success";
}

const bg = { nodata: "bg-nodata", success: "bg-success", warning: "bg-warning", danger: "bg-danger" };

function hourLabel(iso: string) {
  return new Date(iso).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

/** One thin bar per hour, oldest first. Hover (title) shows hour, requests and errors. */
export function UptimeBar({ hours, label, className }: { hours: HourPoint[]; label: string; className?: string }) {
  const served = hours.filter((h) => h.requests > 0);
  const req = served.reduce((s, h) => s + h.requests, 0);
  const err = served.reduce((s, h) => s + h.errors, 0);
  const summary = req
    ? `${label}: ${req.toLocaleString()} requests, ${err.toLocaleString()} errors over the last ${hours.length} hours`
    : `${label}: no traffic in the last ${hours.length} hours`;
  return (
    <div className={cn("min-w-0", className)}>
      <div role="img" aria-label={summary} className="flex h-7 items-stretch gap-px">
        {hours.map((h) => {
          const tone = hourTone(h);
          return (
            <span
              key={h.hour}
              title={`${hourLabel(h.hour)}: ${h.requests.toLocaleString()} requests, ${h.errors.toLocaleString()} errors`}
              className={cn("min-w-0 flex-1 rounded-[1px] hover:opacity-70", bg[tone])}
            />
          );
        })}
      </div>
      <div className="mt-1 flex justify-between text-xs text-muted-foreground">
        <span>{hours.length}h ago</span>
        <span>Now</span>
      </div>
    </div>
  );
}

/** Key for the bar colors, as text. */
export function UptimeLegend() {
  const items: [keyof typeof bg, string][] = [
    ["success", "No errors"],
    ["warning", "1–10% errors"],
    ["danger", "Over 10% errors"],
    ["nodata", "No traffic"],
  ];
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
      {items.map(([k, t]) => (
        <li key={k} className="flex items-center gap-1.5">
          <span aria-hidden className={cn("h-3 w-1.5 rounded-[1px]", bg[k])} />
          {t}
        </li>
      ))}
    </ul>
  );
}
