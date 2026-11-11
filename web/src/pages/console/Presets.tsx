import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Section, TableFrame } from "@/components/console/bits";
import { CheckList, ChipInput, Part, SwitchRow, providerOptions } from "@/components/console/pickers";
import { ConfirmDialog, Field, FormDialog } from "@/components/platform/kit";
import { apiBase, CodeBlock } from "@/components/public/CodeTabs";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api, get, orgBase, type ModelView, type Preset, type PresetParams, type ProviderPrefs } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const MAX_FALLBACKS = 5;
const SLUG = /^[a-z0-9][a-z0-9-]{0,62}$/;
const sortBy = (s: ProviderPrefs["sort"]) => (typeof s === "string" ? s : s?.by) ?? "";

function routingText(p?: ProviderPrefs) {
  if (!p) return "";
  const parts = [
    p.order?.length ? `order ${p.order.join(" → ")}` : "",
    p.only?.length ? `only ${p.only.join(", ")}` : "",
    p.ignore?.length ? `ignore ${p.ignore.join(", ")}` : "",
    sortBy(p.sort) ? `by ${sortBy(p.sort)}` : "",
    p.max_price ? "price cap" : "",
    p.allow_fallbacks === false ? "no fallbacks" : "",
  ];
  return parts.filter(Boolean).join(" · ");
}

function paramsText(p?: PresetParams) {
  if (!p) return "";
  return [
    p.temperature !== undefined ? `temperature ${p.temperature}` : "",
    p.top_p !== undefined ? `top_p ${p.top_p}` : "",
    p.max_tokens !== undefined ? `max_tokens ${p.max_tokens}` : "",
    p.system ? "system prompt" : "",
  ]
    .filter(Boolean)
    .join(", ");
}

const snippet = (slug: string) => `curl ${apiBase()}/chat/completions \\
  -H "Authorization: Bearer $POLYRAIL_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "@preset/${slug || "<slug>"}",
    "messages": [{"role": "user", "content": "Hello"}]
  }'`;

export default function Presets() {
  const { org, can } = useSession();
  const admin = can("admin");
  const base = org ? orgBase(org.id) : "";
  const presets = useAsync(() => (base ? get<Preset[]>(`${base}/presets`) : Promise.resolve([] as Preset[])), [base]);
  const [editing, setEditing] = useState<Preset | "new" | null>(null);
  const [deleting, setDeleting] = useState<Preset | null>(null);
  const [shown, setShown] = useQueryParam("preset", "");

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const create = admin && (
    <Button size="sm" onClick={() => setEditing("new")}>
      <Plus aria-hidden />
      Create preset
    </Button>
  );

  return (
    <>
      <PageHeader
        title="Presets"
        description={
          <>
            Saved models, provider routing and parameters, used as <code className="font-mono">"model": "@preset/slug"</code>. Fields sent with the
            request win over the preset's.{admin ? "" : " Only admins can change presets."}
          </>
        }
        actions={create}
      />
      <DataState
        state={presets}
        rows={3}
        empty={
          <EmptyState title="No presets yet" action={create}>
            A preset keeps a model, its fallbacks, provider preferences and parameters such as a system prompt under one name, so code can switch
            them without a deploy.
          </EmptyState>
        }
      >
        {(list) => {
          const current = list.find((p) => p.slug === shown) ?? list[0];
          return (
            <>
              <TableFrame>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Preset</TableHead>
                      <TableHead>Models</TableHead>
                      <TableHead>Provider routing</TableHead>
                      <TableHead>Parameters</TableHead>
                      {admin && (
                        <TableHead>
                          <span className="sr-only">Actions</span>
                        </TableHead>
                      )}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {list.map((p) => (
                      <TableRow key={p.slug}>
                        <TableCell className="font-mono text-xs whitespace-nowrap">@preset/{p.slug}</TableCell>
                        <TableCell className="max-w-72">
                          <span className="block truncate font-mono text-xs" title={[p.model, ...(p.models ?? [])].filter(Boolean).join(", ")}>
                            {p.model || p.models?.[0]}
                          </span>
                          {!!p.models?.length && <span className="text-xs text-muted-foreground">then {p.models.length} fallback{p.models.length === 1 ? "" : "s"}</span>}
                        </TableCell>
                        <TableCell className="max-w-64">
                          <div className="flex flex-wrap items-center gap-1">
                            {p.provider?.zdr && <Badge variant="outline">ZDR</Badge>}
                            {p.provider?.data_collection === "deny" && <Badge variant="outline">No training</Badge>}
                            <span className="truncate text-xs" title={routingText(p.provider)}>
                              {routingText(p.provider) || (!p.provider?.zdr && p.provider?.data_collection !== "deny" && <span className="text-muted-foreground">Model default</span>)}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="text-xs">{paramsText(p.params) || <span className="text-muted-foreground">None</span>}</TableCell>
                        {admin && (
                          <TableCell className="text-right whitespace-nowrap">
                            <Button variant="ghost" size="icon" aria-label={`Edit ${p.slug}`} title="Edit" onClick={() => setEditing(p)}>
                              <Pencil aria-hidden />
                            </Button>
                            <Button variant="ghost" size="icon" className="text-destructive hover:text-destructive" aria-label={`Delete ${p.slug}`} title="Delete" onClick={() => setDeleting(p)}>
                              <Trash2 aria-hidden />
                            </Button>
                          </TableCell>
                        )}
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </TableFrame>
              <Section
                title="Use a preset"
                description="Any API key of this organization can use its presets."
                actions={
                  list.length > 1 && (
                    <Select value={current.slug} onValueChange={setShown}>
                      <SelectTrigger size="sm" className="w-48" aria-label="Preset for the example">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {list.map((p) => (
                          <SelectItem key={p.slug} value={p.slug}>
                            {p.slug}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )
                }
              >
                <CodeBlock code={snippet(current.slug)} />
              </Section>
            </>
          );
        }}
      </DataState>

      {editing && (
        <PresetDialog
          key={editing === "new" ? "new" : editing.slug}
          base={base}
          preset={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={presets.reload}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete @preset/${deleting?.slug}?`}
        description="Requests that name this preset start failing with 404 preset_not_found."
        confirmLabel="Delete preset"
        success="Preset deleted"
        onConfirm={async () => {
          await api("DELETE", `${base}/presets/${encodeURIComponent(deleting!.slug)}`);
          presets.reload();
        }}
      />
    </>
  );
}

const num = (s: string) => (s.trim() === "" ? undefined : Number(s));
const str = (n?: number) => (n === undefined ? "" : String(n));

function PresetDialog({ base, preset, onClose, onSaved }: { base: string; preset?: Preset; onClose: () => void; onSaved: () => void }) {
  const [open, setOpen] = useState(true);
  const models = useAsync(() => get<ModelView[]>(`${base}/models`), [base]);
  const providers = useAsync(() => providerOptions(base), [base]);
  const pp = preset?.provider ?? {};
  const [slug, setSlug] = useState(preset?.slug ?? "");
  const [model, setModel] = useState(preset?.model ?? "");
  const [fallbacks, setFallbacks] = useState(preset?.models ?? []);
  const [order, setOrder] = useState(pp.order ?? []);
  const [only, setOnly] = useState(pp.only ?? []);
  const [ignore, setIgnore] = useState(pp.ignore ?? []);
  const [sort, setSort] = useState(sortBy(pp.sort) || "default");
  const [maxIn, setMaxIn] = useState(str(pp.max_price?.prompt));
  const [maxOut, setMaxOut] = useState(str(pp.max_price?.completion));
  const [zdr, setZdr] = useState(!!pp.zdr);
  const [denyDC, setDenyDC] = useState(pp.data_collection === "deny");
  const [fallbackOK, setFallbackOK] = useState(pp.allow_fallbacks !== false);
  const pr = preset?.params ?? {};
  const [temperature, setTemperature] = useState(str(pr.temperature));
  const [topP, setTopP] = useState(str(pr.top_p));
  const [maxTokens, setMaxTokens] = useState(str(pr.max_tokens));
  const [system, setSystem] = useState(pr.system ?? "");

  const chat = (models.data ?? []).filter((m) => m.kind === "chat");
  const modelOptions = model && !chat.some((m) => m.id === model) ? [{ id: model }, ...chat] : chat;
  const slugBad = !!slug && !SLUG.test(slug);

  const save = async () => {
    if (!SLUG.test(slug)) throw new Error("Slug: lowercase letters, digits and -, starting with a letter or digit (at most 63).");
    if (!model) throw new Error("Choose a model.");
    if (fallbacks.length > MAX_FALLBACKS) throw new Error(`At most ${MAX_FALLBACKS} fallback models.`);
    const nums = { temperature: num(temperature), top_p: num(topP), max_tokens: num(maxTokens), prompt: num(maxIn), completion: num(maxOut) };
    if (Object.values(nums).some((v) => v !== undefined && (!Number.isFinite(v) || v < 0))) throw new Error("Numbers must be 0 or more.");
    if (nums.max_tokens !== undefined && !Number.isInteger(nums.max_tokens)) throw new Error("Max tokens must be a whole number.");
    const provider: ProviderPrefs = {
      order: order.length ? order : undefined,
      only: only.length ? only : undefined,
      ignore: ignore.length ? ignore : undefined,
      sort: sort === "default" ? undefined : sort,
      max_price: nums.prompt !== undefined || nums.completion !== undefined ? { prompt: nums.prompt, completion: nums.completion } : undefined,
      zdr: zdr || undefined,
      data_collection: denyDC ? "deny" : undefined,
      allow_fallbacks: fallbackOK ? undefined : false,
    };
    const params: PresetParams = { temperature: nums.temperature, top_p: nums.top_p, max_tokens: nums.max_tokens, system: system.trim() ? system : undefined };
    const body = {
      slug,
      model,
      models: fallbacks.length ? fallbacks : undefined,
      provider: Object.values(provider).some((v) => v !== undefined) ? provider : undefined,
      params: Object.values(params).some((v) => v !== undefined) ? params : undefined,
    };
    if (preset) await api("PUT", `${base}/presets/${encodeURIComponent(preset.slug)}`, body);
    else await api("POST", `${base}/presets`, body);
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
      title={preset ? `Edit @preset/${preset.slug}` : "Create preset"}
      description="Leave a field empty to use the model's default. A request's own models, provider and parameters replace the preset's."
      submitLabel={preset ? "Save preset" : "Create preset"}
      success={preset ? "Preset saved" : "Preset created"}
      onSubmit={save}
    >
      <div className="grid items-start gap-4 sm:grid-cols-2">
        <Field label="Slug" hint={slugBad ? <span className="text-destructive">Lowercase letters, digits and -.</span> : "Can't be changed later."}>
          {(id, hint) => (
            <Input
              id={id}
              aria-describedby={hint}
              aria-invalid={slugBad}
              value={slug}
              onChange={(e) => setSlug(e.target.value.toLowerCase())}
              disabled={!!preset}
              required
              maxLength={63}
              placeholder="support-bot"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          )}
        </Field>
        <Field label="Model" hint={models.error ? <span className="text-destructive">{models.error.message}</span> : undefined}>
          {(id, hint) => (
            <Select value={model} onValueChange={setModel}>
              <SelectTrigger id={id} aria-describedby={hint} className="w-full font-mono text-xs">
                <SelectValue placeholder={models.loading ? "Loading models…" : "Choose a model"} />
              </SelectTrigger>
              <SelectContent>
                {modelOptions.map((m) => (
                  <SelectItem key={m.id} value={m.id} className="font-mono text-xs">
                    {m.id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
      </div>
      <ChipInput
        label="Fallback models"
        value={fallbacks}
        onChange={setFallbacks}
        placeholder="vendor/model"
        suggestions={chat.map((m) => m.id).filter((id) => id !== model)}
        help={`Tried in order after the model, at most ${MAX_FALLBACKS}. Press Enter to add.`}
      />

      <Part title="Provider routing" description="Narrow and order the providers that serve these models.">
        <div className="grid gap-4">
          <ChipInput
            label="Try first, in this order"
            value={order}
            onChange={setOrder}
            placeholder="provider name or type"
            suggestions={providers.data?.map((o) => o.value)}
            mono={false}
          />
          <div className="grid gap-4 sm:grid-cols-2">
            <CheckList legend="Only these providers" help="Empty: any provider." options={providers.data} value={only} onChange={setOnly} error={providers.error} loading={providers.loading} />
            <CheckList legend="Never these providers" options={providers.data} value={ignore} onChange={setIgnore} error={providers.error} loading={providers.loading} />
          </div>
          <div className="grid items-start gap-4 sm:grid-cols-3">
            <Field label="Sort providers by">
              {(id) => (
                <Select value={sort} onValueChange={setSort}>
                  <SelectTrigger id={id} className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="default">Model's routing strategy</SelectItem>
                    <SelectItem value="price">Lowest price</SelectItem>
                    <SelectItem value="throughput">Highest throughput</SelectItem>
                    <SelectItem value="latency">Lowest latency</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label="Max input price" hint="USD per 1M tokens">
              {(id, hint) => <Input id={id} aria-describedby={hint} type="number" min={0} step="any" inputMode="decimal" placeholder="No cap" value={maxIn} onChange={(e) => setMaxIn(e.target.value)} />}
            </Field>
            <Field label="Max output price" hint="USD per 1M tokens">
              {(id, hint) => <Input id={id} aria-describedby={hint} type="number" min={0} step="any" inputMode="decimal" placeholder="No cap" value={maxOut} onChange={(e) => setMaxOut(e.target.value)} />}
            </Field>
          </div>
          <SwitchRow id="pre-zdr" label="Zero data retention only" help="Only providers that declare they keep no prompts or outputs.">
            <Switch id="pre-zdr" checked={zdr} onCheckedChange={setZdr} />
          </SwitchRow>
          <SwitchRow id="pre-dc" label="Deny data collection" help="Skip providers that declare they train on prompts or retain them.">
            <Switch id="pre-dc" checked={denyDC} onCheckedChange={setDenyDC} />
          </SwitchRow>
          <SwitchRow id="pre-fb" label="Allow provider fallbacks" help="Off: only the first eligible provider of each model is tried (fallback models still apply).">
            <Switch id="pre-fb" checked={fallbackOK} onCheckedChange={setFallbackOK} />
          </SwitchRow>
        </div>
      </Part>

      <Part title="Parameters" description="Applied when the request does not set them.">
        <div className="grid gap-4">
          <div className="grid items-start gap-4 sm:grid-cols-3">
            <Field label="Temperature">
              {(id) => <Input id={id} type="number" min={0} max={2} step="0.1" inputMode="decimal" placeholder="Default" value={temperature} onChange={(e) => setTemperature(e.target.value)} />}
            </Field>
            <Field label="Top P">
              {(id) => <Input id={id} type="number" min={0} max={1} step="0.05" inputMode="decimal" placeholder="Default" value={topP} onChange={(e) => setTopP(e.target.value)} />}
            </Field>
            <Field label="Max tokens">
              {(id) => <Input id={id} type="number" min={1} step={1} inputMode="numeric" placeholder="Default" value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} />}
            </Field>
          </div>
          <Field label="System prompt" hint="Added before the request's messages.">
            {(id, hint) => <Textarea id={id} aria-describedby={hint} rows={4} value={system} onChange={(e) => setSystem(e.target.value)} placeholder="You are a concise support assistant." />}
          </Field>
        </div>
      </Part>

      <Part title="Usage">
        <CodeBlock code={snippet(slug)} />
      </Part>
    </FormDialog>
  );
}
