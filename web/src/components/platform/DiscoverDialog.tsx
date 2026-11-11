// Discover a provider's upstream models and import a selection as catalog models (docs/API.md, model import).
import { useMemo, useState } from "react";
import { DataState, EmptyState } from "@/components/states";
import { Field, FormDialog } from "@/components/platform/kit";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, compact, kindLabel, type DiscoveredModel, type ImportModel, type ModelKind, type Provider } from "@/lib/api";
import { useAsync } from "@/lib/hooks";

/** Same rule as the server's reModelID. */
const ID_RE = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._:/-]+$/;

/** Default catalog id: "<provider type>/<upstream>", with characters the id can't hold replaced. */
export const defaultID = (vendor: string, upstream: string) =>
  `${vendor.replace(/[^A-Za-z0-9._-]/g, "-")}/${upstream.replace(/^models\//, "").replace(/[^A-Za-z0-9._:/-]/g, "-")}`;

/** Best guess of a model's kind from its upstream name; the admin can change it. */
export function guessKind(upstream: string): ModelKind {
  const u = upstream.toLowerCase();
  if (/rerank/.test(u)) return "rerank";
  if (/embed/.test(u)) return "embedding";
  if (/ocr/.test(u)) return "ocr";
  if (/whisper|transcri|stt/.test(u)) return "transcription";
  if (/tts|speech/.test(u)) return "speech";
  if (/sora|veo|video|kling|seedance/.test(u)) return "video";
  if (/dall-e|image|imagen|flux|stable-diffusion|sdxl/.test(u)) return "image";
  return "chat";
}

type Row = { id: string; name: string; kind: ModelKind; input: string; output: string };
const usdText = (micros?: number) => (micros == null ? "" : String(micros / 1e6));

export function DiscoverDialog({ base, provider, onClose }: { base: string; provider: Provider; onClose: () => void }) {
  const [open, setOpen] = useState(true);
  const url = `${base}/${encodeURIComponent(provider.id)}`;
  const found = useAsync(() => api<{ models: DiscoveredModel[] }>("POST", `${url}/discover`).then((r) => r.models), [url]);
  const [q, setQ] = useState("");
  const [picked, setPicked] = useState<Record<string, Row>>({}); // by upstream id
  const [errors, setErrors] = useState<Record<string, string>>({}); // by upstream id

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (found.data ?? []).filter((m) => !needle || m.upstream.toLowerCase().includes(needle) || (m.name ?? "").toLowerCase().includes(needle));
  }, [found.data, q]);
  const count = Object.keys(picked).length;

  const rowFor = (m: DiscoveredModel): Row => ({
    id: defaultID(provider.type, m.upstream),
    name: m.name || m.upstream,
    kind: guessKind(m.upstream),
    input: usdText(m.input_price_micros),
    output: usdText(m.output_price_micros),
  });
  const toggle = (ms: DiscoveredModel[], on: boolean) =>
    setPicked((p) => {
      const next = { ...p };
      for (const m of ms) {
        if (on) next[m.upstream] ??= rowFor(m);
        else delete next[m.upstream];
      }
      return next;
    });
  const edit = (upstream: string, patch: Partial<Row>) => setPicked((p) => ({ ...p, [upstream]: { ...p[upstream], ...patch } }));
  const allShown = shown.length > 0 && shown.every((m) => picked[m.upstream]);

  const submit = async () => {
    const byUpstream = new Map((found.data ?? []).map((m) => [m.upstream, m]));
    const errs: Record<string, string> = {};
    const seen = new Map<string, string>();
    const models: ImportModel[] = [];
    for (const [upstream, r] of Object.entries(picked)) {
      const id = r.id.trim();
      const price = (s: string) => (s.trim() === "" ? 0 : Number(s));
      const [inp, out] = [price(r.input), price(r.output)];
      if (!ID_RE.test(id) || id.includes("..")) errs[upstream] = "Id must look like vendor/name (letters, digits, . _ : - /).";
      else if (seen.has(id)) errs[upstream] = `Same id as ${seen.get(id)}.`;
      else if (!(inp >= 0) || !(out >= 0)) errs[upstream] = "Prices must be 0 or more.";
      seen.set(id, upstream);
      const m = byUpstream.get(upstream);
      models.push({
        upstream,
        id,
        name: r.name.trim() || id,
        description: m?.description,
        kind: r.kind,
        context_length: m?.context_length,
        max_output_tokens: m?.max_output_tokens,
        input_price_micros: Math.round(inp * 1e6),
        output_price_micros: Math.round(out * 1e6),
      });
    }
    setErrors(errs);
    if (Object.keys(errs).length) {
      requestAnimationFrame(() => document.querySelector('[role="dialog"] li[aria-invalid="true"]')?.scrollIntoView({ block: "nearest" }));
      throw new Error("Fix the highlighted models.");
    }
    if (models.length === 0) throw new Error("Select at least one model.");
    try {
      await api("POST", `${url}/import`, { models });
    } catch (e) {
      // The server stops at the first bad entry and names its id in quotes; pin the message to that row.
      const id = /"([^"]+)"/.exec((e as Error).message)?.[1];
      const row = models.find((m) => m.id === id);
      if (row) setErrors({ [row.upstream]: (e as Error).message });
      throw e;
    } finally {
      // bring the first rejected row into view once it renders
      requestAnimationFrame(() => document.querySelector('[role="dialog"] li[aria-invalid="true"]')?.scrollIntoView({ block: "nearest" }));
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      wide
      title={`Import models from ${provider.name}`}
      description={`Models the provider lists with its own keys. Each selected model is created enabled, routed to ${provider.name}, ${provider.org_id ? "private to this organization" : "in the public catalog"}.`}
      submitLabel={count ? `Import ${count} model${count === 1 ? "" : "s"}` : "Import"}
      success={`Imported ${count} model${count === 1 ? "" : "s"}`}
      onSubmit={submit}
    >
      <DataState state={found} rows={5} empty={<EmptyState title="No models listed">The provider returned an empty model list.</EmptyState>}>
        {(all) => (
          <div className="grid gap-3">
            <div className="flex flex-wrap items-center gap-3">
              <Input type="search" aria-label="Search upstream models" placeholder={`Search ${all.length} models`} value={q} onChange={(e) => setQ(e.target.value)} className="min-w-48 flex-1" />
              <div className="flex items-center gap-2">
                <Checkbox id="discover-all" checked={allShown} disabled={shown.length === 0} onCheckedChange={(c) => toggle(shown, c === true)} />
                <Label htmlFor="discover-all" className="font-normal">Select all shown</Label>
              </div>
            </div>
            <p className="text-xs text-muted-foreground" aria-live="polite">
              {count} selected. Prices are USD per 1M tokens, prefilled when the provider lists them. Per-image, per-request or per-second prices are set by editing the model after import.
            </p>
            {shown.length === 0 ? (
              <p className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground">No models match.</p>
            ) : (
              <ul className="max-h-[50dvh] divide-y overflow-y-auto scrollbar-thin rounded-md border">
                {shown.map((m) => {
                  const r = picked[m.upstream];
                  const err = errors[m.upstream];
                  const cid = `discover-${m.upstream}`;
                  return (
                    <li key={m.upstream} className="grid gap-2 px-3 py-2.5" aria-invalid={err ? true : undefined}>
                      <div className="flex items-start gap-3">
                        <Checkbox id={cid} className="mt-0.5" checked={!!r} onCheckedChange={(c) => toggle([m], c === true)} />
                        <Label htmlFor={cid} className="grid min-w-0 gap-0.5 font-normal">
                          <span className="font-mono text-sm [overflow-wrap:anywhere]">{m.upstream}</span>
                          {(m.name && m.name !== m.upstream) || m.context_length ? (
                            <span className="text-xs text-muted-foreground">
                              {[m.name !== m.upstream && m.name, m.context_length && `${compact(m.context_length)} context`].filter(Boolean).join(", ")}
                            </span>
                          ) : null}
                        </Label>
                      </div>
                      {r && (
                        <div className="grid gap-2 sm:pl-7">
                          <div className="grid gap-2 sm:grid-cols-2">
                            <Field label="Catalog id">
                              {(fid) => (
                                <Input id={fid} value={r.id} onChange={(e) => edit(m.upstream, { id: e.target.value })} className="h-8 font-mono text-xs" spellCheck={false} autoComplete="off" aria-invalid={err ? true : undefined} />
                              )}
                            </Field>
                            <Field label="Name">
                              {(fid) => <Input id={fid} value={r.name} onChange={(e) => edit(m.upstream, { name: e.target.value })} className="h-8" />}
                            </Field>
                          </div>
                          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
                            <Field label="Kind" className="col-span-2 sm:col-span-1">
                              {(fid) => (
                                <Select value={r.kind} onValueChange={(v) => edit(m.upstream, { kind: v as ModelKind })}>
                                  <SelectTrigger id={fid} size="sm" className="w-full">
                                    <SelectValue />
                                  </SelectTrigger>
                                  <SelectContent>
                                    {Object.entries(kindLabel).map(([v, label]) => (
                                      <SelectItem key={v} value={v}>{label}</SelectItem>
                                    ))}
                                  </SelectContent>
                                </Select>
                              )}
                            </Field>
                            <Field label="Input / 1M">
                              {(fid) => <Input id={fid} type="number" min={0} step="any" value={r.input} onChange={(e) => edit(m.upstream, { input: e.target.value })} className="h-8" placeholder="0" />}
                            </Field>
                            <Field label="Output / 1M">
                              {(fid) => <Input id={fid} type="number" min={0} step="any" value={r.output} onChange={(e) => edit(m.upstream, { output: e.target.value })} className="h-8" placeholder="0" />}
                            </Field>
                          </div>
                        </div>
                      )}
                      {err && <p role="alert" className="text-sm text-destructive sm:pl-7">{err}</p>}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        )}
      </DataState>
    </FormDialog>
  );
}
