import { useMemo, useState, type ReactNode } from "react";
import { RefreshCw } from "lucide-react";
import { StatusBadge, TableFrame, ms } from "@/components/console/bits";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ApiError, fmt, get, orgBase, usd, when, type APIKey, type LogContent, type Org, type RequestLog } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const time = (t: string) => new Date(t).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" });

export default function Logs() {
  const { org, can } = useSession();
  const base = org ? orgBase(org.id) : "";
  const logs = useAsync(() => get<RequestLog[]>(`${base}/logs?limit=200`), [base]);
  const keys = useAsync(() => get<APIKey[]>(`${base}/keys`).catch(() => [] as APIKey[]), [base]);
  const [status, setStatus] = useQueryParam("status", "all");
  const [model, setModel] = useQueryParam("model", "all");
  const [open, setOpen] = useState<RequestLog | null>(null);
  const orgInfo = useAsync(() => (base ? get<Org>(base) : Promise.resolve(null)), [base]);
  const contentOn = !!orgInfo.data?.content_logging?.enabled;
  const showContent = contentOn && can("admin");

  const models = useMemo(() => [...new Set((logs.data ?? []).map((l) => l.model))].sort(), [logs.data]);
  const rows = useMemo(
    () => (logs.data ?? []).filter((l) => (status === "errors" ? l.status >= 400 : true) && (model === "all" || l.model === model)),
    [logs.data, status, model],
  );
  const keyName = (id: string) => keys.data?.find((k) => k.id === id)?.name ?? id;

  if (!org) return <EmptyState title="You are not in an organization yet" />;

  return (
    <>
      <PageHeader
        title="Logs"
        description={
          contentOn
            ? `The most recent 200 requests. Content logging is on: chat request and response bodies are kept for ${orgInfo.data?.content_logging?.retention_days} days and admins can open them.`
            : "The most recent 200 requests. Prompts and completions are not stored, only metadata."
        }
        actions={
          <Button variant="outline" size="sm" onClick={logs.reload} disabled={logs.loading}>
            <RefreshCw className={logs.loading ? "motion-safe:animate-spin" : undefined} aria-hidden />
            Refresh
          </Button>
        }
      />
      <div className="mb-3 flex flex-wrap gap-2">
        <Select value={status} onValueChange={setStatus}>
          <SelectTrigger size="sm" className="w-36" aria-label="Status filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="errors">Errors only</SelectItem>
          </SelectContent>
        </Select>
        <Select value={model} onValueChange={setModel}>
          <SelectTrigger size="sm" className="w-56" aria-label="Model filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All models</SelectItem>
            {models.map((m) => (
              <SelectItem key={m} value={m}>
                {m}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {(status !== "all" || model !== "all") && (
          <Button variant="ghost" size="sm" onClick={() => (setStatus("all"), setModel("all"))}>
            Clear filters
          </Button>
        )}
        {logs.data && <p className="ml-auto self-center text-sm text-muted-foreground tabular-nums">{fmt(rows.length)} of {fmt(logs.data.length)}</p>}
      </div>

      <DataState
        state={logs}
        rows={8}
        empty={<EmptyState title="No requests yet">Requests made with this organization's API keys show up here within a second or two.</EmptyState>}
      >
        {() =>
          rows.length === 0 ? (
            <EmptyState title="No requests match these filters" action={<Button variant="outline" size="sm" onClick={() => (setStatus("all"), setModel("all"))}>Clear filters</Button>} />
          ) : (
            <TableFrame>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Time</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead>Provider</TableHead>
                    <TableHead>End user</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead className="text-right">Tokens in</TableHead>
                    <TableHead className="text-right">Tokens out</TableHead>
                    <TableHead className="text-right">Cost</TableHead>
                    <TableHead className="text-right">Latency</TableHead>
                    <TableHead>Mode</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((l) => (
                    <TableRow
                      key={l.id}
                      tabIndex={0}
                      className="cursor-pointer focus-visible:bg-accent focus-visible:outline-none"
                      onClick={() => setOpen(l)}
                      onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), setOpen(l))}
                      aria-label={`Request ${l.id}, ${l.model}, status ${l.status}. Open details`}
                    >
                      <TableCell className="whitespace-nowrap text-muted-foreground tabular-nums">{time(l.at)}</TableCell>
                      <TableCell className="font-mono text-xs">{l.model}</TableCell>
                      <TableCell>{l.provider ?? <span className="text-muted-foreground">–</span>}</TableCell>
                      <TableCell className="max-w-40 truncate font-mono text-xs" title={l.end_user}>
                        {l.end_user ?? <span className="font-sans text-muted-foreground">–</span>}
                      </TableCell>
                      <TableCell>
                        <StatusBadge status={l.status} />
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{fmt(l.in_tokens)}</TableCell>
                      <TableCell className="text-right tabular-nums">{fmt(l.out_tokens)}</TableCell>
                      <TableCell className="text-right tabular-nums">{usd(l.cost_micros)}</TableCell>
                      <TableCell className="text-right tabular-nums">{ms(l.latency_ms)}</TableCell>
                      <TableCell className="space-x-1 whitespace-nowrap">
                        {l.stream && <Badge variant="secondary">stream</Badge>}
                        {l.cached && <Badge variant="secondary">cached</Badge>}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableFrame>
          )
        }
      </DataState>

      <Sheet open={!!open} onOpenChange={(o) => !o && setOpen(null)}>
        <SheetContent className="no-scrollbar w-full overflow-y-auto sm:max-w-md">
          {open && (
            <>
              <SheetHeader>
                <SheetTitle>Request details</SheetTitle>
                <SheetDescription>{contentOn ? "Content logging is on for this organization." : "Prompts and completions are not stored."}</SheetDescription>
              </SheetHeader>
              <dl className="grid grid-cols-[minmax(0,8rem)_minmax(0,1fr)] gap-x-4 gap-y-2 px-4 pb-6 text-sm">
                <Detail k="Request ID" v={<code className="break-all text-xs">{open.id}</code>} />
                <Detail k="Time" v={when(open.at)} />
                <Detail k="Status" v={<StatusBadge status={open.status} />} />
                {open.error && <Detail k="Error" v={<span className="break-words text-danger">{open.error}</span>} />}
                <Detail k="Model" v={<code className="break-all text-xs">{open.model}</code>} />
                <Detail k="Provider" v={open.provider ?? "–"} />
                <Detail k="Upstream model" v={open.upstream_model ? <code className="break-all text-xs">{open.upstream_model}</code> : "–"} />
                <Detail k="API key" v={keyName(open.key_id)} />
                <Detail k="End user" v={open.end_user ? <code className="break-all text-xs">{open.end_user}</code> : "–"} />
                <Detail k="Tokens in" v={fmt(open.in_tokens)} />
                <Detail k="Tokens out" v={fmt(open.out_tokens)} />
                <Detail k="Cost" v={usd(open.cost_micros)} />
                <Detail k="Latency" v={ms(open.latency_ms)} />
                <Detail k="Streamed" v={open.stream ? "Yes" : "No"} />
                <Detail k="From cache" v={open.cached ? "Yes" : "No"} />
              </dl>
              {showContent && <ContentView key={open.id} base={base} id={open.id} />}
            </>
          )}
        </SheetContent>
      </Sheet>
    </>
  );
}

function Detail({ k, v }: { k: string; v: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{k}</dt>
      <dd className="min-w-0 tabular-nums">{v}</dd>
    </>
  );
}

/** Admin only: the stored request and response bodies of one request (each read is audited). */
function ContentView({ base, id }: { base: string; id: string }) {
  const [show, setShow] = useState(false);
  const content = useAsync(
    () => (show ? get<LogContent>(`${base}/logs/${encodeURIComponent(id)}/content`) : Promise.resolve(null)),
    [base, id, show],
  );
  const missing = content.error instanceof ApiError && content.error.status === 404;
  return (
    <section aria-labelledby="content-h" className="grid gap-3 px-4 pb-6">
      <div className="flex items-center justify-between gap-2">
        <h3 id="content-h" className="text-sm font-semibold">
          Content
        </h3>
        {!show && (
          <Button variant="outline" size="sm" onClick={() => setShow(true)}>
            View content
          </Button>
        )}
      </div>
      {!show ? (
        <p className="text-xs text-muted-foreground">Opening the stored bodies is recorded in the audit log.</p>
      ) : missing ? (
        <p className="text-sm text-muted-foreground">No content stored for this request: it was not a chat completion, logging was off at the time, or it is past retention.</p>
      ) : (
        <DataState state={content} rows={2}>
          {(c) =>
            c && (
              <>
                <Body title="Request" text={c.request} cut={c.request_truncated} />
                <Body title="Response" text={c.response} cut={c.response_truncated} />
              </>
            )
          }
        </DataState>
      )}
    </section>
  );
}

function Body({ title, text, cut }: { title: string; text: string; cut?: boolean }) {
  let pretty = text;
  try {
    pretty = JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    /* streamed responses are plain text */
  }
  return (
    <div className="grid gap-1">
      <h4 className="text-xs font-medium text-muted-foreground">
        {title}
        {cut && " (cut at 64 KB)"}
      </h4>
      <pre tabIndex={0} aria-label={`${title} body`} className="scrollbar-thin max-h-72 overflow-auto rounded-md border bg-muted/50 p-3 font-mono text-xs whitespace-pre-wrap break-words">
        {pretty || "(empty)"}
      </pre>
    </div>
  );
}
