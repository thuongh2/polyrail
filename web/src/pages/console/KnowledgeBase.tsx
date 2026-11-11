import { useEffect, useRef, useState, type DragEvent, type FormEvent } from "react";
import { Link, useLocation } from "wouter";
import { ChevronLeft, ClipboardPaste, Link2, Search, Trash2, TriangleAlert, Upload } from "lucide-react";
import { Field, Section, TableFrame } from "@/components/console/bits";
import { ConfirmDialog, FormDialog, NoAccess } from "@/components/platform/kit";
import { DataState, EmptyState, ErrorState, LoadingRows, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { bytes, fmt, get, kbApi, kbLimits, orgBase, usd, when, type APIKey, type KB, type KBDoc, type KBHit } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import { useKBForm } from "./KnowledgeBases";

const ACCEPT = ".txt,.md,.markdown,.csv,.json,.html,.htm";
const MAX_MB = 10; // KB_MAX_DOC_MB default (docs/KB.md §1)

const statusText: Record<KBDoc["status"], { label: string; cls: string }> = {
  queued: { label: "Queued", cls: "text-muted-foreground" },
  processing: { label: "Processing", cls: "border-warning/60 text-foreground" },
  ready: { label: "Ready", cls: "text-success" },
  failed: { label: "Failed", cls: "text-danger" },
};

const pending = (d: KBDoc) => d.status === "queued" || d.status === "processing";

export default function KnowledgeBase({ id }: { id: string }) {
  const { org, can } = useSession();
  const orgID = org?.id ?? "";
  const kb = useAsync(() => (orgID ? kbApi(orgID).get(id) : Promise.resolve(undefined)), [orgID, id]);
  const docs = useAsync(() => (orgID ? kbApi(orgID).docs(id) : Promise.resolve([] as KBDoc[])), [orgID, id]);
  const [tab, setTab] = useQueryParam("tab", "documents");

  // Poll every 3 s while any document is still being processed; counts on the KB change too.
  const busy = docs.data?.some(pending) ?? false;
  useEffect(() => {
    if (!busy) return;
    const t = setTimeout(() => (docs.reload(), kb.reload()), 3000);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [busy, docs.data]);

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const back = (
    <Link href="/kbs" className="mb-3 inline-flex items-center gap-1 rounded-sm text-sm text-muted-foreground hover:text-foreground">
      <ChevronLeft className="size-4" aria-hidden /> Knowledge bases
    </Link>
  );
  if (kb.error)
    return (
      <>
        {back}
        <PageHeader title="Knowledge base" />
        <ErrorState error={kb.error} onRetry={kb.reload} />
      </>
    );
  if (!kb.data)
    return (
      <>
        {back}
        <LoadingRows rows={4} />
      </>
    );
  const k = kb.data;
  const refresh = () => (docs.reload(), kb.reload());

  return (
    <>
      {back}
      <PageHeader
        title={k.name}
        description={
          <>
            {k.description && <span className="block">{k.description}</span>}
            <span className="tabular-nums">
              {fmt(k.docs)} documents, {fmt(k.chunks)} chunks, {bytes(k.bytes)} · <span className="font-mono text-xs">{k.embedding_model}</span>
              {k.dimensions ? ` · ${k.dimensions} dims` : ""}
            </span>
          </>
        }
      />
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList aria-label="Knowledge base sections" className="mb-4 h-auto flex-wrap">
          <TabsTrigger value="documents">Documents</TabsTrigger>
          <TabsTrigger value="search">Search</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="keys">Keys</TabsTrigger>
        </TabsList>
        <TabsContent value="documents">
          <Documents org={org.id} kb={k} docs={docs} admin={can("admin")} onChange={refresh} />
        </TabsContent>
        <TabsContent value="search">
          {can("member") ? <SearchTab org={org.id} kb={k} docs={docs.data ?? []} /> : <NoAccess>Searching needs the member role or higher.</NoAccess>}
        </TabsContent>
        <TabsContent value="settings">
          <SettingsTab key={k.updated_at} org={org.id} kb={k} admin={can("admin")} onSaved={refresh} />
        </TabsContent>
        <TabsContent value="keys">
          <KeysTab org={org.id} kb={k} />
        </TabsContent>
      </Tabs>
    </>
  );
}

type DocsState = { data?: KBDoc[]; error?: Error; loading: boolean; reload: () => void };

function Documents({ org, kb, docs, admin, onChange }: { org: string; kb: KB; docs: DocsState; admin: boolean; onChange: () => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [over, setOver] = useState(false);
  const [uploading, setUploading] = useState(0);
  const [pasting, setPasting] = useState(false);
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<KBDoc | null>(null);
  const api = kbApi(org);

  async function upload(files: File[]) {
    const ok = ACCEPT.split(",");
    let added = 0;
    setUploading(files.length);
    for (const f of files) {
      const ext = f.name.slice(f.name.lastIndexOf(".")).toLowerCase();
      if (!ok.includes(ext)) {
        toast(`${f.name}: only ${ACCEPT.replaceAll(",", " ")} files are supported`, { error: true });
      } else if (f.size > MAX_MB * 1024 * 1024) {
        toast(`${f.name}: larger than ${MAX_MB} MB`, { error: true });
      } else {
        try {
          await api.upload(kb.id, f);
          added++;
        } catch (e) {
          toast(`${f.name}: ${(e as Error).message}`, { error: true });
        }
      }
      setUploading((n) => n - 1);
    }
    if (added) toast(added === 1 ? "Document queued" : `${added} documents queued`);
    onChange();
  }

  const drop = (e: DragEvent) => {
    e.preventDefault();
    setOver(false);
    if (e.dataTransfer.files.length) upload([...e.dataTransfer.files]);
  };

  return (
    <>
      {admin && (
        <div
          onDragOver={(e) => (e.preventDefault(), setOver(true))}
          onDragLeave={() => setOver(false)}
          onDrop={drop}
          className={cn("mb-4 flex flex-col items-center gap-3 rounded-lg border border-dashed px-6 py-8 text-center", over && "border-ring bg-accent")}
        >
          <Upload className="size-5 text-muted-foreground" aria-hidden />
          <p className="text-sm text-muted-foreground">
            {uploading ? `Uploading, ${uploading} left…` : `Drop files here: text, Markdown, CSV, JSON or HTML, up to ${MAX_MB} MB each.`}
          </p>
          <div className="flex flex-wrap justify-center gap-2">
            <input
              ref={input}
              type="file"
              multiple
              accept={ACCEPT}
              className="sr-only"
              tabIndex={-1}
              aria-hidden
              onChange={(e) => {
                const fs = [...(e.target.files ?? [])];
                e.target.value = "";
                if (fs.length) upload(fs);
              }}
            />
            <Button size="sm" disabled={uploading > 0} onClick={() => input.current?.click()}>
              <Upload aria-hidden /> Choose files
            </Button>
            <Button size="sm" variant="outline" onClick={() => setPasting(true)}>
              <ClipboardPaste aria-hidden /> Paste text
            </Button>
            <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
              <Link2 aria-hidden /> Add URL
            </Button>
          </div>
        </div>
      )}
      <DataState
        state={docs}
        rows={3}
        empty={<EmptyState title="No documents yet">{admin ? "Upload a file, paste text or add a URL above." : "An organization admin can add documents."}</EmptyState>}
      >
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Document</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Chunks</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                  <TableHead className="text-right">Embedding cost</TableHead>
                  <TableHead>Added</TableHead>
                  {admin && (
                    <TableHead>
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  )}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((d) => {
                  const st = statusText[d.status] ?? { label: d.status, cls: "" };
                  const stale = d.status === "ready" && d.embedding_model && d.embedding_model !== kb.embedding_model;
                  return (
                    <TableRow key={d.id}>
                      <TableCell className="max-w-72">
                        <span className="block truncate font-medium" title={d.filename}>
                          {d.filename}
                        </span>
                        <span className="block truncate text-xs text-muted-foreground" title={d.url}>
                          {d.source === "url" ? d.url : d.source === "text" ? "Pasted text" : "Upload"}
                        </span>
                      </TableCell>
                      <TableCell className="max-w-64">
                        <Badge variant="outline" className={st.cls}>
                          {st.label}
                        </Badge>
                        {d.status === "failed" && d.error && <span className="mt-1 block text-xs whitespace-normal text-danger">{d.error}</span>}
                        {stale && <span className="mt-1 block text-xs whitespace-normal text-muted-foreground">Embedded with an earlier model; not searched until re-embedded.</span>}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{d.status === "ready" ? fmt(d.chunks) : "–"}</TableCell>
                      <TableCell className="text-right tabular-nums whitespace-nowrap">{bytes(d.bytes)}</TableCell>
                      <TableCell className="text-right tabular-nums">{d.status === "ready" ? usd(d.cost_micros) : "–"}</TableCell>
                      <TableCell className="whitespace-nowrap text-muted-foreground">{when(d.created_at)}</TableCell>
                      {admin && (
                        <TableCell className="text-right">
                          <Button variant="ghost" size="icon" onClick={() => setDeleting(d)} aria-label={`Delete ${d.filename}`}>
                            <Trash2 aria-hidden />
                          </Button>
                        </TableCell>
                      )}
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
      {docs.data?.some(pending) && <p className="mt-2 text-xs text-muted-foreground" aria-live="polite">Processing documents; this list refreshes every 3 seconds.</p>}

      <PasteDialog open={pasting} onOpenChange={setPasting} onSubmit={async (f, t) => (await api.addText(kb.id, f, t), onChange())} />
      <URLDialog open={adding} onOpenChange={setAdding} onSubmit={async (u) => (await api.addURL(kb.id, u), onChange())} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete “${deleting?.filename}”?`}
        description="Its chunks are removed from the knowledge base right away. This can't be undone."
        confirmLabel="Delete document"
        success="Document deleted"
        onConfirm={async () => {
          await api.removeDoc(kb.id, deleting!.id);
          onChange();
        }}
      />
    </>
  );
}

function PasteDialog({ open, onOpenChange, onSubmit }: { open: boolean; onOpenChange: (o: boolean) => void; onSubmit: (filename: string, text: string) => Promise<unknown> }) {
  const [filename, setFilename] = useState("");
  const [text, setText] = useState("");
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => (o || (setFilename(""), setText("")), onOpenChange(o))}
      title="Paste text"
      description="Markdown headings (#, ##, ###) become sections in search results."
      submitLabel="Add document"
      success="Document queued"
      wide
      onSubmit={async () => {
        if (!filename.trim()) throw new Error("Give the document a filename, such as notes.md.");
        if (!text.trim()) throw new Error("Paste some text.");
        await onSubmit(filename.trim(), text);
        setFilename(""), setText("");
      }}
    >
      <Field id="kb-paste-name" label="Filename">
        <Input id="kb-paste-name" value={filename} onChange={(e) => setFilename(e.target.value)} placeholder="notes.md" required />
      </Field>
      <Field id="kb-paste-text" label="Text">
        <Textarea id="kb-paste-text" rows={12} value={text} onChange={(e) => setText(e.target.value)} className="font-mono text-xs" />
      </Field>
    </FormDialog>
  );
}

function URLDialog({ open, onOpenChange, onSubmit }: { open: boolean; onOpenChange: (o: boolean) => void; onSubmit: (url: string) => Promise<unknown> }) {
  const [url, setUrl] = useState("");
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => (o || setUrl(""), onOpenChange(o))}
      title="Add URL"
      description="The gateway fetches the page once (public addresses only, up to 5 MB of text, Markdown, CSV, JSON or HTML)."
      submitLabel="Add document"
      success="Document queued"
      onSubmit={async () => {
        await onSubmit(url.trim());
        setUrl("");
      }}
    >
      <Field id="kb-url" label="URL">
        <Input id="kb-url" type="url" required value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/handbook.html" className="font-mono text-sm" spellCheck={false} />
      </Field>
    </FormDialog>
  );
}

function SearchTab({ org, kb, docs }: { org: string; kb: KB; docs: KBDoc[] }) {
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [hits, setHits] = useState<KBHit[] | null>(null);
  const name = (id: string) => docs.find((d) => d.id === id)?.filename ?? id;

  async function run(e: FormEvent) {
    e.preventDefault();
    if (!query.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      setHits(await kbApi(org).search(kb.id, query.trim()));
    } catch (err) {
      setError((err as Error).message);
      setHits(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <form onSubmit={run} className="mb-4 flex flex-wrap items-end gap-2">
        <div className="grid min-w-64 flex-1 gap-1.5">
          <label htmlFor="kb-q" className="text-sm font-medium">
            Query
          </label>
          <Input id="kb-q" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="How do refunds work?" />
        </div>
        <Button type="submit" disabled={busy || !query.trim()}>
          <Search aria-hidden /> {busy ? "Searching…" : "Search"}
        </Button>
      </form>
      <p className="mb-4 text-xs text-muted-foreground">
        Returns the excerpts a chat request would receive (up to {kb.top_k}). The query is embedded and billed to the organization.
      </p>
      <section aria-live="polite" aria-label="Search results">
        {error ? (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        ) : hits === null ? null : hits.length === 0 ? (
          <EmptyState title="No matching excerpts">Try other words, or lower the minimum score in Settings.</EmptyState>
        ) : (
          <ol className="grid gap-3">
            {hits.map((h, i) => (
              <li key={`${h.doc_id}#${h.seq}`} className="rounded-lg border p-4">
                <p className="mb-2 flex flex-wrap items-baseline gap-x-2 text-sm">
                  <span className="font-mono text-xs text-muted-foreground">[{i + 1}]</span>
                  <span className="font-medium">{h.filename ?? name(h.doc_id)}</span>
                  {h.section && <span className="text-muted-foreground">§ {h.section}</span>}
                  <span className="ml-auto text-xs text-muted-foreground tabular-nums">score {h.score.toFixed(3)}</span>
                </p>
                <p className="text-sm break-words whitespace-pre-wrap">{h.text}</p>
              </li>
            ))}
          </ol>
        )}
      </section>
    </>
  );
}

function SettingsTab({ org, kb, admin, onSaved }: { org: string; kb: KB; admin: boolean; onSaved: () => void }) {
  const form = useKBForm(org, kb, true, !admin);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [reindexing, setReindexing] = useState(false);
  const [, navigate] = useLocation();
  const s = form.state;
  const reembed =
    s.model !== kb.embedding_model ||
    (s.dims.trim() === "" ? 0 : Number(s.dims)) !== (kb.dimensions ?? 0) ||
    Number(s.chunk) !== kb.chunk_chars ||
    Number(s.overlap) !== kb.overlap_chars;

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await kbApi(org).update(kb.id, form.build());
      toast(reembed ? "Saved; every document is being re-embedded" : "Settings saved");
      onSaved();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <form onSubmit={save} className="grid max-w-2xl gap-4">
        {form.fields}
        <div className="flex items-start gap-2 rounded-md border border-warning/50 bg-warning/10 p-3 text-sm" role={reembed ? "alert" : undefined}>
          <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
          <p>
            {reembed ? "You changed the model, dimensions or chunking. Saving re-embeds every document, which is billed again." : "Changing the model, dimensions or chunking re-embeds every document, which is billed again."}{" "}
            After a model change, a document is left out of search until it has been re-embedded.
          </p>
        </div>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
        {admin && (
          <div>
            <Button type="submit" disabled={busy}>
              {busy ? "Saving…" : "Save changes"}
            </Button>
          </div>
        )}
      </form>
      {admin && (
        <>
          <Section title="Re-embed documents" description="Queue every document again with the current settings, for example after a failed batch.">
            <Button variant="outline" size="sm" onClick={() => setReindexing(true)}>
              Re-embed all documents
            </Button>
          </Section>
          <Section title="Delete knowledge base" description="Removes every document and chunk. Keys that use it stop being grounded.">
            <Button variant="destructive" size="sm" onClick={() => setDeleting(true)}>
              Delete knowledge base
            </Button>
          </Section>
        </>
      )}
      <ConfirmDialog
        open={reindexing}
        onOpenChange={setReindexing}
        destructive={false}
        title="Re-embed every document?"
        description={`All ${fmt(kb.docs)} documents are queued again and their embeddings are billed again.`}
        confirmLabel="Re-embed"
        success="Documents queued for re-embedding"
        onConfirm={async () => {
          await kbApi(org).reindex(kb.id);
          onSaved();
        }}
      />
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete “${kb.name}”?`}
        description="Every document and chunk is deleted. This can't be undone."
        confirmLabel="Delete knowledge base"
        success="Knowledge base deleted"
        onConfirm={async () => {
          await kbApi(org).remove(kb.id);
          navigate("/kbs");
        }}
      />
    </>
  );
}

function KeysTab({ org, kb }: { org: string; kb: KB }) {
  const keys = useAsync(() => get<APIKey[]>(`${orgBase(org)}/keys`).then((ks) => ks.filter((k) => !k.revoked && k.kb_ids?.includes(kb.id))), [org, kb.id]);
  return (
    <>
      <p className="mb-4 text-sm text-muted-foreground">
        Chat requests with these keys are grounded in this knowledge base (up to {kbLimits.perRequest} per key). Set them on the{" "}
        <Link href="/keys" className="text-foreground underline underline-offset-4">
          API keys
        </Link>{" "}
        page, or pick knowledge bases per request with the <code className="font-mono text-xs">X-GW-KB</code> header.
      </p>
      <DataState state={keys} rows={2} empty={<EmptyState title="No keys use this knowledge base" />}>
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Key</TableHead>
                  <TableHead>Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((k) => (
                  <TableRow key={k.id}>
                    <TableCell className="font-medium">{k.name}</TableCell>
                    <TableCell className="font-mono text-xs">{k.prefix}…</TableCell>
                    <TableCell className="text-muted-foreground">{when(k.created_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
    </>
  );
}
