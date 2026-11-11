import type { ReactNode } from "react";
import { Link } from "wouter";
import { ChevronLeft } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, ErrorState, LoadingRows } from "@/components/states";
import { apiBase, CodeBlock, CodeTabs, CopyButton, requestSamples } from "@/components/public/CodeTabs";
import { StatusBadge, latency } from "@/components/public/StatusBadge";
import { UptimeBar, UptimeLegend } from "@/components/public/UptimeBar";
import { ApiError, compact, fmt, pct, perMillion, publicApi, type ModelDetail as Detail } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";

const ms = latency;
const tps = (v: number | null) => (v == null ? "–" : `${compact(Math.round(v))} tok/s`);

export default function ModelDetail({ id }: { id: string }) {
  const state = useAsync(() => publicApi.model(id), [id]);
  const [tab, setTab] = useQueryParam("tab", "overview");

  if (state.error instanceof ApiError && state.error.status === 404)
    return (
      <div className="py-8">
        <EmptyState
          title="Model not found"
          action={
            <Button asChild variant="outline" size="sm">
              <Link href="/models">Browse models</Link>
            </Button>
          }
        >
          No public model has the id <code className="font-mono">{id}</code>. It may be private, disabled or renamed.
        </EmptyState>
      </div>
    );
  if (state.error) return <ErrorState error={state.error} onRetry={state.reload} />;
  if (!state.data)
    return (
      <div className="space-y-6">
        <LoadingRows rows={2} />
        <LoadingRows rows={5} />
      </div>
    );

  const d = state.data;
  const m = d.model;
  const embedding = m.kind === "embedding";

  return (
    <div className="min-w-0">
      <Link href="/models" className="mb-4 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
        <ChevronLeft className="size-4" aria-hidden />
        Models
      </Link>
      <header className="mb-8">
        <h1 className="text-3xl font-semibold tracking-tight [overflow-wrap:break-word]">{m.name}</h1>
        <div className="mt-1 flex items-center gap-1">
          <code className="truncate font-mono text-sm text-muted-foreground">{m.id}</code>
          <CopyButton text={m.id} label="Copy model id" />
        </div>
        <div className="mt-3 flex flex-wrap gap-1.5">
          <Badge variant="secondary">{embedding ? "Embedding" : "Chat"}</Badge>
          {(m.modalities ?? ["text"]).map((x) => (
            <Badge key={x} variant="outline">
              {x === "image" ? "Image input" : x === "text" ? "Text input" : x}
            </Badge>
          ))}
        </div>
        <dl className="mt-6 grid grid-cols-2 gap-x-6 gap-y-4 rounded-lg border p-4 text-sm sm:grid-cols-3 lg:grid-cols-6">
          <Meta label="Owned by" value={m.owned_by} />
          <Meta label="Created" value={m.created ? new Date(m.created * 1000).toLocaleDateString() : "–"} />
          <Meta label="Context" value={m.context_length ? `${fmt(m.context_length)} tokens` : "–"} />
          <Meta label="Max output" value={m.max_output_tokens ? `${fmt(m.max_output_tokens)} tokens` : "–"} />
          <Meta label="Input price" value={`${perMillion(m.pricing.input)} / 1M`} />
          <Meta label="Output price" value={embedding ? "–" : `${perMillion(m.pricing.output)} / 1M`} />
        </dl>
      </header>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList variant="line" className="w-full justify-start border-b pb-1">
          <TabsTrigger value="overview" className="flex-none px-3">
            Overview
          </TabsTrigger>
          <TabsTrigger value="providers" className="flex-none px-3">
            Providers ({d.providers.length})
          </TabsTrigger>
          <TabsTrigger value="api" className="flex-none px-3">
            API
          </TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="pt-6">
          <Overview d={d} />
        </TabsContent>
        <TabsContent value="providers" className="pt-6">
          <Providers d={d} />
        </TabsContent>
        <TabsContent value="api" className="pt-6">
          <ApiTab d={d} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Meta({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 truncate font-medium tabular-nums">{value}</dd>
    </div>
  );
}

function Overview({ d }: { d: Detail }) {
  const m = d.model;
  const s = d.stats_24h;
  return (
    <div className="grid gap-10 lg:grid-cols-[minmax(0,1fr)_minmax(0,22rem)]">
      <div className="min-w-0 space-y-8">
        <section>
          <h2 className="mb-2 text-lg font-semibold">About</h2>
          <p className="max-w-prose text-muted-foreground">{m.description || "No description provided."}</p>
        </section>
        <section aria-labelledby="stats-h">
          <h2 id="stats-h" className="mb-3 text-lg font-semibold">
            Last 24 hours
          </h2>
          <dl className="grid grid-cols-2 gap-px overflow-hidden rounded-lg border bg-border sm:grid-cols-4">
            <Stat label="Requests" value={fmt(s.requests)} />
            <Stat label="Uptime" value={pct(s.uptime)} />
            <Stat label="Avg latency" value={ms(s)} />
            <Stat label="Throughput" value={tps(s.throughput_tps)} />
          </dl>
          <p className="mt-2 text-xs text-muted-foreground">Across all providers serving this model, counted per provider attempt.</p>
        </section>
      </div>
      <section aria-labelledby="price-h">
        <h2 id="price-h" className="mb-3 text-lg font-semibold">
          Pricing
        </h2>
        <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Tokens</TableHead>
              <TableHead className="text-right">Per 1M</TableHead>
              <TableHead className="text-right">Per 1K</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <PriceRow label="Input" p={m.pricing.input} />
            {m.kind === "chat" && <PriceRow label="Output" p={m.pricing.output} />}
          </TableBody>
        </Table>
        </div>
      </section>
    </div>
  );
}

function PriceRow({ label, p }: { label: string; p: string }) {
  const per1k = Number(p) / 1000;
  return (
    <TableRow>
      <TableCell>{label}</TableCell>
      <TableCell className="text-right font-medium tabular-nums">{perMillion(p)}</TableCell>
      <TableCell className="text-right tabular-nums text-muted-foreground">
        {per1k === 0 ? "Free" : "$" + per1k.toPrecision(2).replace(/\.?0+$/, "")}
      </TableCell>
    </TableRow>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-background p-4">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="mt-1 text-xl font-semibold tabular-nums">{value}</dd>
    </div>
  );
}

function Providers({ d }: { d: Detail }) {
  if (d.providers.length === 0) return <EmptyState title="No providers">This model has no public provider right now.</EmptyState>;
  const total = d.providers.reduce((s, p) => s + p.weight, 0);
  return (
    <div className="space-y-10">
      <section aria-labelledby="prov-h">
        <h2 id="prov-h" className="sr-only">
          Providers
        </h2>
        <div className="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Provider</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Upstream model</TableHead>
                <TableHead className="text-right">Uptime 24h</TableHead>
                <TableHead className="text-right">Avg latency</TableHead>
                <TableHead className="text-right">Throughput</TableHead>
                <TableHead className="text-right">Traffic share</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {d.providers.map((p) => (
                <TableRow key={p.name}>
                  <TableCell>
                    <div className="font-medium">{p.name}</div>
                    <div className="text-xs text-muted-foreground">{p.type}</div>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={p.status} />
                  </TableCell>
                  <TableCell>
                    <code className="font-mono text-xs">{p.upstream_model}</code>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{pct(p.stats_24h.uptime)}</TableCell>
                  <TableCell className="text-right tabular-nums">{ms(p.stats_24h)}</TableCell>
                  <TableCell className="text-right tabular-nums">{tps(p.stats_24h.throughput_tps)}</TableCell>
                  <TableCell className="text-right tabular-nums">{total ? `${Math.round((p.weight / total) * 100)}%` : "–"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          Traffic share is the routing weight. When a provider fails, the gateway retries the request on the next one.
        </p>
      </section>
      <section aria-labelledby="uptime-h">
        <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
          <h2 id="uptime-h" className="text-lg font-semibold">
            Uptime, last 72 hours
          </h2>
          <UptimeLegend />
        </div>
        <ul className="space-y-5">
          {d.providers.map((p) => (
            <li key={p.name} className="grid gap-2 sm:grid-cols-[10rem_minmax(0,1fr)] sm:items-start sm:gap-6">
              <div className="flex items-center justify-between gap-2 sm:block">
                <p className="truncate text-sm font-medium">{p.name}</p>
                <p className="text-xs text-muted-foreground">{pct(p.stats_24h.uptime)} over 24h</p>
              </div>
              <UptimeBar hours={p.hourly} label={p.name} />
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}

function ApiTab({ d }: { d: Detail }) {
  const base = apiBase();
  const m = d.model;
  return (
    <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,18rem)]">
      <CodeTabs samples={requestSamples(base, m.id, m.kind)} />
      <aside className="space-y-4 text-sm">
        <div>
          <h2 className="mb-1 font-medium">Base URL</h2>
          <CodeBlock code={base} />
        </div>
        <p className="text-muted-foreground">
          Create a key in the console and send it as <code className="font-mono text-foreground">Authorization: Bearer</code>.{" "}
          {m.kind === "chat" && "Add \"stream\": true for server-sent events. "}
          <Link href="/docs" className="text-foreground underline underline-offset-4">
            Full reference
          </Link>
        </p>
      </aside>
    </div>
  );
}
