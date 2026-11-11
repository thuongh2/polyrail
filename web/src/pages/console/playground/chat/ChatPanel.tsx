import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Code, Download, History, Plus, SlidersHorizontal } from "lucide-react";
import { Field } from "@/components/console/bits";
import { KBPicker } from "@/components/console/KBPicker";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { get, kbApi, orgBase, type KBDoc, type Preset } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import type { PanelProps } from "../shared";
import { Composer } from "./Composer";
import { FormatGroup, ModelPicker, ParamsGroup, RoutingGroup, ToolsGroup } from "./Controls";
import { HistorySheet, InspectSheet, ViewCodeDialog } from "./dialogs";
import {
  buildBody,
  defaultParams,
  deleteSaved,
  gwError,
  headerObject,
  loadSaved,
  mergeToolCalls,
  paramProblems,
  parseJSON,
  parseTools,
  readSSE,
  saveChat,
  shortenData,
  uid,
  type Attachment,
  type Chunk,
  type GwError,
  type Meta,
  type Msg,
  type Params,
  type Saved,
  type ToolCall,
} from "./lib";
import { MessageItem, type MessageActions } from "./Message";

function useDesktop() {
  const q = "(min-width: 1024px)";
  const [v, setV] = useState(() => matchMedia(q).matches);
  useEffect(() => {
    const m = matchMedia(q);
    const on = () => setV(m.matches);
    m.addEventListener("change", on);
    return () => m.removeEventListener("change", on);
  }, []);
  return v;
}

/** Gateway response headers that describe how an answer was served. */
function headerMeta(h: Record<string, string>): Partial<Meta> {
  return {
    route: h["x-gw-route"],
    provider: h["x-gateway-provider"],
    cache: h["x-gw-cache"] ?? (h["x-gateway-cache-hit"] === "true" ? "response" : undefined),
    guardrail: h["x-gw-guardrail"],
    kb: h["x-gw-kb"],
    sources: h["x-gw-kb-sources"]?.split(",").map((x) => x.trim()).filter(Boolean),
    costMicros: h["x-gw-cost-micros"] != null ? Number(h["x-gw-cost-micros"]) : undefined,
  };
}

export function ChatPanel({ models, apiKey, keyField }: PanelProps) {
  const { org, wsList } = useSession();
  const orgID = org?.id ?? "";
  const desktop = useDesktop();
  const presets = useAsync(() => (orgID ? get<Preset[]>(wsList(`${orgBase(orgID)}/presets`)).catch(() => [] as Preset[]) : Promise.resolve([] as Preset[])), [orgID]);
  const kbs = useAsync(() => (orgID ? kbApi(orgID, wsList).list() : Promise.resolve([])), [orgID]);
  const [kbIDs, setKBIDs] = useState<string[]>([]);
  const kbDocs = useAsync(
    () => Promise.all(kbIDs.map((id) => kbApi(orgID).docs(id).catch(() => [] as KBDoc[]))).then((ls) => ls.flat()),
    [orgID, kbIDs.join(",")],
  );
  const docName = useCallback(
    (ref: string) => {
      const [doc, seq] = ref.split("#");
      const f = kbDocs.data?.find((d) => d.id === doc)?.filename;
      return f ? `${f} #${seq}` : ref;
    },
    [kbDocs.data],
  );

  const [modelParam, setModel] = useQueryParam("model", "");
  const chatModels = useMemo(() => models.data?.filter((m) => m.kind === "chat") ?? [], [models.data]);
  const presetList = presets.data ?? [];
  const chosen = modelParam.startsWith("@preset/")
    ? presetList.some((p) => "@preset/" + p.slug === modelParam) || presets.loading
      ? modelParam
      : chatModels[0]?.id ?? ""
    : chatModels.find((m) => m.id === modelParam)?.id ?? chatModels[0]?.id ?? "";

  const [chatId, setChatId] = useState(uid);
  const [system, setSystem] = useState("");
  const [params, setParams] = useState<Params>(defaultParams);
  const set = useCallback((patch: Partial<Params>) => setParams((p) => ({ ...p, ...patch })), []);
  const [input, setInput] = useState("");
  const [images, setImages] = useState<Attachment[]>([]);
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [busy, setBusy] = useState(false);
  const [inspectId, setInspectId] = useState("");
  const [codeOpen, setCodeOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [saved, setSaved] = useState<Saved[]>(() => loadSaved(orgID));
  const abort = useRef<AbortController | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  const problems = paramProblems(params);
  const kbHeaders: Record<string, string> = kbIDs.length ? { "X-GW-KB": kbIDs.join(",") } : {};

  // Vision: the model (or the preset's model) must declare image input.
  const visionModel = chosen.startsWith("@preset/") ? presetList.find((p) => "@preset/" + p.slug === chosen)?.model : chosen;
  const vm = chatModels.find((m) => m.id === visionModel);
  const vision = vm?.modalities?.includes("image")
    ? { ok: true, why: "" }
    : { ok: false, why: vm ? `${vm.name} does not list image input. An admin can add it under Build > Models (input modalities).` : "This preset's model is not known here, so image input is off." };

  useEffect(() => () => abort.current?.abort(), []);
  useEffect(() => {
    if (!busy) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && abort.current?.abort();
    addEventListener("keydown", onKey);
    return () => removeEventListener("keydown", onKey);
  }, [busy]);

  // Keep the newest message in view while it streams, unless the reader scrolled up.
  useEffect(() => {
    const el = scroller.current;
    if (desktop && el && stick.current) el.scrollTop = el.scrollHeight;
  }, [msgs, desktop]);
  useEffect(() => {
    if (!desktop) scroller.current?.lastElementChild?.scrollIntoView({ block: "nearest" });
  }, [msgs.length, desktop]);

  // Save after each finished turn (never the key; image data and Inspect payloads are dropped).
  useEffect(() => {
    if (busy || !msgs.length || !orgID) return;
    const title = msgs.find((m) => m.role === "user")?.content.slice(0, 80) || "Image chat";
    if (!saveChat(orgID, { id: chatId, title, at: Date.now(), model: chosen, system, params, msgs })) return;
    setSaved(loadSaved(orgID));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [busy, msgs.length, chatId]);

  async function run(history: Msg[]) {
    if (!apiKey || !chosen || problems.length) return;
    const p = params;
    const body = buildBody(chosen, system, history, p);
    const headers = { "Content-Type": "application/json", Authorization: `Bearer ${apiKey.trim()}`, ...kbHeaders };
    const schema = p.format === "json_schema" ? (parseJSON(p.schema) as { value?: unknown }).value : undefined;
    const id = uid();
    const request = { url: "/v1/chat/completions", headers: { ...headers, Authorization: "Bearer [redacted]" }, body: shortenData(body) };
    setMsgs([...history, { id, role: "assistant", content: "", meta: { format: p.format, schema, request } }]);
    stick.current = true;
    setBusy(true);
    const ctl = new AbortController();
    abort.current = ctl;
    const patch = (f: (m: Msg) => Msg) => setMsgs((xs) => xs.map((m) => (m.id === id ? f(m) : m)));
    const started = performance.now();
    // stream accumulators, also used when the stream is stopped
    let content = "";
    let reasoning = "";
    let calls: ToolCall[] = [];
    let ttft: number | undefined;
    let frame = 0;
    const flush = () => {
      frame = 0;
      patch((m) => ({ ...m, content, reasoning: reasoning || undefined, tool_calls: calls.length ? calls : undefined }));
    };
    try {
      const res = await fetch("/v1/chat/completions", { method: "POST", signal: ctl.signal, headers, body: JSON.stringify(body) });
      const h = headerObject(res.headers);
      const hm = headerMeta(h);
      if (!res.ok) {
        const e = await gwError(res);
        patch((m) => ({ ...m, error: e, meta: { ...m.meta, ...hm, total: performance.now() - started, response: { status: res.status, headers: h, body: { error: e } } } }));
        return;
      }
      if (!res.body || !h["content-type"]?.includes("event-stream")) {
        const json = await res.json();
        const choice = json.choices?.[0];
        const msg = choice?.message ?? {};
        patch((m) => ({
          ...m,
          content: msg.content ?? "",
          reasoning: msg.reasoning ?? msg.reasoning_content ?? undefined,
          tool_calls: msg.tool_calls?.length ? msg.tool_calls : undefined,
          meta: { ...m.meta, ...hm, model: json.model, usage: json.usage, finish: choice?.finish_reason, total: performance.now() - started, response: { status: res.status, headers: h, body: json } },
        }));
        return;
      }
      let model: string | undefined;
      let usage: Meta["usage"];
      let finish: string | undefined;
      let chunks = 0;
      let last: Chunk = {};
      patch((m) => ({ ...m, meta: { ...m.meta, ...hm } }));
      await readSSE(res.body, (raw) => {
        const c = raw as Chunk;
        chunks++;
        last = c;
        if (c.error) throw Object.assign(new Error(c.error.message || "The provider returned an error"), { code: c.error.code });
        model = c.model ?? model;
        usage = c.usage ?? usage;
        const ch = c.choices?.[0];
        finish = ch?.finish_reason ?? finish;
        const d = ch?.delta;
        if (!d) return;
        const piece = d.content ?? "";
        const think = d.reasoning ?? d.reasoning_content ?? "";
        if ((piece || think || d.tool_calls) && ttft === undefined) ttft = performance.now() - started;
        content += piece;
        reasoning += think;
        if (d.tool_calls) calls = mergeToolCalls(calls, d.tool_calls);
        if (!frame) frame = requestAnimationFrame(flush); // one render per frame, however fast tokens arrive
      });
      cancelAnimationFrame(frame);
      const total = performance.now() - started;
      const assembled = {
        id: (last as { id?: string }).id,
        object: "chat.completion",
        model,
        choices: [{ index: 0, message: { role: "assistant", content, ...(reasoning ? { reasoning } : {}), ...(calls.length ? { tool_calls: calls } : {}) }, finish_reason: finish }],
        usage,
      };
      patch((m) => ({
        ...m,
        content,
        reasoning: reasoning || undefined,
        tool_calls: calls.length ? calls : undefined,
        meta: { ...m.meta, model, usage, finish, ttft, total, response: { status: res.status, headers: h, body: assembled, chunks } },
      }));
    } catch (err) {
      cancelAnimationFrame(frame);
      const total = performance.now() - started;
      if (ctl.signal.aborted) patch((m) => ({ ...m, content, reasoning: reasoning || undefined, tool_calls: calls.length ? calls : undefined, stopped: true, meta: { ...m.meta, ttft, total } }));
      else {
        const e: GwError = { status: 0, message: (err as Error).message, code: (err as { code?: string }).code };
        patch((m) => ({ ...m, content, error: e, meta: { ...m.meta, total } }));
      }
    } finally {
      setBusy(false);
      abort.current = null;
    }
  }

  function send() {
    const text = input.trim();
    if (busy || (!text && !images.length) || !apiKey || !chosen || problems.length) return;
    const user: Msg = { id: uid(), role: "user", content: text, ...(images.length ? { images } : {}) };
    setInput("");
    setImages([]);
    run([...msgs, user]);
  }

  // Message actions read the latest state through a ref, so the memoized messages keep stable props.
  const live = useRef({ msgs, run });
  live.current = { msgs, run };
  const act = useMemo<MessageActions>(
    () => ({
      edit: (id, text) => {
        const { msgs, run } = live.current;
        const i = msgs.findIndex((m) => m.id === id);
        if (i >= 0) run([...msgs.slice(0, i), { ...msgs[i], content: text.trim() }]);
      },
      regenerate: () => {
        const { msgs, run } = live.current;
        let i = msgs.length;
        while (i > 0 && msgs[i - 1].role === "assistant") i--;
        run(msgs.slice(0, i));
      },
      retry: (id) => {
        const { msgs, run } = live.current;
        const i = msgs.findIndex((m) => m.id === id);
        if (i >= 0) run(msgs.slice(0, i));
      },
      inspect: (id) => setInspectId(id),
      toolResults: (_id, results) => {
        const { msgs, run } = live.current;
        run([...msgs, ...results.map((r): Msg => ({ id: uid(), role: "tool", tool_call_id: r.call.id, content: r.content }))]);
      },
      runMcp: async (call) => {
        try {
          const res = await fetch("/v1/mcp/tool/execute", {
            method: "POST",
            headers: { "Content-Type": "application/json", Authorization: `Bearer ${keyRef.current.trim()}` },
            body: JSON.stringify(call),
          });
          if (!res.ok) throw new Error((await gwError(res)).message);
          const out = await res.json();
          return typeof out.content === "string" ? out.content : JSON.stringify(out.content ?? out);
        } catch (e) {
          toast(`The gateway could not run ${call.function.name}: ${(e as Error).message}`, { error: true });
          return "";
        }
      },
    }),
    [],
  );
  const keyRef = useRef(apiKey);
  keyRef.current = apiKey;

  const answered = msgs.filter((m) => m.role === "tool").map((m) => m.tool_call_id).join("\n");
  const userTools = (parseTools(params.tools).tools ?? []).map((t) => t.function.name).join("\n");
  const lastAssistant = msgs.length && msgs[msgs.length - 1].role === "assistant" ? msgs[msgs.length - 1].id : "";

  function newChat() {
    abort.current?.abort();
    setMsgs([]);
    setChatId(uid());
  }

  function exportChat() {
    const data = { exported_at: new Date().toISOString(), model: chosen, system, params, request: buildBody(chosen, system, msgs, params), messages: msgs };
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `playground-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.json`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }

  const draft: Msg[] = input.trim() || images.length ? [...msgs, { id: "draft", role: "user", content: input.trim(), images }] : msgs;
  const disabledWhy = !apiKey ? (desktop ? "Paste an API key on the left." : "Open Settings and paste an API key.") : !chosen ? "Pick a model." : problems.length ? `Fix the settings: ${problems[0]}` : "";

  const controls: ReactNode = (
    <div className="grid content-start gap-4">
      <ModelPicker models={models} presets={presetList} value={chosen} onChange={setModel} />
      {keyField}
      <Field id="pg-system" label="System prompt">
        <Textarea id="pg-system" rows={4} value={system} onChange={(e) => setSystem(e.target.value)} placeholder="You are a helpful assistant." />
      </Field>
      <div className="grid gap-2">
        <RoutingGroup p={params} set={set} models={chatModels} primary={chosen} />
        <ParamsGroup p={params} set={set} problems={problems} />
        <FormatGroup p={params} set={set} />
        <ToolsGroup p={params} set={set} />
      </div>
      {!!kbs.data?.length && (
        <KBPicker legend="Knowledge bases" help="Optional: ground answers in up to 3 (sent as X-GW-KB). None uses the key's own setting." kbs={kbs.data} value={kbIDs} onChange={setKBIDs} />
      )}
    </div>
  );

  const inspected = msgs.find((m) => m.id === inspectId)?.meta;

  return (
    <>
      <div className="grid gap-6 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
        {desktop && <aside aria-label="Chat settings">{controls}</aside>}
        <div className="flex min-h-[28rem] min-w-0 flex-col rounded-lg border lg:h-[calc(100dvh-18rem)] lg:min-h-[30rem]">
          <div className="flex flex-wrap items-center gap-1 border-b px-2 py-1.5">
            {!desktop && (
              <Button type="button" variant="outline" size="sm" onClick={() => setSettingsOpen(true)}>
                <SlidersHorizontal aria-hidden />
                Settings
              </Button>
            )}
            <p className="ml-1 min-w-0 truncate text-sm text-muted-foreground">{chosen || "No model"}</p>
            <div className="ml-auto flex items-center gap-0.5">
              <Button type="button" variant="ghost" size="sm" onClick={() => setCodeOpen(true)} disabled={!chosen}>
                <Code aria-hidden />
                <span className="max-sm:sr-only">View code</span>
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={() => setHistoryOpen(true)}>
                <History aria-hidden />
                <span className="max-sm:sr-only">History</span>
              </Button>
              <Button type="button" variant="ghost" size="icon-sm" aria-label="Export conversation as JSON" title="Export conversation as JSON" onClick={exportChat} disabled={!msgs.length}>
                <Download aria-hidden />
              </Button>
              <Button type="button" variant="ghost" size="sm" onClick={newChat} disabled={!msgs.length}>
                <Plus aria-hidden />
                <span className="max-sm:sr-only">New chat</span>
              </Button>
            </div>
          </div>
          <div
            ref={scroller}
            className="scrollbar-thin flex-1 space-y-4 p-4 lg:overflow-y-auto"
            aria-label="Conversation"
            role="log"
            onScroll={(e) => {
              const el = e.currentTarget;
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
            }}
          >
            {msgs.length === 0 ? (
              <div className="grid h-full place-items-center py-16 text-center text-sm text-muted-foreground">
                <p className="max-w-xs">{apiKey ? "Send a message to start." : desktop ? "Paste an API key on the left, then send a message." : "Open Settings to paste an API key, then send a message."}</p>
              </div>
            ) : (
              msgs.map((m) => (
                <MessageItem
                  key={m.id}
                  m={m}
                  streaming={busy && m.id === lastAssistant}
                  busy={busy}
                  last={m.id === lastAssistant}
                  answered={answered}
                  userTools={userTools}
                  docName={docName}
                  act={act}
                />
              ))
            )}
          </div>
          <Composer
            input={input}
            setInput={setInput}
            images={images}
            setImages={setImages}
            detail={params.detail}
            setDetail={(detail) => set({ detail })}
            vision={vision}
            busy={busy}
            disabledWhy={disabledWhy}
            placeholder={chosen ? `Message ${chosen}` : "Message"}
            onSend={send}
            onStop={() => abort.current?.abort()}
          />
        </div>
      </div>
      <p className="sr-only" aria-live="polite">
        {busy ? "Generating answer" : msgs.length ? "Answer complete" : ""}
      </p>
      {!desktop && (
        <Sheet open={settingsOpen} onOpenChange={setSettingsOpen}>
          <SheetContent side="bottom" className="max-h-[85dvh] gap-0">
            <SheetHeader>
              <SheetTitle>Chat settings</SheetTitle>
              <SheetDescription>Model, key, parameters, response format and tools.</SheetDescription>
            </SheetHeader>
            <div className="scrollbar-thin overflow-y-auto px-4 pb-6">{controls}</div>
          </SheetContent>
        </Sheet>
      )}
      {codeOpen && <ViewCodeDialog open={codeOpen} onOpenChange={setCodeOpen} body={buildBody(chosen, system, draft, params)} headers={kbHeaders} />}
      <InspectSheet meta={inspected} onClose={() => setInspectId("")} />
      <HistorySheet
        open={historyOpen}
        onOpenChange={setHistoryOpen}
        saved={saved}
        current={chatId}
        onLoad={(c) => {
          abort.current?.abort();
          setChatId(c.id);
          setModel(c.model);
          setSystem(c.system);
          setParams({ ...defaultParams, ...c.params });
          setMsgs(c.msgs);
          setHistoryOpen(false);
        }}
        onDelete={(id) => {
          deleteSaved(orgID, id);
          setSaved(loadSaved(orgID));
        }}
      />
    </>
  );
}
