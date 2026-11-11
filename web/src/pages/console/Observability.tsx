// Trace exports (docs/API.md "Trace exports"): one OpenTelemetry span per request to an OTLP
// collector or Langfuse. Org scope (admins, the org's requests) or platform scope (operators).
import { useMemo, useState, type FormEvent } from "react";
import { Link } from "wouter";
import { FlaskConical, Pencil, Plus, Trash2, X } from "lucide-react";
import type { Scope } from "@/App";
import { TableFrame } from "@/components/console/bits";
import { CheckList } from "@/components/console/pickers";
import { ConfirmDialog, Field, FormError, NoAccess } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  get, orgBase, PLATFORM, traceExportsApi, when, type APIKey, type Org, type TraceExport, type TraceExportInput, type TraceExportType,
} from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

/** Auto-disable threshold (docs/API.md "Trace exports"). */
const MAX_FAILURES = 10;
const typeLabel: Record<TraceExportType, string> = { otlp: "OTLP", langfuse: "Langfuse" };

const hostOf = (u: string) => {
  try {
    return new URL(u).host;
  } catch {
    return u;
  }
};
const pct = (r: number) => `${Math.round(r * 100)}%`;

type TestResult = { ok: boolean; status?: number; error?: string };
const testText = (r: TestResult) =>
  r.ok ? `Test span accepted${r.status ? ` (HTTP ${r.status})` : ""}` : `Test failed${r.status ? ` (HTTP ${r.status})` : ""}${r.error ? `: ${r.error}` : ""}`;

export default function Observability({ scope = "org" }: { scope?: Scope }) {
  const { org, wsCan, user, wsList, wsBody, ws } = useSession();
  const allowed = scope === "platform" ? !!user?.operator : wsCan("admin");
  const base = scope === "platform" ? PLATFORM : org ? orgBase(org.id) : "";
  // The layout remounts the page on a workspace switch, so wsList is current for this page's life.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const t = useMemo(() => traceExportsApi(base, scope === "org" ? wsList : undefined), [base, scope]);
  const list = useAsync(() => (allowed && base ? t.list() : Promise.resolve([] as TraceExport[])), [t, allowed]);
  // Content needs org content logging on (org scope); keys feed the key filter.
  const orgInfo = useAsync(() => (scope === "org" && allowed && base ? get<Org>(base) : Promise.resolve(null)), [base, allowed, scope]);
  const [editing, setEditing] = useState<TraceExport | "new" | null>(null);
  const [deleting, setDeleting] = useState<TraceExport | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const title = "Observability";
  const description =
    scope === "platform"
      ? "Send one OpenTelemetry span per request of every organization to an OTLP collector or Langfuse."
      : ws && !ws.is_default
        ? `Send one OpenTelemetry span per request of the ${ws.name} workspace to an OTLP collector (Grafana Tempo, Datadog, Jaeger…) or Langfuse.`
        : "Send one OpenTelemetry span per request to an OTLP collector (Grafana Tempo, Datadog, Jaeger…) or Langfuse. Exports of the default workspace receive every workspace's requests.";
  if (scope === "org" && !org) return <EmptyState title="You are not in an organization yet" />;
  if (!allowed)
    return (
      <>
        <PageHeader title={title} description={description} />
        <NoAccess>{scope === "platform" ? "Only operators manage platform trace exports." : "Only organization admins manage trace exports."}</NoAccess>
      </>
    );

  async function run(x: TraceExport, fn: () => Promise<void>) {
    setBusy(x.id);
    try {
      await fn();
    } catch (e) {
      toast((e as Error).message, { error: true });
    } finally {
      setBusy(null);
    }
  }
  const setEnabled = (x: TraceExport, enabled: boolean) =>
    run(x, async () => {
      await t.update(x.id, { enabled });
      toast(enabled ? "Export enabled" : "Export disabled");
      list.reload();
    });
  const test = (x: TraceExport) =>
    run(x, async () => {
      const r = await t.test(x.id);
      toast(testText(r), { error: !r.ok });
    });

  const add = (
    <Button onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add export
    </Button>
  );
  const contentLogging = scope === "platform" ? true : !!orgInfo.data?.content_logging?.enabled;

  return (
    <>
      <PageHeader title={title} description={description} actions={add} />
      <DataState
        state={list}
        rows={3}
        empty={
          <EmptyState title="No trace exports yet" action={add}>
            Each request becomes one span with the model, provider, tokens, cost and latency, using the OpenTelemetry gen_ai conventions.
          </EmptyState>
        }
      >
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead className="text-right">Sample</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Failures</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((x) => {
                  const autoOff = !x.enabled && x.failures >= MAX_FAILURES;
                  return (
                    <TableRow key={x.id}>
                      <TableCell>
                        <span className="font-medium">{x.name}</span>
                        <span className="block text-xs text-muted-foreground">
                          Added {when(x.created_at)}
                          {x.key_ids?.length ? ` · ${x.key_ids.length} key${x.key_ids.length === 1 ? "" : "s"}` : ""}
                          {x.include_content ? " · with content" : ""}
                        </span>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{typeLabel[x.type] ?? x.type}</Badge>
                      </TableCell>
                      <TableCell className="max-w-56 truncate font-mono text-xs" title={x.endpoint}>{hostOf(x.endpoint)}</TableCell>
                      <TableCell className="text-right tabular-nums">{pct(x.sample_rate)}</TableCell>
                      <TableCell className="whitespace-nowrap">
                        <Badge
                          variant="outline"
                          className={!x.enabled ? (autoOff ? "text-danger" : "text-muted-foreground") : x.failures ? "border-warning/60 text-foreground" : "text-success"}
                        >
                          {!x.enabled ? (autoOff ? "Disabled after failures" : "Disabled") : x.failures ? "Failing" : "Active"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{x.failures}</TableCell>
                      <TableCell>
                        <Switch checked={x.enabled} disabled={busy === x.id} onCheckedChange={(on) => setEnabled(x, on)} aria-label={`${x.enabled ? "Disable" : "Enable"} export ${x.name}`} />
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="sm" disabled={busy === x.id} onClick={() => test(x)} aria-label={`Send a test span to ${x.name}`}>
                            <FlaskConical aria-hidden /> Test
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setEditing(x)} aria-label={`Edit export ${x.name}`}>
                            <Pencil aria-hidden />
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setDeleting(x)} aria-label={`Delete export ${x.name}`}>
                            <Trash2 aria-hidden />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
      <p className="mt-3 text-xs text-muted-foreground">
        Spans go out in batches of up to 100 at least every 2 seconds and are best effort: a restart loses buffered spans. An export is
        disabled after {MAX_FAILURES} failed batches in a row. At most 5 exports{scope === "platform" ? " on the platform" : " per organization"}.
      </p>

      {editing && (
        <ExportDialog
          key={editing === "new" ? "new" : editing.id}
          scope={scope}
          base={base}
          exp={editing === "new" ? undefined : editing}
          contentLogging={contentLogging}
          onClose={() => setEditing(null)}
          save={(body) => (editing === "new" ? t.create(scope === "org" ? { ...body, ...wsBody } : body) : t.update(editing.id, body))}
          test={(id) => t.test(id)}
          onSaved={list.reload}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "export"}?`}
        description="Spans stop going to this endpoint immediately. Its stored headers can't be recovered."
        confirmLabel="Delete export"
        success="Export deleted"
        onConfirm={async () => {
          await t.remove(deleting!.id);
          list.reload();
        }}
      />
    </>
  );
}

// ---- presets: hints only, never URLs or tokens we made up ----

type Preset = { id: string; label: string; type: TraceExportType; placeholder: string; hint: string; headers: string[] };
const presets: Preset[] = [
  {
    id: "grafana",
    label: "Grafana Cloud / Tempo",
    type: "otlp",
    placeholder: "https://<otlp-gateway-host>/otlp/v1/traces",
    hint: "Grafana Cloud: the OTLP endpoint from your stack's OpenTelemetry page, plus /v1/traces, with an Authorization: Basic header (instance ID and token). Self-hosted Tempo: its OTLP/HTTP receiver, usually port 4318, path /v1/traces.",
    headers: ["Authorization"],
  },
  {
    id: "datadog",
    label: "Datadog Agent",
    type: "otlp",
    placeholder: "http://<agent-host>:4318/v1/traces",
    hint: "Enable OTLP ingestion on the Datadog Agent (OTLP/HTTP receiver, port 4318 by default); the Agent adds your API key.",
    headers: [],
  },
  {
    id: "jaeger",
    label: "Jaeger",
    type: "otlp",
    placeholder: "http://<jaeger-host>:4318/v1/traces",
    hint: "Jaeger accepts OTLP/HTTP on port 4318 at /v1/traces.",
    headers: [],
  },
  {
    id: "langfuse",
    label: "Langfuse",
    type: "langfuse",
    placeholder: "https://cloud.langfuse.com",
    hint: "The Langfuse base URL (cloud region or your own host); spans go to <base>/api/public/otel/v1/traces. Keys are in the project's settings.",
    headers: [],
  },
];

type HeaderRow = { name: string; value: string };

function ExportDialog({
  scope, base, exp, contentLogging, onClose, save, test, onSaved,
}: {
  scope: Scope;
  base: string;
  exp?: TraceExport;
  contentLogging: boolean;
  onClose: () => void;
  save: (body: TraceExportInput) => Promise<TraceExport>;
  test: (id: string) => Promise<TestResult>;
  onSaved: () => void;
}) {
  const [preset, setPreset] = useState<Preset | null>(null);
  const [name, setName] = useState(exp?.name ?? "");
  const [type, setType] = useState<TraceExportType>(exp?.type ?? "otlp");
  const [endpoint, setEndpoint] = useState(exp?.endpoint ?? "");
  // Header values are write-only: an existing export keeps its headers unless they are replaced.
  const [replaceHeaders, setReplaceHeaders] = useState(!exp);
  const [headers, setHeaders] = useState<HeaderRow[]>([]);
  const [replaceKeys, setReplaceKeys] = useState(!exp || exp.type !== "langfuse");
  const [pk, setPk] = useState("");
  const [sk, setSk] = useState("");
  const [rate, setRate] = useState(Math.round((exp?.sample_rate ?? 1) * 100));
  const [keyIDs, setKeyIDs] = useState<string[]>(exp?.key_ids ?? []);
  const [content, setContent] = useState(exp?.include_content ?? false);
  const [enabled, setEnabled] = useState(exp?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [result, setResult] = useState<TestResult | null>(null);
  const { wsList } = useSession();
  const keys = useAsync(() => (scope === "org" ? get<APIKey[]>(wsList(`${base}/keys`)) : Promise.resolve([] as APIKey[])), [base, scope]);
  const keyOpts = keys.data?.filter((k) => !k.revoked).map((k) => ({ value: k.id, label: k.name, hint: k.prefix + "…" }));

  const applyPreset = (p: Preset) => {
    setPreset(p);
    setType(p.type);
    if (!name.trim()) setName(p.label.split(" /")[0]);
    if (p.type === "otlp") {
      setReplaceHeaders(true);
      setHeaders(p.headers.map((h) => ({ name: h, value: "" })));
    }
    setErr("");
  };
  const otlpHint = preset?.type === type ? preset.hint : type === "otlp" ? "The full OTLP/HTTP traces URL, ending in /v1/traces." : presets[3].hint;
  const placeholder = preset?.type === type ? preset.placeholder : type === "otlp" ? "https://otlp.example.com/v1/traces" : presets[3].placeholder;
  const typeChanged = !!exp && exp.type !== type;

  async function submit(e: FormEvent) {
    e.preventDefault();
    const body: TraceExportInput = {
      name: name.trim(),
      type,
      endpoint: endpoint.trim(),
      sample_rate: rate / 100,
      include_content: content && contentLogging,
    };
    if (scope === "org") body.key_ids = keyIDs;
    if (exp) body.enabled = enabled;
    if (type === "otlp" && (replaceHeaders || typeChanged)) {
      const rows = headers.filter((h) => h.name.trim() || h.value);
      if (rows.some((h) => !h.name.trim() || !h.value)) return setErr("Each header needs a name and a value.");
      const names = rows.map((h) => h.name.trim().toLowerCase());
      if (new Set(names).size !== names.length) return setErr("A header is listed twice.");
      body.headers = Object.fromEntries(rows.map((h) => [h.name.trim(), h.value]));
    }
    if (type === "langfuse" && (replaceKeys || typeChanged)) {
      if (!pk.trim() || !sk.trim()) return setErr("Enter the Langfuse public and secret keys.");
      body.langfuse = { public_key: pk.trim(), secret_key: sk.trim() };
      if (typeChanged) body.headers = {}; // drop the OTLP headers; the keys become the Authorization header
    }
    setBusy(true);
    setErr("");
    try {
      await save(body);
      toast(exp ? "Export saved" : "Export added");
      onSaved();
      onClose();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function runTest() {
    if (!exp) return;
    setBusy(true);
    setResult(null);
    try {
      setResult(await test(exp.id));
    } catch (e) {
      setResult({ ok: false, error: (e as Error).message });
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto scrollbar-thin sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{exp ? `Edit ${exp.name}` : "Add trace export"}</DialogTitle>
          <DialogDescription>Header values and Langfuse keys are encrypted and never shown again.</DialogDescription>
        </DialogHeader>
        <form id="trace-export" onSubmit={submit} className="grid gap-4">
          {!exp && (
            <fieldset className="grid gap-1.5">
              <legend className="mb-1.5 text-sm font-medium">Start from</legend>
              <div className="flex flex-wrap gap-1.5">
                {presets.map((p) => (
                  <Button key={p.id} type="button" size="sm" variant={preset?.id === p.id ? "secondary" : "outline"} aria-pressed={preset?.id === p.id} onClick={() => applyPreset(p)}>
                    {p.label}
                  </Button>
                ))}
              </div>
            </fieldset>
          )}
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_10rem]">
            <Field label="Name">
              {(id) => <Input id={id} required maxLength={100} value={name} onChange={(e) => setName(e.target.value)} placeholder="Tempo production" autoComplete="off" />}
            </Field>
            <div className="grid content-start gap-1.5">
              <Label htmlFor="te-type">Type</Label>
              <Select value={type} onValueChange={(v) => (setType(v as TraceExportType), setErr(""))}>
                <SelectTrigger id="te-type" className="w-full"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="otlp">OTLP/HTTP</SelectItem>
                  <SelectItem value="langfuse">Langfuse</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <Field label={type === "otlp" ? "Traces endpoint URL" : "Langfuse base URL"} hint={otlpHint}>
            {(id, hint) => (
              <Input id={id} aria-describedby={hint} required type="url" value={endpoint} onChange={(e) => setEndpoint(e.target.value)} placeholder={placeholder} spellCheck={false} className="font-mono text-xs" />
            )}
          </Field>
          <p className="-mt-2 text-xs text-muted-foreground">
            The endpoint must be HTTPS on a public host, unless the server runs with ALLOW_PRIVATE_PROVIDER_URLS (then any http or https URL, such as an agent on your network).
          </p>

          {type === "otlp" ? (
            !replaceHeaders && !typeChanged ? (
              <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm">
                <span className="min-w-0">
                  Headers:{" "}
                  {exp?.header_names?.length ? <span className="font-mono text-xs">{exp.header_names.join(", ")}</span> : <span className="text-muted-foreground">none</span>}
                  {exp?.header_names?.length ? <span className="text-muted-foreground"> (values hidden)</span> : null}
                </span>
                <Button type="button" size="sm" variant="outline" onClick={() => (setReplaceHeaders(true), setHeaders((exp?.header_names ?? []).map((n) => ({ name: n, value: "" }))))}>
                  Replace headers
                </Button>
              </div>
            ) : (
              <HeadersEditor rows={headers} onChange={setHeaders} replacing={!!exp} />
            )
          ) : !replaceKeys && !typeChanged ? (
            <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm">
              <span>Langfuse keys <span className="text-muted-foreground">saved, hidden</span></span>
              <Button type="button" size="sm" variant="outline" onClick={() => setReplaceKeys(true)}>Replace keys</Button>
            </div>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Public key">
                {(id) => <Input id={id} value={pk} onChange={(e) => setPk(e.target.value)} placeholder="pk-lf-…" autoComplete="off" spellCheck={false} className="font-mono text-xs" />}
              </Field>
              <Field label="Secret key">
                {(id) => <Input id={id} type="password" value={sk} onChange={(e) => setSk(e.target.value)} placeholder="sk-lf-…" autoComplete="new-password" spellCheck={false} className="font-mono text-xs" />}
              </Field>
            </div>
          )}

          <div className="grid gap-1.5">
            <div className="flex items-center justify-between">
              <Label htmlFor="te-rate">Sample rate</Label>
              <span className="text-sm tabular-nums" aria-hidden>{rate}%</span>
            </div>
            <input
              id="te-rate"
              type="range"
              min={0}
              max={100}
              step={1}
              value={rate}
              onChange={(e) => setRate(Number(e.target.value))}
              aria-valuetext={`${rate}%`}
              aria-describedby="te-rate-help"
              className="w-full accent-signal focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            />
            <p id="te-rate-help" className="text-xs text-muted-foreground">Share of requests exported. The choice follows the request id, so it is the same on every node.</p>
          </div>

          {scope === "org" && (
            <CheckList
              legend="Only these API keys"
              help="Leave all unchecked to export every key's requests."
              options={keyOpts}
              value={keyIDs}
              onChange={setKeyIDs}
              error={keys.error}
              loading={keys.loading}
              empty="No active API keys."
            />
          )}

          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <Label htmlFor="te-content">Include prompts and completions</Label>
              <p id="te-content-help" className="text-xs text-muted-foreground">
                {scope === "platform" ? (
                  "Added only for requests of organizations with content logging on (chat completions, each cut at 16 KB)."
                ) : contentLogging ? (
                  "Adds the request body and response to each chat span, each cut at 16 KB."
                ) : (
                  <>
                    Needs content logging, which is off for this organization. Turn it on in{" "}
                    <Link href="/settings" className="underline underline-offset-4">Settings</Link>.
                  </>
                )}
              </p>
            </div>
            <Switch id="te-content" checked={content && contentLogging} disabled={!contentLogging} onCheckedChange={setContent} aria-describedby="te-content-help" />
          </div>

          {exp && (
            <div className="flex items-center gap-3">
              <Switch id="te-enabled" checked={enabled} onCheckedChange={setEnabled} />
              <Label htmlFor="te-enabled">Enabled</Label>
            </div>
          )}

          {exp && (
            <div className="grid gap-2 border-t pt-4">
              <div className="flex items-center justify-between gap-3">
                <p className="text-xs text-muted-foreground">Sends one test span with the saved settings.</p>
                <Button type="button" size="sm" variant="outline" disabled={busy} onClick={runTest}>
                  <FlaskConical aria-hidden /> Test
                </Button>
              </div>
              {result && (
                <p role="status" className={result.ok ? "text-sm text-success" : "text-sm text-danger"}>{testText(result)}</p>
              )}
            </div>
          )}
          <FormError message={err} />
        </form>
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>Cancel</Button>
          <Button type="submit" form="trace-export" disabled={busy}>
            {busy ? "Saving…" : exp ? "Save changes" : "Add export"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function HeadersEditor({ rows, onChange, replacing }: { rows: HeaderRow[]; onChange: (r: HeaderRow[]) => void; replacing: boolean }) {
  const set = (i: number, p: Partial<HeaderRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...p } : r)));
  return (
    <fieldset className="grid gap-2">
      <legend className="mb-1.5 text-sm font-medium">Headers</legend>
      <p className="text-xs text-muted-foreground">
        Sent with every batch, such as an Authorization header or an API key.{replacing ? " Saving replaces all stored headers." : ""}
      </p>
      {rows.map((r, i) => (
        <div key={i} className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] gap-2">
          <Input aria-label={`Header ${i + 1} name`} value={r.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="Authorization" autoComplete="off" spellCheck={false} className="font-mono text-xs" />
          <Input aria-label={`Header ${i + 1} value`} type="password" value={r.value} onChange={(e) => set(i, { value: e.target.value })} placeholder="Value" autoComplete="new-password" spellCheck={false} className="font-mono text-xs" />
          <Button type="button" size="icon" variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))} aria-label={`Remove header ${r.name || i + 1}`}>
            <X aria-hidden />
          </Button>
        </div>
      ))}
      <div>
        <Button type="button" size="sm" variant="outline" disabled={rows.length >= 20} onClick={() => onChange([...rows, { name: "", value: "" }])}>
          <Plus aria-hidden /> Add header
        </Button>
      </div>
    </fieldset>
  );
}
