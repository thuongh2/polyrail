import { useEffect, useRef, useState, type FormEvent } from "react";
import { Eraser, Send, Square } from "lucide-react";
import { Field, ms } from "@/components/console/bits";
import { EmptyState, ErrorState, PageHeader } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { fmt, get, orgBase, type ModelView } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";

type Msg = {
  role: "user" | "assistant";
  content: string;
  provider?: string;
  latency?: number;
  usage?: { prompt_tokens: number; completion_tokens: number };
  error?: string;
  stopped?: boolean;
};

/** Reads an OpenAI-style SSE stream: calls onData for each `data:` payload until [DONE]. */
async function readSSE(body: ReadableStream<Uint8Array>, onData: (json: unknown) => void) {
  const reader = body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) return;
    buf += dec.decode(value, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (!line.startsWith("data:")) continue;
      const data = line.slice(5).trim();
      if (data === "[DONE]") {
        reader.cancel().catch(() => {});
        return;
      }
      let json: unknown;
      try {
        json = JSON.parse(data);
      } catch {
        continue; // ignore a malformed line rather than dropping the whole answer
      }
      try {
        onData(json);
      } catch (e) {
        reader.cancel().catch(() => {}); // a provider error chunk ends the answer
        throw e;
      }
    }
  }
}

type Chunk = {
  provider?: string;
  choices?: { delta?: { content?: string } }[];
  usage?: { prompt_tokens: number; completion_tokens: number };
  error?: { message?: string };
};

export default function Playground() {
  const { org } = useSession();
  const base = org ? orgBase(org.id) : "";
  const models = useAsync(() => get<ModelView[]>(`${base}/models`).then((ms) => ms.filter((m) => m.kind === "chat" && m.enabled)), [base]);
  const [model, setModel] = useQueryParam("model", "");
  const [apiKey, setApiKey] = useState(""); // memory only: never persisted
  const [system, setSystem] = useState("");
  const [input, setInput] = useState("");
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [busy, setBusy] = useState(false);
  const abort = useRef<AbortController | null>(null);
  const end = useRef<HTMLDivElement>(null);

  const chosen = model || models.data?.[0]?.id || "";
  useEffect(() => {
    end.current?.scrollIntoView({ block: "nearest" });
  }, [msgs]);
  useEffect(() => () => {
    abort.current?.abort();
  }, []);

  const patchLast = (f: (m: Msg) => Msg) => setMsgs((xs) => xs.map((m, i) => (i === xs.length - 1 ? f(m) : m)));

  async function send(e?: FormEvent) {
    e?.preventDefault();
    const text = input.trim();
    if (!text || busy || !chosen || !apiKey) return;
    const history = [...msgs.filter((m) => !m.error), { role: "user" as const, content: text }];
    setMsgs([...msgs, { role: "user", content: text }, { role: "assistant", content: "" }]);
    setInput("");
    setBusy(true);
    const ctl = new AbortController();
    abort.current = ctl;
    const started = performance.now();
    try {
      const res = await fetch("/v1/chat/completions", {
        method: "POST",
        signal: ctl.signal,
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey.trim()}` },
        body: JSON.stringify({
          model: chosen,
          stream: true,
          stream_options: { include_usage: true },
          messages: [...(system.trim() ? [{ role: "system", content: system.trim() }] : []), ...history.map(({ role, content }) => ({ role, content }))],
        }),
      });
      const provider = res.headers.get("x-gateway-provider") ?? undefined;
      if (!res.ok || !res.body) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err?.error?.message || `Request failed with status ${res.status}`);
      }
      patchLast((m) => ({ ...m, provider }));
      await readSSE(res.body, (raw) => {
        const c = raw as Chunk;
        if (c.error) throw new Error(c.error.message || "The provider returned an error");
        const delta = c.choices?.[0]?.delta?.content ?? "";
        patchLast((m) => ({ ...m, content: m.content + delta, usage: c.usage ?? m.usage, provider: m.provider ?? c.provider }));
      });
      patchLast((m) => ({ ...m, latency: performance.now() - started }));
    } catch (err) {
      if (ctl.signal.aborted) patchLast((m) => ({ ...m, stopped: true, latency: performance.now() - started }));
      else patchLast((m) => ({ ...m, error: (err as Error).message }));
    } finally {
      setBusy(false);
      abort.current = null;
    }
  }

  if (!org) return <EmptyState title="You are not in an organization yet" />;

  return (
    <>
      <PageHeader
        title="Playground"
        description="Chat with any model your organization can use. Requests go through the gateway with your API key and count toward usage."
        actions={
          <Button variant="outline" size="sm" onClick={() => setMsgs([])} disabled={busy || msgs.length === 0}>
            <Eraser aria-hidden />
            Clear chat
          </Button>
        }
      />
      <div className="grid gap-6 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
        <div className="grid content-start gap-4">
          <Field id="pg-model" label="Model">
            {models.error ? (
              <ErrorState error={models.error} onRetry={models.reload} />
            ) : !models.data ? (
              <Skeleton className="h-9 w-full" />
            ) : models.data.length === 0 ? (
              <p className="text-sm text-muted-foreground">No chat models are enabled for this organization.</p>
            ) : (
              <Select value={chosen} onValueChange={setModel}>
                <SelectTrigger id="pg-model" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {models.data.map((m) => (
                    <SelectItem key={m.id} value={m.id}>
                      {m.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          <Field id="pg-key" label="API key" help="Kept in this tab's memory only; it is gone when you leave the page.">
            <Input
              id="pg-key"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder="sk-gw-…"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              aria-describedby="pg-key-help"
            />
          </Field>
          <Field id="pg-system" label="System prompt">
            <Textarea id="pg-system" rows={5} value={system} onChange={(e) => setSystem(e.target.value)} placeholder="You are a helpful assistant." />
          </Field>
        </div>

        <div className="flex min-h-[28rem] flex-col rounded-lg border">
          <div className="flex-1 space-y-4 overflow-y-auto p-4 lg:max-h-[calc(100dvh-16rem)]" aria-label="Conversation">
            {msgs.length === 0 ? (
              <div className="grid h-full place-items-center py-16 text-center text-sm text-muted-foreground">
                <p className="max-w-xs">{apiKey ? "Send a message to start." : "Paste an API key on the left, then send a message."}</p>
              </div>
            ) : (
              msgs.map((m, i) => <Message key={i} m={m} streaming={busy && i === msgs.length - 1} />)
            )}
            <div ref={end} />
          </div>
          <form onSubmit={send} className="flex items-end gap-2 border-t p-3">
            <label htmlFor="pg-input" className="sr-only">
              Message
            </label>
            <Textarea
              id="pg-input"
              rows={2}
              className="min-h-10 flex-1 resize-none"
              placeholder={chosen ? `Message ${chosen}` : "Message"}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) send(e);
              }}
            />
            {busy ? (
              <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>
                <Square aria-hidden />
                Stop
              </Button>
            ) : (
              <Button type="submit" disabled={!input.trim() || !apiKey || !chosen}>
                <Send aria-hidden />
                Send
              </Button>
            )}
          </form>
        </div>
      </div>
      <p className="sr-only" aria-live="polite">
        {busy ? "Generating answer" : msgs.length ? "Answer complete" : ""}
      </p>
    </>
  );
}

function Message({ m, streaming }: { m: Msg; streaming: boolean }) {
  const user = m.role === "user";
  return (
    <div className={cn("flex flex-col", user ? "items-end" : "items-start")}>
      <p className="mb-1 text-xs text-muted-foreground">{user ? "You" : "Assistant"}</p>
      <div
        className={cn(
          "max-w-[85%] rounded-lg px-3 py-2 text-sm break-words whitespace-pre-wrap",
          user ? "bg-secondary text-secondary-foreground" : "border bg-background",
          m.error && "border-destructive/50",
        )}
      >
        {m.error ? (
          <span className="text-destructive">{m.error}</span>
        ) : m.content ? (
          m.content
        ) : streaming ? (
          <span className="text-muted-foreground">Waiting for the first token…</span>
        ) : (
          <span className="text-muted-foreground">No content returned.</span>
        )}
      </div>
      {!user && !streaming && !m.error && (
        <dl className="mt-1 flex flex-wrap gap-x-3 text-xs text-muted-foreground tabular-nums">
          {m.stopped && (<div className="flex gap-1"><dt>Status</dt><dd className="text-foreground">Stopped by you</dd></div>)}
          {m.provider && (
            <div className="flex gap-1">
              <dt>Provider</dt>
              <dd className="text-foreground">{m.provider}</dd>
            </div>
          )}
          {m.latency != null && (
            <div className="flex gap-1">
              <dt>Latency</dt>
              <dd className="text-foreground">{ms(m.latency)}</dd>
            </div>
          )}
          {m.usage && (
            <div className="flex gap-1">
              <dt>Tokens</dt>
              <dd className="text-foreground">
                {fmt(m.usage.prompt_tokens)} in, {fmt(m.usage.completion_tokens)} out
              </dd>
            </div>
          )}
        </dl>
      )}
    </div>
  );
}
