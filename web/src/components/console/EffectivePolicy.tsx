import type { ReactNode } from "react";
import { Link } from "wouter";
import { DataState } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { get, usd, usdLimit, when, type EffectiveBudget, type EffectivePolicy as EP } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const intervalLabel = { daily: "day", weekly: "week", monthly: "month", lifetime: "lifetime" } as const;
const viaLabel = { org_default: "org default", workspace_default: "workspace default", member: "creator", key: "this key" } as const;

/** The combined policy of one key (GET …/keys/{id}/effective-policy) with budget progress. */
export function EffectivePolicyView({ base, keyId, admin }: { base: string; keyId: string; admin: boolean }) {
  const ep = useAsync(() => get<EP>(`${base}/keys/${encodeURIComponent(keyId)}/effective-policy`), [base, keyId]);
  return (
    <DataState state={ep} rows={3}>
      {(p) =>
        p.policies.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No policy applies to this key.{admin && <> Create one on the <Link className="underline underline-offset-2" href="/policies">Policies</Link> page.</>}
          </p>
        ) : (
          <div className="grid gap-5 text-sm">
            <Group title="Policies">
              <ul className="grid gap-1">
                {p.policies.map((x) => (
                  <li key={x.id} className="flex flex-wrap items-baseline gap-x-2">
                    <span className="font-medium">{x.name}</span>
                    <span className="text-xs text-muted-foreground">via {x.via.map((v) => viaLabel[v] ?? v).join(", ")}</span>
                  </li>
                ))}
              </ul>
            </Group>
            {!!p.budgets?.length && (
              <Group title="Budgets">
                <div className="grid gap-3">
                  {p.budgets.map((b, i) => (
                    <Budget key={i} b={b} />
                  ))}
                </div>
              </Group>
            )}
            <Group title="Models">
              <Lists lists={p.allowed_models} any="Any model" />
            </Group>
            <Group title="Providers">
              <Lists lists={p.allowed_providers} any="Any provider" />
            </Group>
            <Group title="Privacy">
              {p.require_zdr || p.deny_data_collection ? (
                <div className="flex flex-wrap gap-1">
                  {p.require_zdr && <Badge variant="outline">Zero data retention required</Badge>}
                  {p.deny_data_collection && <Badge variant="outline">No training on prompts</Badge>}
                </div>
              ) : (
                <span className="text-muted-foreground">No privacy rule</span>
              )}
            </Group>
            {!!p.filters?.length && (
              <Group title="Prompt filters">
                <ul className="grid gap-1">
                  {p.filters.map((f, i) => (
                    <li key={i} className="flex min-w-0 items-baseline gap-2">
                      <span className={f.action === "block" ? "text-danger" : "text-muted-foreground"}>{f.action === "block" ? "Block" : "Redact"}</span>
                      <code className="min-w-0 truncate font-mono text-xs" title={f.pattern}>
                        {f.name ? `${f.name}: ` : ""}
                        {f.pattern}
                      </code>
                    </li>
                  ))}
                </ul>
              </Group>
            )}
          </div>
        )
      }
    </DataState>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <h4 className="text-xs font-medium text-muted-foreground">{title}</h4>
      {children}
    </div>
  );
}

/** Every list must be matched, so several lists read as "and". */
function Lists({ lists, any }: { lists?: string[][]; any: string }) {
  if (!lists?.length) return <span className="text-muted-foreground">{any}</span>;
  return (
    <div className="grid gap-1.5">
      {lists.map((l, i) => (
        <div key={i} className="flex flex-wrap items-center gap-1">
          {i > 0 && <span className="text-xs text-muted-foreground">and one of</span>}
          {l.map((x) => (
            <Badge key={x} variant="secondary" className="font-mono text-xs">
              {x}
            </Badge>
          ))}
        </div>
      ))}
    </div>
  );
}

function Budget({ b }: { b: EffectiveBudget }) {
  const ratio = b.limit_micros ? b.spent_micros / b.limit_micros : 0;
  const pctV = Math.min(100, Math.round(ratio * 100));
  const tone = ratio >= 1 ? "bg-danger" : ratio >= 0.8 ? "bg-warning" : "bg-signal";
  const period = b.interval === "lifetime" ? "lifetime" : `per ${intervalLabel[b.interval]}`;
  return (
    <div className="grid gap-1">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <span>
          <span className="font-medium tabular-nums">{usd(b.spent_micros)}</span>
          <span className="text-muted-foreground"> of {usdLimit(b.limit_micros)} {period}</span>
        </span>
        <span className="text-xs text-muted-foreground">
          {b.policy}, {b.scope === "member" ? "all keys of the creator" : b.scope === "workspace" ? "all keys of the workspace" : "this key"}
        </span>
      </div>
      <div
        role="progressbar"
        aria-label={`${b.policy} budget used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pctV}
        className="h-1.5 overflow-hidden rounded-full bg-muted"
      >
        <div className={cn("h-full rounded-full", tone)} style={{ width: `${pctV}%` }} />
      </div>
      <span className="text-xs text-muted-foreground">
        {ratio >= 1 ? "Limit reached: requests are refused. " : ""}
        {b.resets_at ? `Resets ${when(b.resets_at)}` : "Never resets"}
        {b.include_byok ? " · includes own providers" : ""}
      </span>
    </div>
  );
}
