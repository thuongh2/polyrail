import { modelHref } from "@/components/public/ModelRow";
import { Link } from "wouter";
import { useMemo, useState } from "react";
import { Download, Pencil, Plus, Trash2, X } from "lucide-react";
import type { Scope } from "@/App";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { ConfirmDialog, EnabledText, Field, FormDialog } from "@/components/platform/kit";
import { providersPath } from "@/pages/platform/ProvidersAdmin";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  api, type CatalogSync, compact, get, kindLabel, mainPrice, orgBase, pathID, perMillion, PLATFORM, type ModelInput, type ModelView, type Provider,
  type Strategy, type Target,
} from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

export default function ModelsAdmin({ scope = "org" }: { scope?: Scope }) {
  const { org, wsCan, user, ws, wsList } = useSession();
  const base = scope === "platform" ? PLATFORM + "/models" : org ? orgBase(org.id) + "/models" : null;
  const admin = scope === "platform" ? !!user?.operator : wsCan("admin");
  const provBase = admin ? providersPath(scope, org?.id) : null;

  const models = useAsync(() => (base ? get<ModelView[]>(scope === "org" ? wsList(base) : base) : Promise.resolve([] as ModelView[])), [base]);
  // Private models may target providers of their workspace and providers shared across workspaces.
  const usable = (p: Provider) => scope === "platform" || !ws || !p.org_id || p.shared_across_workspaces || (p.workspace_id ?? "default") === ws.id;
  const providers = useAsync(
    () => (provBase ? get<Provider[]>(provBase).then((ps) => ps.filter(usable)) : Promise.resolve([] as Provider[])),
    [provBase],
  );
  const provName = useMemo(() => new Map((providers.data ?? []).map((p) => [p.id, p.name])), [providers.data]);

  const [owner, setOwner] = useQueryParam("owner", "all");
  const [q, setQ] = useQueryParam("q");
  const [editing, setEditing] = useState<ModelView | "new" | null>(null);
  const [deleting, setDeleting] = useState<ModelView | null>(null);
  const [loadingCatalog, setLoadingCatalog] = useState(false);

  const editable = (m: ModelView) => admin && (scope === "platform" || m.owner === "org");

  const title = scope === "platform" ? "Public models" : "Models";
  const description =
    scope === "platform"
      ? "The public catalog. Each model routes to one or more public providers by weight and fails over between them."
      : "Public models every organization can call, plus private models only this organization can call.";

  if (!base) {
    return <PageHeader title={title} description="Join or create an organization to see its models." />;
  }

  const add = admin && (
    <Button onClick={() => setEditing("new")}>
      <Plus aria-hidden /> {scope === "platform" ? "Add public model" : "Add private model"}
    </Button>
  );
  const actions = add && (
    <>
      {scope === "platform" && (
        <Button variant="outline" onClick={() => setLoadingCatalog(true)}>
          <Download aria-hidden /> Load model catalog
        </Button>
      )}
      {add}
    </>
  );

  return (
    <>
      <PageHeader title={title} description={description} actions={actions || undefined} />
      <div className="mb-4 flex flex-wrap items-center gap-3">
        <Input
          type="search"
          aria-label="Filter models"
          placeholder="Filter by name or id"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          className="w-full sm:w-72"
        />
        {scope === "org" && (
          <Tabs value={owner} onValueChange={setOwner}>
            <TabsList aria-label="Owner">
              <TabsTrigger value="all">All</TabsTrigger>
              <TabsTrigger value="platform">Public</TabsTrigger>
              <TabsTrigger value="org">Private</TabsTrigger>
            </TabsList>
          </Tabs>
        )}
      </div>
      <DataState
        state={models}
        empty={
          <EmptyState title="No models yet" action={add || undefined}>
            {scope === "platform"
              ? "Add a provider first, then add a model that routes to it. It appears in the public catalog when enabled."
              : "Private models route to this organization's own providers."}
          </EmptyState>
        }
      >
        {(all) => {
          const needle = q.trim().toLowerCase();
          const rows = all
            .filter((m) => scope === "platform" || owner === "all" || m.owner === owner)
            .filter((m) => !needle || m.id.toLowerCase().includes(needle) || m.name.toLowerCase().includes(needle))
            .sort((a, b) => a.id.localeCompare(b.id));
          if (rows.length === 0)
            return (
              <EmptyState
                title="No models match"
                action={
                  <Button variant="outline" onClick={() => { setQ(""); setOwner("all"); }}>
                    Clear filters
                  </Button>
                }
              />
            );
          return (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Model</TableHead>
                    <TableHead>Kind</TableHead>
                    <TableHead className="text-right">Context</TableHead>
                    <TableHead className="text-right">Max output</TableHead>
                    <TableHead className="text-right">Input</TableHead>
                    <TableHead className="text-right">Output</TableHead>
                    <TableHead>Routes</TableHead>
                    <TableHead>State</TableHead>
                    {scope === "org" && <TableHead>Owner</TableHead>}
                    {admin && <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((m) => (
                    <TableRow key={m.id}>
                      <TableCell className="min-w-48">
                        {m.owner === "platform" && m.enabled ? (
                          <Link href={"~" + modelHref(m.id)} /* "~" escapes the nested /app router */ className="font-medium underline-offset-4 hover:underline">
                            {m.name}
                          </Link>
                        ) : (
                          <span className="font-medium">{m.name}</span>
                        )}
                        <div className="font-mono text-xs text-muted-foreground">{m.id}</div>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">{kindLabel[m.kind] ?? m.kind}</TableCell>
                      <TableCell className="text-right tabular-nums">{m.context_length ? compact(m.context_length) : "–"}</TableCell>
                      <TableCell className="text-right tabular-nums">{m.max_output_tokens ? compact(m.max_output_tokens) : "–"}</TableCell>
                      <TableCell className="text-right tabular-nums whitespace-nowrap">{mainPrice(m)}</TableCell>
                      <TableCell className="text-right tabular-nums">{m.kind !== "chat" && Number(m.pricing.output) === 0 ? "–" : perMillion(m.pricing.output)}</TableCell>
                      <TableCell className="text-xs">
                        <Routes model={m} provName={provName} />
                      </TableCell>
                      <TableCell><EnabledText on={m.enabled} /></TableCell>
                      {scope === "org" && (
                        <TableCell>
                          <Badge variant={m.owner === "org" ? "secondary" : "outline"}>{m.owner === "org" ? "Private" : "Public"}</Badge>
                        </TableCell>
                      )}
                      {admin && (
                        <TableCell>
                          {editable(m) && (
                            <div className="flex justify-end gap-1">
                              <Button size="icon" variant="ghost" aria-label={`Edit ${m.name}`} onClick={() => setEditing(m)}>
                                <Pencil aria-hidden />
                              </Button>
                              <Button size="icon" variant="ghost" aria-label={`Delete ${m.name}`} onClick={() => setDeleting(m)}>
                                <Trash2 aria-hidden />
                              </Button>
                            </div>
                          )}
                        </TableCell>
                      )}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          );
        }}
      </DataState>

      {editing && (
        <ModelDialog
          key={editing === "new" ? "new" : editing.id}
          scope={scope}
          base={base}
          model={editing === "new" ? undefined : editing}
          providers={providers.data ?? []}
          onClose={() => setEditing(null)}
          onSaved={models.reload}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "model"}?`}
        description={
          <>
            Requests for <span className="font-mono">{deleting?.id}</span> fail with model_not_found, and API keys restricted to it lose access.
          </>
        }
        confirmLabel="Delete model"
        success="Model deleted"
        onConfirm={async () => {
          await api("DELETE", `${base}/${pathID(deleting!.id)}`);
          models.reload();
        }}
      />
      <ConfirmDialog
        open={loadingCatalog}
        onOpenChange={setLoadingCatalog}
        title="Load the model catalog?"
        description={
          <>
            Adds the built-in catalog (from models.dev): one disabled provider per vendor, with no API key, and every
            catalog model routed to them. Existing providers and models are not changed, except that catalog models
            without cache prices get the catalog&apos;s cache read and write prices. Catalog models stay hidden
            until you add an API key to their provider and enable it in Providers.
          </>
        }
        confirmLabel="Load catalog"
        destructive={false}
        success={(r: CatalogSync) =>
          `Added ${r.providers_added} providers and ${r.models_added} models` +
          (r.cache_prices_filled ? `; filled cache prices on ${r.cache_prices_filled} models` : "")
        }
        onConfirm={async () => {
          const r = await api<CatalogSync>("POST", PLATFORM + "/catalog/sync");
          models.reload();
          providers.reload();
          return r;
        }}
      />
    </>
  );
}

/** Routing strategies with a one-line explanation each (docs/API.md "Routing"). */
const strategies: { value: Strategy; label: string; help: string }[] = [
  { value: "weighted", label: "Weighted", help: "Picks the primary at random in proportion to route weights." },
  { value: "priority", label: "Priority", help: "Always tries routes in the order listed." },
  { value: "round-robin", label: "Round robin", help: "Rotates the primary route on every request." },
  { value: "random", label: "Random", help: "Picks the primary uniformly at random, ignoring weights." },
  { value: "p2c", label: "Power of two choices", help: "Samples two routes and prefers the one with better success rate and latency." },
  { value: "least-used", label: "Least used", help: "Prefers the route with the fewest requests in flight on this node." },
  { value: "latency", label: "Lowest latency", help: "Prefers the route with the lowest average latency and error rate." },
  { value: "cost", label: "Lowest cost", help: "Prefers the cheapest route, using per-route prices when set." },
  { value: "cache-affinity", label: "Cache affinity", help: "Sends the same prompt prefix to the same route so provider prompt caches hit." },
];

// Route row columns: provider, upstream model, weight, [input price, output price,] remove.
const plainCols = "sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_5rem_2.25rem]";
const costCols = "sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_4.5rem_5.5rem_5.5rem_2.25rem]";

function Routes({ model, provName }: { model: ModelView; provName: Map<string, string> }) {
  if (!model.targets) return <span className="text-muted-foreground">Managed by the platform</span>;
  if (model.targets.length === 0) return <span className="text-destructive">No routes</span>;
  const strategy = strategies.find((s) => s.value === (model.strategy || "weighted"));
  return (
    <ul className="space-y-0.5">
      {model.targets.length > 1 && <li className="text-muted-foreground">{strategy?.label ?? model.strategy}</li>}
      {model.targets.map((t, i) => (
        <li key={i} className="whitespace-nowrap">
          <span>{provName.get(t.provider_id) ?? t.provider_id}</span>
          <span className="text-muted-foreground">: </span>
          <span className="font-mono">{t.model}</span>
          <span className="text-muted-foreground"> ×{t.weight}</span>
        </li>
      ))}
    </ul>
  );
}

const kindHelp: Record<ModelInput["kind"], string> = {
  chat: "Served on /v1/chat/completions, /v1/completions and /v1/responses.",
  embedding: "Served on /v1/embeddings.",
  speech: "Text in, audio out on /v1/audio/speech.",
  transcription: "Audio in, text out on /v1/audio/transcriptions.",
  image: "Generates and edits images on /v1/images/generations, /edits and /variations.",
  rerank: "Orders documents by relevance to a query on /v1/rerank.",
  ocr: "Extracts text from documents and images on /v1/ocr (Mistral-type providers).",
  video: "Generates video as an async job on /v1/videos.",
};

/** Price fields shown per kind; request price is offered for every kind under "Advanced". */
const priceFields: Record<ModelInput["kind"], { tokens: boolean; audio?: boolean; image?: boolean; request?: boolean; video?: boolean }> = {
  chat: { tokens: true },
  embedding: { tokens: true },
  speech: { tokens: true },
  transcription: { tokens: true, audio: true },
  image: { tokens: true, image: true },
  rerank: { tokens: true, request: true },
  ocr: { tokens: true, request: true },
  video: { tokens: false, video: true },
};

type TargetRow = { provider_id: string; model: string; weight: string; inPrice: string; outPrice: string };

/** micro-USD per 1M tokens → the USD string shown in a price input ("" when unset). */
const usdInput = (m?: number) => (m ? String(m / 1e6) : "");

function ModelDialog({
  scope, base, model, providers, onClose, onSaved,
}: { scope: Scope; base: string; model?: ModelView; providers: Provider[]; onClose: () => void; onSaved: () => void }) {
  const { wsBody } = useSession();
  const [open, setOpen] = useState(true);
  const [id, setId] = useState(model?.id ?? "");
  const [name, setName] = useState(model?.name ?? "");
  const [desc, setDesc] = useState(model?.description ?? "");
  const [kind, setKind] = useState<ModelInput["kind"]>(model?.kind ?? "chat");
  const [ctx, setCtx] = useState(model?.context_length ? String(model.context_length) : "");
  const [maxOut, setMaxOut] = useState(model?.max_output_tokens ? String(model.max_output_tokens) : "");
  const [mods, setMods] = useState<string[]>(model?.modalities?.length ? model.modalities : ["text"]);
  const [inPrice, setInPrice] = useState(model ? model.pricing.input : "");
  const [outPrice, setOutPrice] = useState(model ? model.pricing.output : "");
  const [cacheRead, setCacheRead] = useState(model?.pricing.input_cache_read ?? "");
  const [cacheWrite, setCacheWrite] = useState(model?.pricing.input_cache_write ?? "");
  const [minPrice, setMinPrice] = useState(model?.pricing.audio_minute ?? "");
  const [imgPrice, setImgPrice] = useState(model?.pricing.image ?? "");
  const [reqPrice, setReqPrice] = useState(model?.pricing.request ?? "");
  const [vidPrice, setVidPrice] = useState(model?.pricing.video_second ?? "");
  const [enabled, setEnabled] = useState(model?.enabled ?? true);
  const [strategy, setStrategy] = useState<Strategy>(model?.strategy || "weighted");
  const newRow = (): TargetRow => ({ provider_id: providers[0]?.id ?? "", model: "", weight: "1", inPrice: "", outPrice: "" });
  const [targets, setTargets] = useState<TargetRow[]>(
    model?.targets?.length
      ? model.targets.map((t) => ({
          provider_id: t.provider_id,
          model: t.model,
          weight: String(t.weight),
          inPrice: usdInput(t.input_price_micros),
          outPrice: usdInput(t.output_price_micros),
        }))
      : [newRow()],
  );
  const cost = strategy === "cost";

  const setTarget = (i: number, patch: Partial<TargetRow>) => setTargets((ts) => ts.map((t, j) => (j === i ? { ...t, ...patch } : t)));
  const toggleMod = (m: string, on: boolean) => setMods((ms) => (on ? [...new Set([...ms, m])] : ms.filter((x) => x !== m)));
  const micros = (s: string, label: string) => {
    const v = s.trim() === "" ? 0 : Number(s);
    if (!Number.isFinite(v) || v < 0) throw new Error(`${label} must be a price of 0 or more.`);
    return Math.round(v * 1e6);
  };

  const textModel = kind === "chat" || kind === "embedding";
  const pf = priceFields[kind];
  // The request price lives under "Advanced" unless the kind is priced per request.
  const [advanced, setAdvanced] = useState(!pf.request && !!model?.pricing.request);

  const price = (label: string, value: string, set: (v: string) => void, placeholder: string, hint?: string) => (
    <Field label={label} hint={hint}>
      {(fid, h) => <Input id={fid} aria-describedby={h} type="number" min={0} step="any" value={value} onChange={(e) => set(e.target.value)} placeholder={placeholder} />}
    </Field>
  );

  const save = async () => {
    const ts: Target[] = targets.map((t, i) => {
      if (!t.provider_id) throw new Error(`Route ${i + 1}: choose a provider.`);
      if (!t.model.trim()) throw new Error(`Route ${i + 1}: enter the upstream model name.`);
      const w = Number(t.weight);
      if (!(w > 0)) throw new Error(`Route ${i + 1}: weight must be greater than 0.`);
      // Route prices are kept even when another strategy is chosen, so switching back loses nothing.
      const tin = t.inPrice.trim() ? micros(t.inPrice, `Route ${i + 1} input price`) : undefined;
      const tout = t.outPrice.trim() ? micros(t.outPrice, `Route ${i + 1} output price`) : undefined;
      return { provider_id: t.provider_id, model: t.model.trim(), weight: w, input_price_micros: tin, output_price_micros: tout };
    });
    if (ts.length === 0) throw new Error("Add at least one route.");
    const body: ModelInput = {
      id: id.trim(),
      name: name.trim(),
      description: desc.trim() || undefined,
      kind,
      context_length: textModel && ctx ? Number(ctx) : undefined,
      max_output_tokens: textModel && maxOut ? Number(maxOut) : undefined,
      modalities: textModel && mods.length ? mods : ["text"],
      input_price_micros: pf.tokens ? micros(inPrice, "Input price") : 0,
      output_price_micros: pf.tokens ? micros(outPrice, "Output price") : 0,
      cache_read_price_micros: kind === "chat" ? micros(cacheRead, "Cache read price") : undefined,
      cache_write_price_micros: kind === "chat" ? micros(cacheWrite, "Cache write price") : undefined,
      audio_minute_price_micros: pf.audio ? micros(minPrice, "Audio price") : undefined,
      image_price_micros: pf.image ? micros(imgPrice, "Image price") : undefined,
      video_second_price_micros: pf.video ? micros(vidPrice, "Video price") : undefined,
      request_price_micros: pf.request || advanced ? micros(reqPrice, "Request price") : undefined,
      targets: ts,
      strategy,
      enabled,
    };
    if (model) await api("PUT", `${base}/${pathID(model.id)}`, body);
    else await api("POST", base, scope === "org" ? { ...body, ...wsBody } : body);
    onSaved();
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      wide
      title={model ? `Edit ${model.name}` : scope === "platform" ? "Add public model" : "Add private model"}
      description={
        scope === "platform"
          ? "Public models appear in the catalog and are callable by every organization when enabled."
          : "Private models are callable only by this organization and route to its own providers."
      }
      submitLabel={model ? "Save changes" : "Add model"}
      success={model ? "Model saved" : "Model added"}
      onSubmit={save}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Model id" hint={model ? "The id can't be changed." : "vendor/name, used as model in API requests."}>
          {(fid, hint) => (
            <Input
              id={fid}
              aria-describedby={hint}
              value={id}
              onChange={(e) => setId(e.target.value)}
              readOnly={!!model}
              tabIndex={model ? -1 : undefined}
              required
              pattern="[A-Za-z0-9._:\-]+\/[A-Za-z0-9._:\-\/]+"
              placeholder="acme/chat-small"
              className={model ? "bg-muted font-mono text-muted-foreground" : "font-mono"}
              autoComplete="off"
              spellCheck={false}
            />
          )}
        </Field>
        <Field label="Display name">
          {(fid) => <Input id={fid} value={name} onChange={(e) => setName(e.target.value)} required placeholder="Acme Chat Small" />}
        </Field>
      </div>
      <Field label="Description" hint="Optional. Shown in the catalog.">
        {(fid, hint) => <Textarea id={fid} aria-describedby={hint} rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} />}
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="model-kind">Kind</Label>
          <Select value={kind} onValueChange={(v) => setKind(v as ModelInput["kind"])}>
            <SelectTrigger id="model-kind" className="w-full" aria-describedby="model-kind-help">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {Object.entries(kindLabel).map(([v, label]) => (
                <SelectItem key={v} value={v}>{label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p id="model-kind-help" className="text-xs text-muted-foreground">{kindHelp[kind]}</p>
        </div>
        {textModel && (
          <>
            <Field label="Context length">
              {(fid) => <Input id={fid} type="number" min={0} step={1} value={ctx} onChange={(e) => setCtx(e.target.value)} placeholder="128000" />}
            </Field>
            <Field label="Max output tokens">
              {(fid) => <Input id={fid} type="number" min={0} step={1} value={maxOut} onChange={(e) => setMaxOut(e.target.value)} placeholder="16384" />}
            </Field>
          </>
        )}
      </div>
      {textModel && <fieldset className="grid gap-2">
        <legend className="mb-1.5 text-sm font-medium">Input modalities</legend>
        <div className="flex gap-6">
          {["text", "image"].map((m) => (
            <div key={m} className="flex items-center gap-2">
              <Checkbox id={`mod-${m}`} checked={mods.includes(m)} onCheckedChange={(c) => toggleMod(m, c === true)} />
              <Label htmlFor={`mod-${m}`} className="font-normal">{m === "text" ? "Text" : "Image"}</Label>
            </div>
          ))}
        </div>
      </fieldset>}
      <div className="grid gap-4 sm:grid-cols-3">
        {pf.audio && price("Audio price, USD per minute", minPrice, setMinPrice, "0.006", "Charged per minute of input audio.")}
        {pf.image && price("Image price, USD per image", imgPrice, setImgPrice, "0.04", "Charged per generated image.")}
        {pf.video && price("Video price, USD per second", vidPrice, setVidPrice, "0.10", "Charged per second of generated video, once the job completes.")}
        {pf.request && price("Request price, USD per request", reqPrice, setReqPrice, "0.002", "Charged per successful request.")}
        {pf.tokens &&
          price(
            kind === "speech" ? "Input price (USD per 1M characters or tokens)" : "Input price (USD per 1M tokens)",
            inPrice,
            setInPrice,
            "0.15",
            kind === "speech" ? "Per 1M characters when the provider reports no token usage." : textModel ? undefined : "Optional, when the provider reports tokens.",
          )}
        {pf.tokens &&
          price("Output price (USD per 1M tokens)", outPrice, setOutPrice, "0.60", textModel ? undefined : "Optional, when the provider reports output tokens.")}
      </div>
      {kind === "chat" && (
        <div className="grid gap-4 sm:grid-cols-3">
          {price("Cache read (USD per 1M tokens)", cacheRead, setCacheRead, inPrice || "0.075", "Optional. Leave empty to bill cached tokens at the input price.")}
          {price("Cache write (USD per 1M tokens)", cacheWrite, setCacheWrite, inPrice || "0.1875", "Optional. Leave empty to bill cached tokens at the input price.")}
        </div>
      )}
      {!pf.request && (
        <div className="grid gap-3">
          <div className="flex items-center gap-3">
            <Switch id="model-advanced" checked={advanced} onCheckedChange={setAdvanced} aria-describedby="model-advanced-help" />
            <Label htmlFor="model-advanced">Also charge per request</Label>
          </div>
          <p id="model-advanced-help" className="-mt-2 text-xs text-muted-foreground">Advanced. A flat fee added to every successful request, on top of the prices above.</p>
          {advanced && <div className="grid gap-4 sm:grid-cols-3">{price("Request price, USD per request", reqPrice, setReqPrice, "0.001")}</div>}
        </div>
      )}

      <div className="grid gap-1.5 sm:max-w-sm">
        <Label htmlFor="model-strategy">Routing strategy</Label>
        <Select value={strategy} onValueChange={(v) => setStrategy(v as Strategy)}>
          <SelectTrigger id="model-strategy" className="w-full" aria-describedby="model-strategy-help">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {strategies.map((s) => (
              <SelectItem key={s.value} value={s.value}>{s.label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p id="model-strategy-help" className="text-xs text-muted-foreground">
          {strategies.find((s) => s.value === strategy)?.help} The other routes are failover in order.
        </p>
      </div>

      <fieldset className="grid gap-2">
        <legend className="text-sm font-medium">Routes</legend>
        <p className="text-xs text-muted-foreground">
          {cost
            ? "Route prices (USD per 1M tokens) rank routes for the cost strategy only; empty uses the model price. Billing always uses the model price."
            : "The strategy picks the primary route; if it fails the next is tried."}
          {scope === "org" ? " Private models route to this organization's providers." : ""}
        </p>
        {providers.length === 0 ? (
          <p className="rounded-md border border-dashed px-3 py-3 text-sm text-muted-foreground">
            No providers yet.{" "}
            <a className="underline underline-offset-4" href={scope === "platform" ? "/app/platform/providers" : "/app/providers"}>
              Add a provider
            </a>{" "}
            first.
          </p>
        ) : (
          <div className="grid gap-2">
            <div className={`hidden gap-2 text-xs text-muted-foreground sm:grid ${cost ? costCols : plainCols}`}>
              <span>Provider</span>
              <span>Upstream model</span>
              <span>Weight</span>
              {cost && (
                <>
                  <span>Input $/1M</span>
                  <span>Output $/1M</span>
                </>
              )}
            </div>
            {targets.map((t, i) => (
              <div key={i} className={`grid grid-cols-[minmax(0,1fr)_5rem_2.25rem] gap-2 ${cost ? costCols : plainCols}`}>
                <Select value={t.provider_id} onValueChange={(v) => setTarget(i, { provider_id: v })}>
                  <SelectTrigger aria-label={`Route ${i + 1} provider`} className="col-span-3 w-full sm:col-span-1">
                    <SelectValue placeholder="Provider" />
                  </SelectTrigger>
                  <SelectContent>
                    {providers.map((p) => (
                      <SelectItem key={p.id} value={p.id}>
                        {p.name}
                        {!p.enabled && " (disabled)"}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Input
                  aria-label={`Route ${i + 1} upstream model`}
                  value={t.model}
                  onChange={(e) => setTarget(i, { model: e.target.value })}
                  placeholder="gpt-4o-mini"
                  className="font-mono"
                  spellCheck={false}
                />
                <Input
                  aria-label={`Route ${i + 1} weight`}
                  type="number"
                  min={0}
                  step="any"
                  value={t.weight}
                  onChange={(e) => setTarget(i, { weight: e.target.value })}
                />
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  aria-label={`Remove route ${i + 1}`}
                  disabled={targets.length === 1}
                  className="sm:order-last"
                  onClick={() => setTargets((ts) => ts.filter((_, j) => j !== i))}
                >
                  <X aria-hidden />
                </Button>
                {cost && (
                  <div className="col-span-3 grid grid-cols-2 gap-2 sm:col-span-2">
                    <Input
                      aria-label={`Route ${i + 1} input price, USD per 1M tokens`}
                      type="number"
                      min={0}
                      step="any"
                      value={t.inPrice}
                      onChange={(e) => setTarget(i, { inPrice: e.target.value })}
                      placeholder={inPrice || "Model"}
                    />
                    <Input
                      aria-label={`Route ${i + 1} output price, USD per 1M tokens`}
                      type="number"
                      min={0}
                      step="any"
                      value={t.outPrice}
                      onChange={(e) => setTarget(i, { outPrice: e.target.value })}
                      placeholder={outPrice || "Model"}
                    />
                  </div>
                )}
              </div>
            ))}
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setTargets((ts) => [...ts, newRow()])}
              >
                <Plus aria-hidden /> Add route
              </Button>
            </div>
          </div>
        )}
      </fieldset>
      <div className="flex items-center gap-3">
        <Switch id="model-enabled" checked={enabled} onCheckedChange={setEnabled} />
        <Label htmlFor="model-enabled">Enabled</Label>
      </div>
    </FormDialog>
  );
}
