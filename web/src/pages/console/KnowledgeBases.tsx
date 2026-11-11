import { useState } from "react";
import { Link, useLocation } from "wouter";
import { Plus } from "lucide-react";
import { Field, TableFrame } from "@/components/console/bits";
import { FormDialog } from "@/components/platform/kit";
import { DataState, EmptyState, ErrorState, PageHeader } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { bytes, fmt, get, kbApi, kbLimits as L, orgBase, type KB, type KBInput, type ModelView } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

export const kbDescription =
  "Documents the gateway chunks, embeds and searches. Attach a knowledge base to an API key, or send X-GW-KB, and chat requests are grounded in its best-matching excerpts.";

export default function KnowledgeBases() {
  const { org, wsCan, wsList, ws, wsName } = useSession();
  const kbs = useAsync(() => (org ? kbApi(org.id, wsList).list() : Promise.resolve([] as KB[])), [org?.id]);
  const [creating, setCreating] = useState(false);
  const [, navigate] = useLocation();

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const admin = wsCan("admin");
  const add = admin && (
    <Button size="sm" onClick={() => setCreating(true)}>
      <Plus aria-hidden /> Create knowledge base
    </Button>
  );

  return (
    <>
      <PageHeader title="Knowledge bases" description={kbDescription} actions={add} />
      <DataState
        state={kbs}
        rows={3}
        empty={
          <EmptyState title="No knowledge bases yet" action={add}>
            {admin ? "Create one, then add documents to it." : "An organization admin can create one."}
          </EmptyState>
        }
      >
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  {!ws && <TableHead>Workspace</TableHead>}
                  <TableHead>Embedding model</TableHead>
                  <TableHead className="text-right">Documents</TableHead>
                  <TableHead className="text-right">Chunks</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((k) => (
                  <TableRow key={k.id}>
                    <TableCell className="max-w-72">
                      <Link href={`/kbs/${encodeURIComponent(k.id)}`} className="font-medium underline-offset-4 hover:underline">
                        {k.name}
                      </Link>
                      {k.description && <span className="block truncate text-xs text-muted-foreground">{k.description}</span>}
                    </TableCell>
                    {!ws && <TableCell className="whitespace-nowrap">{wsName(k.workspace_id)}</TableCell>}
                    <TableCell className="font-mono text-xs">
                      {k.embedding_model}
                      {k.dimensions ? <span className="text-muted-foreground"> · {k.dimensions} dims</span> : null}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{fmt(k.docs)}</TableCell>
                    <TableCell className="text-right tabular-nums">{fmt(k.chunks)}</TableCell>
                    <TableCell className="text-right tabular-nums whitespace-nowrap">{bytes(k.bytes)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
      <CreateKBDialog
        open={creating}
        onOpenChange={setCreating}
        org={org.id}
        onCreated={(kb) => navigate(`/kbs/${encodeURIComponent(kb.id)}`)}
      />
    </>
  );
}

function CreateKBDialog({ open, onOpenChange, org, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; org: string; onCreated: (kb: KB) => void }) {
  const { wsBody } = useSession();
  const form = useKBForm(org, undefined, open);
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => (o || form.reset(), onOpenChange(o))}
      title="Create knowledge base"
      description="Documents are embedded with the model you pick here. Embedding is billed to the organization like any request."
      submitLabel="Create"
      success="Knowledge base created"
      onSubmit={async () => {
        const kb = await kbApi(org).create({ ...form.build(), ...wsBody });
        form.reset();
        onCreated(kb);
      }}
    >
      {form.fields}
    </FormDialog>
  );
}

type FormState = Record<"name" | "description" | "model" | "dims" | "chunk" | "overlap" | "topk" | "minscore" | "maxctx", string>;

const fromKB = (kb?: KB): FormState => ({
  name: kb?.name ?? "",
  description: kb?.description ?? "",
  model: kb?.embedding_model ?? "",
  dims: kb?.dimensions ? String(kb.dimensions) : "",
  chunk: String(kb?.chunk_chars ?? L.chunk_chars.def),
  overlap: String(kb?.overlap_chars ?? L.overlap_chars.def),
  topk: String(kb?.top_k ?? L.top_k.def),
  minscore: String(kb?.min_score ?? L.min_score.def),
  maxctx: String(kb?.max_context_tokens ?? L.max_context_tokens.def),
});

/** Dimensions default for OpenAI text-embedding-3 models (they support shortening). */
const defaultDims = (model: string) => (model.includes("text-embedding-3") ? "512" : "");

/** The KB settings form shared by the create dialog and the Settings tab. build() throws on invalid input. */
export function useKBForm(org: string, kb: KB | undefined, active: boolean, disabled = false) {
  const [s, setS] = useState<FormState>(() => fromKB(kb));
  const { ws } = useSession();
  // Embedding models of the KB's workspace (the current one for a new KB).
  const kbWS = kb ? (kb.workspace_id ?? "default") : ws?.id;
  const wsList = (p: string) => (kbWS ? `${p}?workspace=${encodeURIComponent(kbWS)}` : p);
  const models = useAsync(
    () =>
      active ? get<ModelView[]>(wsList(`${orgBase(org)}/models`)).then((ms) => ms.filter((m) => m.kind === "embedding" && m.enabled)) : Promise.resolve(undefined),
    [org, active],
  );
  const set = (k: keyof FormState) => (v: string) => setS((p) => ({ ...p, [k]: v }));
  const pickModel = (m: string) => setS((p) => ({ ...p, model: m, dims: defaultDims(m) }));
  const list = models.data ?? [];
  // A KB's current model stays selectable even if it is no longer enabled.
  const options = kb && !list.some((m) => m.id === kb.embedding_model) ? [{ id: kb.embedding_model, name: kb.embedding_model }, ...list] : list;

  function build(): KBInput {
    const name = s.name.trim();
    if (!name) throw new Error("Give the knowledge base a name.");
    if (!s.model) throw new Error("Choose an embedding model.");
    const int = (v: string, label: string, min: number, max: number) => {
      const n = Number(v);
      if (v.trim() === "" || !Number.isInteger(n) || n < min || n > max) throw new Error(`${label} must be a whole number from ${min} to ${max}.`);
      return n;
    };
    const dims = s.dims.trim() === "" ? 0 : int(s.dims, "Dimensions", 1, 8192);
    const chunk = int(s.chunk, "Chunk size", L.chunk_chars.min, L.chunk_chars.max);
    const overlap = int(s.overlap, "Overlap", 0, Math.floor(chunk / 2));
    const topk = int(s.topk, "Excerpts per request", L.top_k.min, L.top_k.max);
    const maxctx = int(s.maxctx, "Max context tokens", L.max_context_tokens.min, L.max_context_tokens.max);
    const min = Number(s.minscore);
    if (s.minscore.trim() === "" || !(min >= 0 && min <= 1)) throw new Error("Minimum score must be between 0 and 1.");
    return {
      name,
      description: s.description.trim(),
      embedding_model: s.model,
      dimensions: dims,
      chunk_chars: chunk,
      overlap_chars: overlap,
      top_k: topk,
      min_score: min,
      max_context_tokens: maxctx,
    };
  }

  const num = (id: string, k: keyof FormState, label: string, help: string, step?: string) => (
    <Field id={id} label={label} help={help}>
      <Input id={id} type="number" inputMode={step ? "decimal" : "numeric"} step={step} value={s[k]} onChange={(e) => set(k)(e.target.value)} aria-describedby={id + "-help"} disabled={disabled} />
    </Field>
  );

  const fields = (
    <fieldset disabled={disabled} className="grid gap-4">
      <Field id="kb-name" label="Name">
        <Input id="kb-name" value={s.name} onChange={(e) => set("name")(e.target.value)} placeholder="Support handbook" required />
      </Field>
      <Field id="kb-desc" label="Description">
        <Textarea id="kb-desc" rows={2} value={s.description} onChange={(e) => set("description")(e.target.value)} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_8rem]">
        <Field id="kb-model" label="Embedding model">
          {models.error ? (
            <ErrorState error={models.error} onRetry={models.reload} />
          ) : !models.data ? (
            <Skeleton className="h-9 w-full" />
          ) : options.length === 0 ? (
            <p className="text-sm text-muted-foreground">No embedding models are enabled for this organization.</p>
          ) : (
            <Select value={s.model} onValueChange={pickModel} disabled={disabled}>
              <SelectTrigger id="kb-model" className="w-full">
                <SelectValue placeholder="Choose a model" />
              </SelectTrigger>
              <SelectContent>
                {options.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field id="kb-dims" label="Dimensions">
          <Input id="kb-dims" type="number" inputMode="numeric" min={1} value={s.dims} onChange={(e) => set("dims")(e.target.value)} placeholder="Native" />
        </Field>
      </div>
      <details className="group rounded-md border px-3 py-2">
        <summary className="cursor-pointer rounded-sm text-sm font-medium focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none">
          Advanced: chunking and retrieval
        </summary>
        <div className="mt-3 grid gap-4 pb-1 sm:grid-cols-2">
          {num("kb-chunk", "chunk", "Chunk size (characters)", `${L.chunk_chars.min} to ${L.chunk_chars.max}; default ${L.chunk_chars.def}.`)}
          {num("kb-overlap", "overlap", "Overlap (characters)", `At most half the chunk size; default ${L.overlap_chars.def}.`)}
          {num("kb-topk", "topk", "Excerpts per request", `${L.top_k.min} to ${L.top_k.max}; default ${L.top_k.def}.`)}
          {num("kb-minscore", "minscore", "Minimum score", `Cosine similarity floor, 0 to 1; default ${L.min_score.def}.`, "0.01")}
          {num("kb-maxctx", "maxctx", "Max context tokens", `${L.max_context_tokens.min} to ${L.max_context_tokens.max}; default ${L.max_context_tokens.def}.`)}
        </div>
      </details>
    </fieldset>
  );

  return { fields, build, state: s, reset: () => setS(fromKB(kb)) };
}
