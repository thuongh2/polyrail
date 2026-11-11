import { lazy, memo, Suspense, useEffect, useState, type ReactNode } from "react";
import { Code2, FileSearch, ImageOff, Pencil, RefreshCw, RotateCcw, Send, Server, Wrench } from "lucide-react";
import { ms } from "@/components/console/bits";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { fmt, usd } from "@/lib/api";
import { cn } from "@/lib/utils";
import { CopyIcon } from "./CopyIcon";
import { JsonBadge, JsonView, useJsonCheck } from "./JsonView";
import { parseJSON, type GwError, type Meta, type Msg, type ToolCall } from "./lib";

// react-markdown + remark-gfm load with the first answer, not with the Playground page.
const MarkdownView = lazy(() => import("./Markdown").then((m) => ({ default: m.MarkdownView })));

export type MessageActions = {
  edit: (id: string, text: string) => void;
  regenerate: () => void;
  retry: (id: string) => void;
  inspect: (id: string) => void;
  toolResults: (id: string, results: { call: ToolCall; content: string }[]) => void;
  runMcp: (call: ToolCall) => Promise<string>;
};

type Props = {
  m: Msg;
  streaming: boolean;
  busy: boolean;
  last: boolean;
  answered: string; // tool_call ids that already have a tool message, "\n"-joined (a string keeps memo cheap)
  userTools: string; // tool names from the Tools editor, "\n"-joined; other names were injected by the gateway (MCP)
  docName: (ref: string) => string;
  act: MessageActions;
};

function MessageView({ m, streaming, busy, last, answered, userTools, docName, act }: Props) {
  if (m.role === "user") return <UserMessage m={m} busy={busy} act={act} />;
  if (m.role === "tool") return <ToolMessage m={m} />;
  return <AssistantMessage m={m} streaming={streaming} busy={busy} last={last} answered={answered} userTools={userTools} docName={docName} act={act} />;
}
export const MessageItem = memo(MessageView);

function UserMessage({ m, busy, act }: { m: Msg; busy: boolean; act: MessageActions }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(m.content);
  return (
    <div className="group flex flex-col items-end">
      <p className="mb-1 text-xs text-muted-foreground">You</p>
      {editing ? (
        <form
          className="grid w-full max-w-[85%] gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setEditing(false);
            act.edit(m.id, draft);
          }}
        >
          <label htmlFor={`edit-${m.id}`} className="sr-only">
            Edit message
          </label>
          <Textarea
            id={`edit-${m.id}`}
            value={draft}
            autoFocus
            rows={Math.min(10, draft.split("\n").length + 1)}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setEditing(false);
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) e.currentTarget.form?.requestSubmit();
            }}
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={busy || (!draft.trim() && !m.images?.length)}>
              <Send aria-hidden />
              Save and resend
            </Button>
          </div>
        </form>
      ) : (
        <div className="max-w-[85%] rounded-lg bg-secondary px-3 py-2 text-sm break-words whitespace-pre-wrap text-secondary-foreground">
          {m.content}
          {!!m.images?.length && (
            <ul className={cn("flex flex-wrap gap-2", m.content && "mt-2")} aria-label="Attached images">
              {m.images.map((a, i) => (
                <li key={i}>
                  {a.url ? (
                    <img src={a.url} alt={a.name} referrerPolicy="no-referrer" className="size-20 rounded-md border bg-background object-cover" />
                  ) : (
                    <span className="flex size-20 flex-col items-center justify-center gap-1 rounded-md border bg-background p-1 text-center text-[0.6875rem] text-muted-foreground">
                      <ImageOff aria-hidden className="size-4" />
                      Not saved
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {!editing && (
        <div className="mt-1 flex gap-0.5 opacity-100 sm:opacity-0 sm:group-focus-within:opacity-100 sm:group-hover:opacity-100">
          <CopyIcon text={m.content} label="Copy message" />
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label="Edit and resend"
            title="Edit and resend"
            disabled={busy}
            onClick={() => {
              setDraft(m.content);
              setEditing(true);
            }}
          >
            <Pencil aria-hidden />
          </Button>
        </div>
      )}
    </div>
  );
}

function ToolMessage({ m }: { m: Msg }) {
  const pretty = parseJSON(m.content);
  return (
    <div className="flex flex-col items-end">
      <p className="mb-1 text-xs text-muted-foreground">Tool result</p>
      <div className="max-w-[85%] min-w-0 rounded-lg border border-dashed px-3 py-2">
        <p className="mb-1 font-mono text-xs text-muted-foreground">{m.tool_call_id}</p>
        <pre className="scrollbar-thin overflow-x-auto font-mono text-[0.8125rem] whitespace-pre-wrap">{pretty.ok ? JSON.stringify(pretty.value, null, 2) : m.content}</pre>
      </div>
    </div>
  );
}

function AssistantMessage({ m, streaming, busy, last, answered, userTools, docName, act }: Omit<Props, "m"> & { m: Msg }) {
  const [raw, setRaw] = useState(false);
  const json = m.meta?.format === "json_object" || m.meta?.format === "json_schema";
  const check = useJsonCheck(json && !streaming ? m.content : "", json ? m.meta?.schema : undefined);
  const sources = m.meta?.sources ?? [];
  const cite = sources.length
    ? {
        msgId: m.id,
        sources,
        label: docName,
        onCite: (k: number) => {
          const el = document.getElementById(`src-${m.id}-${k}`);
          el?.scrollIntoView({ block: "nearest" });
          el?.focus();
        },
      }
    : undefined;
  const done = new Set(answered.split("\n"));
  const pending = (m.tool_calls ?? []).filter((c) => !done.has(c.id));

  let body: ReactNode;
  if (m.error) body = <ErrorBox e={m.error} onRetry={() => act.retry(m.id)} busy={busy} />;
  else if (!m.content && !m.tool_calls?.length && !m.reasoning)
    body = <p className="text-sm text-muted-foreground">{streaming ? "Waiting for the first token…" : m.stopped ? "Stopped before any text arrived." : "No content returned."}</p>;
  else if (!m.content) body = null;
  else if (raw || (json && streaming)) body = <pre className="scrollbar-thin overflow-x-auto font-mono text-[0.8125rem] whitespace-pre-wrap break-words">{m.content}</pre>;
  else if (json && check.ok) body = <JsonView check={check} />;
  else
    body = (
      <Suspense fallback={<p className="text-sm break-words whitespace-pre-wrap">{m.content}</p>}>
        <MarkdownView text={m.content} cite={cite} />
      </Suspense>
    );

  return (
    <div className="group flex flex-col items-start" aria-busy={streaming || undefined}>
      <p className="mb-1 text-xs text-muted-foreground">Assistant</p>
      <div className="grid w-full min-w-0 gap-3">
        {m.reasoning && (
          <details className="rounded-md border px-3 py-2 text-sm" open={streaming && !m.content}>
            <summary className="cursor-pointer text-muted-foreground">Reasoning</summary>
            <p className="mt-2 whitespace-pre-wrap text-muted-foreground">{m.reasoning}</p>
          </details>
        )}
        {body}
        {!!m.tool_calls?.length && (
          <ToolCalls
            calls={m.tool_calls}
            pending={pending}
            streaming={streaming}
            busy={busy}
            userTools={new Set(userTools.split("\n"))}
            onSubmit={(r) => act.toolResults(m.id, r)}
            runMcp={act.runMcp}
          />
        )}
        {!!sources.length && !streaming && (
          <div className="text-xs">
            <p className="mb-1 font-medium">Sources</p>
            <ol className="grid gap-0.5 text-muted-foreground">
              {sources.map((s, i) => (
                <li key={s} id={`src-${m.id}-${i + 1}`} tabIndex={-1} className="rounded-sm focus:bg-accent focus:text-foreground focus:outline-none">
                  <span className="tabular-nums">[{i + 1}]</span> {docName(s)}
                </li>
              ))}
            </ol>
          </div>
        )}
      </div>
      {!streaming && !m.error && m.meta && <MetaBar m={m.meta} stopped={m.stopped} />}
      {!streaming && (
        <div className="mt-1 flex flex-wrap items-center gap-0.5">
          {m.content && <CopyIcon text={m.content} label="Copy answer" />}
          {m.content && (
            <Button type="button" variant="ghost" size="xs" aria-pressed={raw} onClick={() => setRaw(!raw)} title={raw ? "Show formatted" : "Show raw text"}>
              <Code2 aria-hidden />
              {raw ? "Formatted" : "Raw"}
            </Button>
          )}
          {json && !m.error && m.content && <JsonBadge check={check} />}
          {last && !m.error && (
            <Button type="button" variant="ghost" size="xs" onClick={act.regenerate} disabled={busy}>
              <RefreshCw aria-hidden />
              Regenerate
            </Button>
          )}
          {m.meta?.request && (
            <Button type="button" variant="ghost" size="xs" onClick={() => act.inspect(m.id)}>
              <FileSearch aria-hidden />
              Inspect
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

const errorHints: Record<string, string> = {
  policy_budget_exceeded: "A policy budget is used up. An admin can raise it under Manage > Policies.",
  policy_model_not_allowed: "A policy does not allow this model for your key. Pick another model or ask an admin.",
  key_budget_exceeded: "This API key reached its monthly budget.",
  insufficient_credits: "The organization is out of credits. Add credits under Manage > Credits.",
  invalid_api_key: "Check the API key on the left.",
  model_not_allowed: "This API key's model allowlist does not include the model.",
  rpm_exceeded: "Too many requests per minute for this key.",
  tpm_exceeded: "Too many tokens per minute for this key.",
  unsupported_parameters: "The model does not support part of this request (for example image input).",
  body_too_large: "The request is larger than the gateway accepts. Remove some images.",
};

function ErrorBox({ e, onRetry, busy }: { e: GwError; onRetry: () => void; busy: boolean }) {
  const [left, setLeft] = useState(e.retryAfter ?? 0);
  useEffect(() => {
    if (!e.retryAfter) return;
    const until = Date.now() + e.retryAfter * 1000;
    const id = setInterval(() => {
      const s = Math.max(0, Math.ceil((until - Date.now()) / 1000));
      setLeft(s);
      if (!s) clearInterval(id);
    }, 250);
    return () => clearInterval(id);
  }, [e.retryAfter]);
  const hint = (e.code && errorHints[e.code]) || (e.status === 429 ? "Rate limited. Wait and retry." : undefined);
  return (
    <div role="alert" className="grid gap-2 rounded-md border border-danger/40 px-3 py-2 text-sm">
      <p className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-danger">{e.status ? `Error ${e.status}` : "Error"}</span>
        {e.code && <code className="rounded bg-muted px-1 font-mono text-xs">{e.code}</code>}
        {e.policy && <span className="text-xs text-muted-foreground">policy: {e.policy}</span>}
      </p>
      <p className="break-words">{e.message}</p>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      <div>
        <Button type="button" variant="outline" size="sm" onClick={onRetry} disabled={busy || left > 0}>
          <RotateCcw aria-hidden />
          {left > 0 ? `Retry in ${left} s` : "Retry"}
        </Button>
      </div>
    </div>
  );
}

function ToolCalls({
  calls,
  pending,
  streaming,
  busy,
  userTools,
  onSubmit,
  runMcp,
}: {
  calls: ToolCall[];
  pending: ToolCall[];
  streaming: boolean;
  busy: boolean;
  userTools: Set<string>;
  onSubmit: (r: { call: ToolCall; content: string }[]) => void;
  runMcp: (call: ToolCall) => Promise<string>;
}) {
  const [results, setResults] = useState<Record<string, string>>({});
  const [running, setRunning] = useState<string>("");
  const ready = pending.length > 0 && pending.every((c) => results[c.id]?.trim());
  return (
    <div className="grid gap-2">
      {calls.map((c) => {
        const args = parseJSON(c.function.arguments || "{}");
        const mcp = !userTools.has(c.function.name);
        const open = pending.includes(c) && !streaming;
        return (
          <section key={c.id || c.function.name} aria-label={`Tool call ${c.function.name}`} className="min-w-0 rounded-md border">
            <header className="flex flex-wrap items-center gap-2 border-b px-3 py-1.5 text-sm">
              <Wrench aria-hidden className="size-4 text-muted-foreground" />
              <span className="font-mono font-medium">{c.function.name || "…"}</span>
              {mcp && (
                <Badge variant="outline" title="Not in your Tools list: injected by the gateway from an MCP server">
                  MCP
                </Badge>
              )}
              <span className="ml-auto font-mono text-xs text-muted-foreground">{c.id}</span>
            </header>
            <pre className="scrollbar-thin overflow-x-auto px-3 py-2 font-mono text-[0.8125rem]">
              {args.ok ? JSON.stringify(args.value, null, 2) : c.function.arguments}
            </pre>
            {open && (
              <div className="grid gap-2 border-t px-3 py-2">
                <label htmlFor={`tr-${c.id}`} className="text-xs font-medium">
                  Result of {c.function.name}
                </label>
                <Textarea
                  id={`tr-${c.id}`}
                  rows={3}
                  className="font-mono text-[0.8125rem]"
                  placeholder='{"temperature": 21, "unit": "celsius"}'
                  value={results[c.id] ?? ""}
                  onChange={(e) => setResults({ ...results, [c.id]: e.target.value })}
                />
                {mcp && (
                  <div>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={!!running || busy}
                      onClick={async () => {
                        setRunning(c.id);
                        try {
                          const out = await runMcp(c);
                          setResults((r) => ({ ...r, [c.id]: out }));
                        } finally {
                          setRunning("");
                        }
                      }}
                    >
                      <Server aria-hidden />
                      {running === c.id ? "Running…" : "Run on the gateway"}
                    </Button>
                  </div>
                )}
              </div>
            )}
          </section>
        );
      })}
      {pending.length > 0 && !streaming && (
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" size="sm" disabled={!ready || busy} onClick={() => onSubmit(pending.map((c) => ({ call: c, content: results[c.id].trim() })))}>
            <Send aria-hidden />
            Send {pending.length === 1 ? "tool result" : `${pending.length} tool results`}
          </Button>
          {!ready && <p className="text-xs text-muted-foreground">Type a result for each call; it is sent as a tool message and the model continues.</p>}
        </div>
      )}
    </div>
  );
}

function MetaBar({ m, stopped }: { m: Meta; stopped?: boolean }) {
  const u = m.usage;
  const cached = u?.prompt_tokens_details?.cached_tokens;
  const reasoning = u?.completion_tokens_details?.reasoning_tokens;
  const costMicros = u?.cost_micros ?? (u?.cost != null ? Math.round(u.cost * 1e6) : m.costMicros);
  const items: [string, ReactNode][] = [];
  if (stopped) items.push(["Status", "Stopped by you"]);
  if (m.finish === "length") items.push(["Finish", "Hit max tokens"]);
  if (m.model) items.push(["Model", m.model]);
  if (m.route) items.push(["Route", m.route]);
  else if (m.provider) items.push(["Provider", m.provider]);
  if (m.ttft != null) items.push(["First token", ms(m.ttft)]);
  if (m.total != null) items.push(["Total", ms(m.total)]);
  if (u?.prompt_tokens != null)
    items.push([
      "Tokens",
      `${fmt(u.prompt_tokens)} in${cached ? ` (${fmt(cached)} cached)` : ""}, ${fmt(u.completion_tokens ?? 0)} out${reasoning ? ` (${fmt(reasoning)} reasoning)` : ""}`,
    ]);
  if (costMicros != null) items.push(["Cost", usd(costMicros)]);
  if (m.cache) items.push(["Cache", m.cache === "semantic" ? "Semantic cache hit" : "Response cache hit"]);
  if (m.guardrail) items.push(["Guardrail", m.guardrail.replace(/_/g, " ")]);
  if (m.kb === "error") items.push(["Knowledge base", "Lookup failed; answered without it"]);
  if (m.kb === "empty") items.push(["Knowledge base", "No matching excerpts"]);
  if (!items.length) return null;
  return (
    <dl className="mt-2 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground tabular-nums" aria-label="Response details">
      {items.map(([k, v]) => (
        <div key={k} className="flex min-w-0 gap-1">
          <dt>{k}</dt>
          <dd className={cn("min-w-0 break-all text-foreground")}>{v}</dd>
        </div>
      ))}
    </dl>
  );
}
