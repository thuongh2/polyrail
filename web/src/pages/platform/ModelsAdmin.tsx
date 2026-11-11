import { useMemo, useState } from "react";
import { Pencil, Plus, Trash2, X } from "lucide-react";
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
  api, compact, get, orgBase, pathID, perMillion, PLATFORM, type ModelInput, type ModelView, type Provider, type Target,
} from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

export default function ModelsAdmin({ scope = "org" }: { scope?: Scope }) {
  const { org, can, user } = useSession();
  const base = scope === "platform" ? PLATFORM + "/models" : org ? orgBase(org.id) + "/models" : null;
  const admin = scope === "platform" ? !!user?.operator : can("admin");
  const provBase = admin ? providersPath(scope, org?.id) : null;

  const models = useAsync(() => (base ? get<ModelView[]>(base) : Promise.resolve([] as ModelView[])), [base]);
  const providers = useAsync(() => (provBase ? get<Provider[]>(provBase) : Promise.resolve([] as Provider[])), [provBase]);
  const provName = useMemo(() => new Map((providers.data ?? []).map((p) => [p.id, p.name])), [providers.data]);

  const [owner, setOwner] = useQueryParam("owner", "all");
  const [q, setQ] = useQueryParam("q");
  const [editing, setEditing] = useState<ModelView | "new" | null>(null);
  const [deleting, setDeleting] = useState<ModelView | null>(null);

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

  return (
    <>
      <PageHeader title={title} description={description} actions={add || undefined} />
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
                    <TableHead className="text-right">Input / 1M</TableHead>
                    <TableHead className="text-right">Output / 1M</TableHead>
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
                          <a href={`/models/${m.id}`} className="font-medium underline-offset-4 hover:underline">
                            {m.name}
                          </a>
                        ) : (
                          <span className="font-medium">{m.name}</span>
                        )}
                        <div className="font-mono text-xs text-muted-foreground">{m.id}</div>
                      </TableCell>
                      <TableCell>{m.kind === "embedding" ? "Embedding" : "Chat"}</TableCell>
                      <TableCell className="text-right tabular-nums">{m.context_length ? compact(m.context_length) : "–"}</TableCell>
                      <TableCell className="text-right tabular-nums">{m.max_output_tokens ? compact(m.max_output_tokens) : "–"}</TableCell>
                      <TableCell className="text-right tabular-nums">{perMillion(m.pricing.input)}</TableCell>
                      <TableCell className="text-right tabular-nums">{m.kind === "embedding" && Number(m.pricing.output) === 0 ? "–" : perMillion(m.pricing.output)}</TableCell>
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
    </>
  );
}

function Routes({ model, provName }: { model: ModelView; provName: Map<string, string> }) {
  if (!model.targets) return <span className="text-muted-foreground">Managed by the platform</span>;
  if (model.targets.length === 0) return <span className="text-destructive">No routes</span>;
  return (
    <ul className="space-y-0.5">
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

type TargetRow = { provider_id: string; model: string; weight: string };

function ModelDialog({
  scope, base, model, providers, onClose, onSaved,
}: { scope: Scope; base: string; model?: ModelView; providers: Provider[]; onClose: () => void; onSaved: () => void }) {
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
  const [enabled, setEnabled] = useState(model?.enabled ?? true);
  const [targets, setTargets] = useState<TargetRow[]>(
    model?.targets?.length
      ? model.targets.map((t) => ({ provider_id: t.provider_id, model: t.model, weight: String(t.weight) }))
      : [{ provider_id: providers[0]?.id ?? "", model: "", weight: "1" }],
  );

  const setTarget = (i: number, patch: Partial<TargetRow>) => setTargets((ts) => ts.map((t, j) => (j === i ? { ...t, ...patch } : t)));
  const toggleMod = (m: string, on: boolean) => setMods((ms) => (on ? [...new Set([...ms, m])] : ms.filter((x) => x !== m)));
  const micros = (s: string, label: string) => {
    const v = s.trim() === "" ? 0 : Number(s);
    if (!Number.isFinite(v) || v < 0) throw new Error(`${label} must be a price of 0 or more.`);
    return Math.round(v * 1e6);
  };

  const save = async () => {
    const ts: Target[] = targets.map((t, i) => {
      if (!t.provider_id) throw new Error(`Route ${i + 1}: choose a provider.`);
      if (!t.model.trim()) throw new Error(`Route ${i + 1}: enter the upstream model name.`);
      const w = Number(t.weight);
      if (!(w > 0)) throw new Error(`Route ${i + 1}: weight must be greater than 0.`);
      return { provider_id: t.provider_id, model: t.model.trim(), weight: w };
    });
    if (ts.length === 0) throw new Error("Add at least one route.");
    const body: ModelInput = {
      id: id.trim(),
      name: name.trim(),
      description: desc.trim() || undefined,
      kind,
      context_length: ctx ? Number(ctx) : undefined,
      max_output_tokens: maxOut ? Number(maxOut) : undefined,
      modalities: mods.length ? mods : ["text"],
      input_price_micros: micros(inPrice, "Input price"),
      output_price_micros: micros(outPrice, "Output price"),
      targets: ts,
      enabled,
    };
    if (model) await api("PUT", `${base}/${pathID(model.id)}`, body);
    else await api("POST", base, body);
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
            <SelectTrigger id="model-kind" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="chat">Chat</SelectItem>
              <SelectItem value="embedding">Embedding</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Field label="Context length">
          {(fid) => <Input id={fid} type="number" min={0} step={1} value={ctx} onChange={(e) => setCtx(e.target.value)} placeholder="128000" />}
        </Field>
        <Field label="Max output tokens">
          {(fid) => <Input id={fid} type="number" min={0} step={1} value={maxOut} onChange={(e) => setMaxOut(e.target.value)} placeholder="16384" />}
        </Field>
      </div>
      <fieldset className="grid gap-2">
        <legend className="mb-1.5 text-sm font-medium">Input modalities</legend>
        <div className="flex gap-6">
          {["text", "image"].map((m) => (
            <div key={m} className="flex items-center gap-2">
              <Checkbox id={`mod-${m}`} checked={mods.includes(m)} onCheckedChange={(c) => toggleMod(m, c === true)} />
              <Label htmlFor={`mod-${m}`} className="font-normal">{m === "text" ? "Text" : "Image"}</Label>
            </div>
          ))}
        </div>
      </fieldset>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Input price (USD per 1M tokens)">
          {(fid) => <Input id={fid} type="number" min={0} step="any" value={inPrice} onChange={(e) => setInPrice(e.target.value)} placeholder="0.15" />}
        </Field>
        <Field label="Output price (USD per 1M tokens)">
          {(fid) => <Input id={fid} type="number" min={0} step="any" value={outPrice} onChange={(e) => setOutPrice(e.target.value)} placeholder="0.60" />}
        </Field>
      </div>

      <fieldset className="grid gap-2">
        <legend className="text-sm font-medium">Routes</legend>
        <p className="text-xs text-muted-foreground">
          Requests are spread across routes by weight; if one fails the next is tried.
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
            <div className="hidden grid-cols-[minmax(0,1fr)_minmax(0,1fr)_5rem_2.25rem] gap-2 text-xs text-muted-foreground sm:grid">
              <span>Provider</span>
              <span>Upstream model</span>
              <span>Weight</span>
            </div>
            {targets.map((t, i) => (
              <div key={i} className="grid grid-cols-[minmax(0,1fr)_5rem_2.25rem] gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_5rem_2.25rem]">
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
                  onClick={() => setTargets((ts) => ts.filter((_, j) => j !== i))}
                >
                  <X aria-hidden />
                </Button>
              </div>
            ))}
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setTargets((ts) => [...ts, { provider_id: providers[0]?.id ?? "", model: "", weight: "1" }])}
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
