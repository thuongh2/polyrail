import { pathID } from "@/lib/api";
import { Link } from "wouter";
import { Badge } from "@/components/ui/badge";
import { compact, perMillion, type ModelInfo } from "@/lib/api";
import { CopyButton } from "./CodeTabs";

export const modelHref = (id: string) => `/models/${pathID(id)}`;

/** One catalog entry: name, copyable id, description, then context and prices aligned in columns on wide screens. */
export function ModelRow({ m, compactRow = false }: { m: ModelInfo; compactRow?: boolean }) {
  const embedding = m.kind === "embedding";
  return (
    <li className="group relative grid gap-3 px-4 py-4 hover:bg-accent/50 md:grid-cols-[minmax(0,1fr)_6rem_6rem_6rem] md:items-start md:gap-6">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Link
            href={modelHref(m.id)}
            className="font-medium [overflow-wrap:anywhere] after:absolute after:inset-0 after:content-[''] hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          >
            {m.name}
          </Link>
          {embedding && <Badge variant="secondary">Embedding</Badge>}
          {m.modalities?.includes("image") && <Badge variant="outline">Image input</Badge>}
        </div>
        <div className="relative z-10 mt-0.5 flex w-fit max-w-full items-center gap-1">
          <code className="truncate font-mono text-xs text-muted-foreground">{m.id}</code>
          <CopyButton text={m.id} label={`Copy model id ${m.id}`} />
        </div>
        {!compactRow && m.description && (
          <p className="mt-1.5 line-clamp-2 max-w-3xl text-sm text-muted-foreground">{m.description}</p>
        )}
        {!compactRow && !!m.providers?.length && (
          <p className="mt-1.5 text-xs text-muted-foreground">
            {m.providers.length === 1 ? "Provider: " : `${m.providers.length} providers: `}
            {m.providers.join(", ")}
          </p>
        )}
      </div>
      <dl className="grid grid-cols-3 gap-2 text-sm md:contents">
        <Stat label="Context" value={m.context_length ? compact(m.context_length) : "–"} />
        <Stat label="Input / 1M" value={perMillion(m.pricing.input)} />
        <Stat label="Output / 1M" value={embedding ? "–" : perMillion(m.pricing.output)} />
      </dl>
    </li>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 md:text-right">
      <dt className="text-xs text-muted-foreground md:sr-only">{label}</dt>
      <dd className="font-medium tabular-nums">{value}</dd>
    </div>
  );
}

/** Column heads matching ModelRow's wide layout. */
export function ModelListHead() {
  return (
    <div className="hidden gap-6 border-b px-4 py-2 text-xs text-muted-foreground md:grid md:grid-cols-[minmax(0,1fr)_6rem_6rem_6rem]">
      <span>Model</span>
      <span className="text-right">Context</span>
      <span className="text-right">Input / 1M</span>
      <span className="text-right">Output / 1M</span>
    </div>
  );
}
