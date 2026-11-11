import { useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { toast } from "@/components/toast";
import { cn, writeClipboard } from "@/lib/utils";
import { usd } from "@/lib/api";

/** One figure and its label, in tabular numerals. Used in a row by <StatStrip>. */
export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: ReactNode }) {
  return (
    <div className="min-w-0 px-4 py-3">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 truncate text-lg font-semibold tabular-nums">{value}</dd>
      {hint && <dd className="text-xs text-muted-foreground">{hint}</dd>}
    </div>
  );
}

/** A bordered strip of stats; dividers come from the grid gap. */
export function StatStrip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <dl
      className={cn(
        "grid grid-cols-2 gap-px overflow-hidden rounded-lg border bg-border sm:grid-cols-3 lg:grid-cols-6 [&>div]:bg-background",
        className,
      )}
    >
      {children}
    </dl>
  );
}

/** HTTP status as text plus color. */
export function StatusBadge({ status }: { status: number }) {
  const ok = status < 400;
  const warn = status === 429 || status === 402;
  return (
    <Badge
      variant="outline"
      className={cn("tabular-nums", ok ? "text-success" : warn ? "border-warning/60 text-foreground" : "text-danger")}
    >
      {status} {ok ? "OK" : warn ? (status === 429 ? "Limited" : "Payment") : "Error"}
    </Badge>
  );
}

/** Label + control + optional help, vertically stacked. */
export function Field({ id, label, help, children }: { id: string; label: string; help?: ReactNode; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {help && (
        <p id={id + "-help"} className="text-xs text-muted-foreground">
          {help}
        </p>
      )}
    </div>
  );
}

/** A titled section with a hairline under the heading. */
export function Section({ title, description, actions, children }: { title: string; description?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="mt-8 first:mt-0">
      <div className="mb-3 flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">{title}</h2>
          {description && <p className="max-w-2xl text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions}
      </div>
      {children}
    </section>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      onClick={async () => {
        try {
          await writeClipboard(text);
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        } catch {
          toast("Copy failed. Select the text and copy it manually.", { error: true });
        }
      }}
    >
      {done ? <Check aria-hidden /> : <Copy aria-hidden />}
      {done ? "Copied" : label}
    </Button>
  );
}

/** Wraps a wide table so it scrolls inside its border instead of the page. */
export function TableFrame({ children }: { children: ReactNode }) {
  return <div className="overflow-hidden rounded-lg border [&_[data-slot=table-container]]:overflow-x-auto [&_[data-slot=table-container]]:scrollbar-thin">{children}</div>;
}

/** Float USD (usage reports) through the same formatter as micro-USD. */
export const money = (v: number) => usd(Math.round(v * 1e6));

export const ms = (v: number) => (v >= 1000 ? (v / 1000).toFixed(2) + " s" : Math.round(v) + " ms");

/** "20261111" → Date (local). */
export const dayDate = (k: string) => new Date(+k.slice(0, 4), +k.slice(4, 6) - 1, +k.slice(6, 8));

/** Axis ticks share one precision so $0.005 and $0.01 line up. */
export const usdTick = (v: number, max: number) => (v === 0 ? "$0" : "$" + v.toFixed(max < 0.1 ? 3 : max < 10 ? 2 : 0));
