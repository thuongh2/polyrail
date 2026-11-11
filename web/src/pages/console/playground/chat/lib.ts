// Chat playground logic without React: message types, the request body, SSE reading, gateway
// errors, a small JSON-schema checker, code snippets and local conversation storage.

export type Attachment = { url: string; name: string; size: number }; // url: data: URL or https URL
export type ToolCall = { id: string; type: "function"; function: { name: string; arguments: string } };

/** OpenAI usage plus the gateway's usage.include fields. */
export type Usage = {
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  prompt_tokens_details?: { cached_tokens?: number; cache_write_tokens?: number };
  completion_tokens_details?: { reasoning_tokens?: number };
  cost?: number; // USD
  cost_micros?: number;
};

export type GwError = { status: number; message: string; code?: string; type?: string; policy?: string; retryAfter?: number };

/** What one answer cost and how it was served; raw request/response are for the Inspect drawer. */
export type Meta = {
  model?: string;
  route?: string; // X-GW-Route
  provider?: string; // x-gateway-provider
  ttft?: number;
  total?: number;
  usage?: Usage;
  costMicros?: number; // X-GW-Cost-Micros (non-stream)
  cache?: string; // "semantic" | "response"
  guardrail?: string; // X-GW-Guardrail
  kb?: string; // X-GW-KB
  sources?: string[]; // X-GW-KB-Sources: doc#seq
  finish?: string;
  format?: Format;
  schema?: unknown;
  request?: { url: string; headers: Record<string, string>; body: unknown };
  response?: { status: number; headers: Record<string, string>; body: unknown; chunks?: number };
};

export type Msg = {
  id: string;
  role: "user" | "assistant" | "tool";
  content: string;
  images?: Attachment[];
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  reasoning?: string;
  meta?: Meta;
  error?: GwError;
  stopped?: boolean;
};

export type Format = "text" | "json_object" | "json_schema";
export type Params = {
  temperature: string;
  top_p: string;
  max_tokens: string;
  seed: string;
  stop: string; // one per line
  frequency_penalty: string;
  presence_penalty: string;
  reasoning: string; // "" = not sent
  stream: boolean;
  format: Format;
  schemaName: string;
  schema: string;
  strict: boolean;
  tools: string; // JSON array of tool definitions, "" = none
  toolChoice: string; // auto | none | required | fn:<name>
  fallbacks: string[]; // `models`
  sort: string; // "" | price | latency | throughput
  zdr: boolean;
  detail: "auto" | "low" | "high";
};

export const defaultParams: Params = {
  temperature: "",
  top_p: "",
  max_tokens: "",
  seed: "",
  stop: "",
  frequency_penalty: "",
  presence_penalty: "",
  reasoning: "",
  stream: true,
  format: "text",
  schemaName: "answer",
  schema: "",
  strict: true,
  tools: "",
  toolChoice: "auto",
  fallbacks: [],
  sort: "",
  zdr: false,
  detail: "auto",
};

export const exampleSchema = `{
  "type": "object",
  "properties": {
    "city": { "type": "string" },
    "population": { "type": "integer" },
    "landmarks": { "type": "array", "items": { "type": "string" } }
  },
  "required": ["city", "population", "landmarks"],
  "additionalProperties": false
}`;

export const exampleTools = `[
  {
    "type": "function",
    "function": {
      "name": "get_weather",
      "description": "Current weather for a city",
      "parameters": {
        "type": "object",
        "properties": {
          "city": { "type": "string" },
          "unit": { "type": "string", "enum": ["celsius", "fahrenheit"] }
        },
        "required": ["city"]
      }
    }
  }
]`;

export const uid = () => Math.random().toString(36).slice(2, 10) + Date.now().toString(36);

/** Parses JSON, returning the value or an error message. */
export function parseJSON(text: string): { ok: true; value: unknown } | { ok: false; error: string } {
  try {
    return { ok: true, value: JSON.parse(text) };
  } catch (e) {
    return { ok: false, error: (e as Error).message };
  }
}

/** The tools field: a JSON array of {type:"function", function:{name}} or an error. */
export function parseTools(text: string): { tools?: { type: string; function: { name: string } }[]; error?: string } {
  if (!text.trim()) return {};
  const r = parseJSON(text);
  if (!r.ok) return { error: r.error };
  if (!Array.isArray(r.value)) return { error: "Tools must be a JSON array." };
  for (const [i, t] of r.value.entries()) {
    if (!t || typeof t !== "object" || typeof (t as { function?: { name?: unknown } }).function?.name !== "string")
      return { error: `Tool ${i + 1} needs function.name.` };
  }
  return { tools: r.value as { type: string; function: { name: string } }[] };
}

/** Problems with the Parameters panel that would make the request invalid ("" = fine). */
export function paramProblems(p: Params): string[] {
  const out: string[] = [];
  const num = (v: string, name: string, lo: number, hi: number, int = false) => {
    if (!v.trim()) return;
    const n = Number(v);
    if (!Number.isFinite(n) || n < lo || n > hi || (int && !Number.isInteger(n))) out.push(`${name} must be ${int ? "a whole number " : ""}between ${lo} and ${hi}.`);
  };
  num(p.temperature, "Temperature", 0, 2);
  num(p.top_p, "Top P", 0, 1);
  num(p.max_tokens, "Max tokens", 1, 1_000_000, true);
  num(p.seed, "Seed", -(2 ** 53), 2 ** 53, true);
  num(p.frequency_penalty, "Frequency penalty", -2, 2);
  num(p.presence_penalty, "Presence penalty", -2, 2);
  if (p.stop.split("\n").filter((s) => s.trim()).length > 4) out.push("At most 4 stop sequences.");
  if (p.format === "json_schema") {
    const r = parseJSON(p.schema);
    if (!r.ok) out.push(`JSON schema: ${r.error}`);
    else if (!r.value || typeof r.value !== "object" || Array.isArray(r.value)) out.push("JSON schema must be an object.");
    if (!/^[a-zA-Z0-9_-]{1,64}$/.test(p.schemaName)) out.push("Schema name: letters, digits, _ and - only (up to 64).");
  }
  const t = parseTools(p.tools);
  if (t.error) out.push(`Tools: ${t.error}`);
  return out;
}

/** OpenAI wire message for one playground message. */
function wire(m: Msg, detail: Params["detail"]) {
  if (m.role === "tool") return { role: "tool", tool_call_id: m.tool_call_id, content: m.content };
  if (m.role === "assistant") return m.tool_calls?.length ? { role: "assistant", content: m.content || null, tool_calls: m.tool_calls } : { role: "assistant", content: m.content };
  const images = m.images?.filter((a) => a.url) ?? []; // saved chats keep no image data
  if (!images.length) return { role: "user", content: m.content };
  return {
    role: "user",
    content: [
      ...(m.content ? [{ type: "text", text: m.content }] : []),
      ...images.map((a) => ({ type: "image_url", image_url: { url: a.url, ...(detail !== "auto" ? { detail } : {}) } })),
    ],
  };
}

/** Messages worth sending: no failed answers, no empty stopped answers. */
export const sendable = (msgs: Msg[]) => msgs.filter((m) => !m.error && !(m.role === "assistant" && !m.content && !m.tool_calls?.length));

/** The /v1/chat/completions body for this conversation and settings. */
export function buildBody(model: string, system: string, msgs: Msg[], p: Params): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model,
    messages: [...(system.trim() ? [{ role: "system", content: system.trim() }] : []), ...sendable(msgs).map((m) => wire(m, p.detail))],
  };
  const n = (v: string) => (v.trim() ? Number(v) : undefined);
  const set = (k: string, v: unknown) => v !== undefined && (body[k] = v);
  set("temperature", n(p.temperature));
  set("top_p", n(p.top_p));
  set("max_tokens", n(p.max_tokens));
  set("seed", n(p.seed));
  set("frequency_penalty", n(p.frequency_penalty));
  set("presence_penalty", n(p.presence_penalty));
  const stop = p.stop.split("\n").filter((s) => s.trim());
  if (stop.length) body.stop = stop;
  if (p.reasoning) body.reasoning_effort = p.reasoning;
  if (p.format === "json_object") body.response_format = { type: "json_object" };
  if (p.format === "json_schema") {
    const r = parseJSON(p.schema);
    body.response_format = { type: "json_schema", json_schema: { name: p.schemaName, schema: r.ok ? r.value : {}, strict: p.strict } };
  }
  const tools = parseTools(p.tools).tools;
  if (tools?.length) {
    body.tools = tools;
    const fn = p.toolChoice.startsWith("fn:") ? p.toolChoice.slice(3) : "";
    body.tool_choice = !fn ? p.toolChoice : tools.some((t) => t.function.name === fn) ? { type: "function", function: { name: fn } } : "auto";
  }
  if (p.fallbacks.length) body.models = p.fallbacks;
  if (p.sort || p.zdr) body.provider = { ...(p.sort ? { sort: p.sort } : {}), ...(p.zdr ? { zdr: true } : {}) };
  body.usage = { include: true };
  if (p.stream) {
    body.stream = true;
    body.stream_options = { include_usage: true };
  }
  return body;
}

/** Copy of a request body with data: URLs shortened (Inspect, View code, saved chats). */
export function shortenData<T>(v: T): T {
  return JSON.parse(
    JSON.stringify(v, (_k, x) => (typeof x === "string" && x.startsWith("data:") && x.length > 120 ? `${x.slice(0, x.indexOf(",") + 1)}…(${Math.round((x.length * 3) / 4 / 1024)} KB of base64)` : x)),
  );
}

/** Response headers as a plain object (lowercase names). */
export const headerObject = (h: Headers) => Object.fromEntries([...h.entries()]);

/** Gateway error from a failed response: OpenAI error shape, Retry-After for 429. */
export async function gwError(res: Response): Promise<GwError> {
  const body = await res.json().catch(() => ({}));
  const e = (body as { error?: { message?: string; code?: string; type?: string; policy?: string } }).error ?? {};
  const ra = Number(res.headers.get("retry-after"));
  return {
    status: res.status,
    message: e.message || `Request failed with status ${res.status}`,
    code: e.code,
    type: e.type,
    policy: e.policy,
    retryAfter: res.status === 429 && ra > 0 ? ra : undefined,
  };
}

/** Reads an OpenAI-style SSE stream: calls onData for each `data:` payload until [DONE]. */
export async function readSSE(body: ReadableStream<Uint8Array>, onData: (json: unknown) => void) {
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

export type Delta = {
  content?: string | null;
  reasoning?: string | null;
  reasoning_content?: string | null;
  tool_calls?: { index: number; id?: string; type?: string; function?: { name?: string; arguments?: string } }[];
};
export type Chunk = {
  model?: string;
  provider?: string;
  choices?: { delta?: Delta; finish_reason?: string | null }[];
  usage?: Usage;
  error?: { message?: string; code?: string };
};

/** Merges streamed tool_call deltas (by index) into complete calls. */
export function mergeToolCalls(calls: ToolCall[], deltas: NonNullable<Delta["tool_calls"]>): ToolCall[] {
  const out = calls.map((c) => ({ ...c, function: { ...c.function } }));
  for (const d of deltas) {
    const c = (out[d.index] ??= { id: "", type: "function", function: { name: "", arguments: "" } });
    if (d.id) c.id = d.id;
    if (d.function?.name) c.function.name += d.function.name;
    if (d.function?.arguments) c.function.arguments += d.function.arguments;
  }
  return out;
}

// ---- JSON schema check (subset: type, enum, required, properties, additionalProperties, items) ----

const typeOf = (v: unknown) => (v === null ? "null" : Array.isArray(v) ? "array" : Number.isInteger(v) ? "integer" : typeof v);

/** Checks value against the common JSON-schema keywords; returns "path: problem" strings (max 20). */
export function checkSchema(value: unknown, schema: unknown, path = "$", out: string[] = []): string[] {
  if (out.length >= 20 || !schema || typeof schema !== "object") return out;
  const s = schema as { type?: string | string[]; enum?: unknown[]; required?: string[]; properties?: Record<string, unknown>; additionalProperties?: unknown; items?: unknown };
  if (s.type) {
    const want = Array.isArray(s.type) ? s.type : [s.type];
    const got = typeOf(value);
    if (!want.some((t) => t === got || (t === "number" && got === "integer"))) {
      out.push(`${path}: expected ${want.join(" or ")}, got ${got}`);
      return out;
    }
  }
  if (s.enum && !s.enum.some((e) => JSON.stringify(e) === JSON.stringify(value))) out.push(`${path}: not one of ${JSON.stringify(s.enum)}`);
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const obj = value as Record<string, unknown>;
    for (const k of s.required ?? []) if (!(k in obj)) out.push(`${path}: missing required "${k}"`);
    for (const [k, v] of Object.entries(obj)) {
      if (s.properties && k in s.properties) checkSchema(v, s.properties[k], `${path}.${k}`, out);
      else if (s.additionalProperties === false) out.push(`${path}: unexpected property "${k}"`);
    }
  }
  if (Array.isArray(value) && s.items) value.forEach((v, i) => checkSchema(v, s.items, `${path}[${i}]`, out));
  return out;
}

// ---- code snippets ----

/** JSON value as a Python literal (True/False/None), indented like json.dumps(indent=4). */
function py(v: unknown, ind = ""): string {
  if (v === null || v === undefined) return "None";
  if (v === true) return "True";
  if (v === false) return "False";
  if (typeof v === "number" || typeof v === "string") return JSON.stringify(v);
  const next = ind + "    ";
  if (Array.isArray(v)) return v.length ? `[\n${v.map((x) => next + py(x, next)).join(",\n")}\n${ind}]` : "[]";
  const e = Object.entries(v as object);
  return e.length ? `{\n${e.map(([k, x]) => `${next}${JSON.stringify(k)}: ${py(x, next)}`).join(",\n")}\n${ind}}` : "{}";
}

const openaiFields = new Set([
  "model", "messages", "temperature", "top_p", "max_tokens", "seed", "stop", "frequency_penalty", "presence_penalty",
  "reasoning_effort", "response_format", "tools", "tool_choice", "stream", "stream_options",
]);

export function snippets(origin: string, body: Record<string, unknown>, headers: Record<string, string>) {
  const b = shortenData(body);
  const hdr = Object.entries(headers);
  const curl = [
    `curl ${origin}/v1/chat/completions \\`,
    `  -H "Authorization: Bearer $POLYRAIL_API_KEY" \\`,
    `  -H "Content-Type: application/json" \\`,
    ...hdr.map(([k, v]) => `  -H "${k}: ${v}" \\`),
    ...(b.stream ? ["  -N \\"] : []),
    `  -d @- <<'EOF'`,
    JSON.stringify(b, null, 2),
    "EOF",
  ].join("\n");

  const std = Object.entries(b).filter(([k]) => openaiFields.has(k));
  const extra = Object.fromEntries(Object.entries(b).filter(([k]) => !openaiFields.has(k)));
  const args = [
    ...std.map(([k, v]) => `    ${k}=${py(v, "    ")},`),
    ...(Object.keys(extra).length ? [`    extra_body=${py(extra, "    ")},  # Polyrail routing and usage fields`] : []),
    ...(hdr.length ? [`    extra_headers=${py(headers, "    ")},`] : []),
  ];
  const python = [
    "import os",
    "from openai import OpenAI",
    "",
    `client = OpenAI(base_url="${origin}/v1", api_key=os.environ["POLYRAIL_API_KEY"])`,
    "",
    `${b.stream ? "stream" : "completion"} = client.chat.completions.create(`,
    ...args,
    ")",
    b.stream ? 'for chunk in stream:\n    if chunk.choices and chunk.choices[0].delta.content:\n        print(chunk.choices[0].delta.content, end="")' : "print(completion.choices[0].message)",
  ].join("\n");

  const ts = [
    'import OpenAI from "openai";',
    "",
    `const client = new OpenAI({ baseURL: "${origin}/v1", apiKey: process.env.POLYRAIL_API_KEY });`,
    "",
    "// Polyrail fields (models, provider, usage) pass through the SDK unchanged.",
    `const ${b.stream ? "stream" : "completion"} = await client.chat.completions.create(`,
    `  ${JSON.stringify(b, null, 2).replace(/\n/g, "\n  ")} as OpenAI.Chat.${b.stream ? "ChatCompletionCreateParamsStreaming" : "ChatCompletionCreateParamsNonStreaming"},`,
    ...(hdr.length ? [`  { headers: ${JSON.stringify(headers)} },`] : []),
    ");",
    b.stream ? 'for await (const chunk of stream) process.stdout.write(chunk.choices[0]?.delta?.content ?? "");' : "console.log(completion.choices[0].message);",
  ].join("\n");
  return { curl, python, ts };
}

// ---- saved conversations (localStorage; never the API key, never image data) ----

export type Saved = { id: string; title: string; at: number; model: string; system: string; params: Params; msgs: Msg[] };
const storeKey = (org: string) => `playground.chats.${org}`;
const MAX_SAVED = 20;

export function loadSaved(org: string): Saved[] {
  try {
    const v = JSON.parse(localStorage.getItem(storeKey(org)) ?? "[]");
    return Array.isArray(v) ? v : [];
  } catch {
    return [];
  }
}

/** Saves (or replaces) one conversation; images become placeholders and Inspect payloads are dropped. Returns false when storage is unavailable or full. */
export function saveChat(org: string, c: Saved): boolean {
  const slim: Saved = {
    ...c,
    msgs: c.msgs.map((m) => ({
      ...m,
      images: m.images?.map((a) => (a.url.startsWith("data:") ? { ...a, url: "" } : a)),
      meta: m.meta && { ...m.meta, request: undefined, response: undefined },
    })),
  };
  try {
    const all = [slim, ...loadSaved(org).filter((x) => x.id !== c.id)].slice(0, MAX_SAVED);
    localStorage.setItem(storeKey(org), JSON.stringify(all));
    return true;
  } catch {
    return false;
  }
}

export function deleteSaved(org: string, id: string) {
  try {
    localStorage.setItem(storeKey(org), JSON.stringify(loadSaved(org).filter((x) => x.id !== id)));
  } catch {
    /* storage unavailable: nothing to delete */
  }
}
