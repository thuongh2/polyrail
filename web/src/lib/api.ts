// Typed client for the gateway's HTTP API. Shapes mirror the Go handlers (docs/API.md).

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

export async function api<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-GW-CSRF": "1" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return null as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data?.error?.message || res.statusText);
  return data as T;
}

export const get = <T>(path: string) => api<T>("GET", path);

// ---- public ----

/**
 * USD decimal strings: input/output per 1M tokens (speech input: per 1M characters), audio_minute per
 * minute of audio, image per generated image, request per request, video_second per second of video.
 */
export type Pricing = { input: string; output: string; audio_minute?: string; image?: string; request?: string; video_second?: string };

export type ModelKind = "chat" | "embedding" | "speech" | "transcription" | "image" | "rerank" | "ocr" | "video";
export const kindLabel: Record<ModelKind, string> = {
  chat: "Chat",
  embedding: "Embedding",
  speech: "Text to speech",
  transcription: "Speech to text",
  image: "Image",
  rerank: "Rerank",
  ocr: "OCR",
  video: "Video",
};

export type ModelInfo = {
  id: string;
  object: "model";
  created: number;
  owned_by: string;
  name: string;
  description?: string;
  kind: ModelKind;
  context_length?: number;
  max_output_tokens?: number;
  modalities?: string[];
  pricing: Pricing;
  private?: boolean;
  providers?: string[];
};

export type HealthStats = {
  requests: number;
  errors: number;
  uptime: number | null; // 0..1
  avg_latency_ms: number;
  throughput_tps: number | null;
};

export type HealthStatus = "operational" | "degraded" | "down" | "no_data";

export type HourPoint = { hour: string; requests: number; errors: number; avg_latency_ms: number };

export type ProviderHealth = {
  name: string;
  type: string;
  upstream_model: string;
  weight: number;
  status: HealthStatus;
  stats_24h: HealthStats;
  hourly: HourPoint[]; // last 72 hours, oldest first
};

export type ModelDetail = { model: ModelInfo; stats_24h: HealthStats; providers: ProviderHealth[] };

export type StatusPage = {
  providers: { name: string; type: string; status: HealthStatus; models: number; stats_24h: HealthStats }[];
  updated_at: string;
};

export const publicApi = {
  models: () => get<{ data: ModelInfo[] }>("/api/public/models").then((r) => r.data),
  model: (id: string) => get<ModelDetail>(`/api/public/models/${pathID(id)}`),
  status: () => get<StatusPage>("/api/public/status"),
};

// ---- auth ----

export type Role = "viewer" | "member" | "admin" | "owner";
export type User = { id: string; email: string; name: string; operator: boolean; disabled?: boolean; created_at: string };
export type OrgRef = { id: string; name: string; role: Role };
export type AuthConfig = { signup: boolean; sso: boolean; provider_types: { platform: string[]; org: string[] } };

export const authApi = {
  config: () => get<AuthConfig>("/api/auth/config"),
  me: () => get<{ user: User; orgs: OrgRef[] }>("/api/auth/me"),
  /** Like me() but 200 with user null when signed out. */
  session: () => get<{ user: User | null; orgs: OrgRef[] }>("/api/auth/session"),
  login: (email: string, password: string) => api("POST", "/api/auth/login", { email, password }),
  signup: (email: string, password: string, name: string) => api("POST", "/api/auth/signup", { email, password, name }),
  logout: () => api("POST", "/api/auth/logout"),
  password: (current: string, next: string) => api("POST", "/api/auth/password", { current, new: next }),
};

// ---- console ----

export type Guardrails = {
  max_input_chars?: number;
  blocked_patterns?: string[];
  mask_pii?: boolean;
  mask_secrets?: boolean;
  prompt_injection?: "" | "warn" | "block";
  compression?: "" | "lossless";
};
/** Opt-in semantic cache; PATCH with model "" turns it off (then the org has no semantic_cache). */
export type SemanticCache = { model: string; threshold: number; ttl_seconds: number };
export type Org = {
  id: string;
  name: string;
  prepaid: boolean;
  credits_micros: number;
  max_tokens_cap?: number;
  guardrails: Guardrails;
  mcp_servers?: string[];
  semantic_cache?: SemanticCache;
  created_at: string;
  spent_micros?: number; // GET /api/orgs/{org} only
  balance_micros?: number;
};

export type Member = { org_id: string; user_id: string; role: Role; email: string; name: string };

export type APIKey = {
  id: string;
  org_id: string;
  name: string;
  prefix: string;
  created_by: string;
  rpm?: number;
  tpm?: number;
  monthly_budget_micros?: number;
  models?: string[];
  expires_at?: string;
  revoked: boolean;
  created_at: string;
};

/** Per-key cloud config. Secret fields are write-only: the API never returns them. */
export type AzureConfig = { endpoint: string; api_version?: string; deployments?: Record<string, string> };
export type BedrockConfig = {
  region: string;
  arn?: string;
  deployments?: Record<string, string>;
  access_key?: string;
  secret_key?: string;
  session_token?: string;
};
export type VertexConfig = {
  project_id: string;
  project_number?: string;
  region: string;
  deployments?: Record<string, string>;
  auth_credentials?: string;
};
export type ProviderKey = {
  id?: string;
  name?: string;
  value?: string;
  weight: number;
  models?: string[];
  azure?: AzureConfig;
  bedrock?: BedrockConfig;
  vertex?: VertexConfig;
  cloudflare?: { account_id: string };
};
export type Provider = {
  id: string;
  org_id?: string;
  name: string;
  type: string;
  base_url?: string;
  timeout_seconds?: number;
  keys: ProviderKey[];
  enabled: boolean;
  created_at: string;
};

/** input/output_price_micros override the model price for the cost strategy only (billing uses the model price). */
export type Target = { provider_id: string; model: string; weight: number; input_price_micros?: number; output_price_micros?: number };

export type Strategy = "weighted" | "priority" | "round-robin" | "random" | "p2c" | "least-used" | "latency" | "cost" | "cache-affinity";
// Model as written by the admin API (prices in micro-USD per 1M tokens).
export type ModelInput = {
  id: string;
  name: string;
  description?: string;
  kind: ModelKind;
  context_length?: number;
  max_output_tokens?: number;
  modalities?: string[];
  input_price_micros: number;
  output_price_micros: number;
  audio_minute_price_micros?: number; // transcription: micro-USD per audio minute
  image_price_micros?: number; // micro-USD per generated image
  request_price_micros?: number; // micro-USD per successful request, any kind
  video_second_price_micros?: number; // micro-USD per second of generated video
  targets: Target[];
  strategy?: Strategy; // "weighted" when absent
  enabled: boolean;
};
// Model as listed by GET .../models (ModelInfo + management fields).
export type ModelView = ModelInfo & { targets?: Target[]; strategy?: Strategy; enabled: boolean; owner: "platform" | "org" };

/** One upstream model from POST …/providers/{id}/discover (prices in micro-USD per 1M tokens, when listed). */
export type DiscoveredModel = {
  upstream: string;
  name?: string;
  description?: string;
  context_length?: number;
  max_output_tokens?: number;
  input_price_micros?: number;
  output_price_micros?: number;
};
/** One entry of POST …/providers/{id}/import; the model gets one target {provider, upstream}. */
export type ImportModel = {
  upstream: string;
  id: string;
  name: string;
  description?: string;
  kind: ModelKind;
  context_length?: number;
  max_output_tokens?: number;
  input_price_micros: number;
  output_price_micros: number;
};
/** POST /api/admin/catalog/sync: how many catalog providers (disabled, keyless) and models were added. */
export type CatalogSync = { providers_added: number; models_added: number };

/** POST /v1/images/generations response, as the provider returns it. */
export type ImageResult = { created?: number; data: { b64_json?: string; url?: string; revised_prompt?: string }[] };
/** POST /v1/rerank response, sorted by score. */
export type RerankResult = { id?: string; model?: string; results: { index: number; relevance_score: number; document?: unknown }[] };

export type UsageRow = {
  key: string; // day (yyyymmdd), model id, key id or "total"
  requests: number;
  errors: number;
  in_tokens: number;
  out_tokens: number;
  cost_usd: number;
  avg_latency_ms: number;
  cache_hits: number;
};
export type UsageReport = { total: UsageRow; daily: UsageRow[]; models: UsageRow[]; keys: UsageRow[] };

export type RequestLog = {
  id: string;
  org_id: string;
  key_id: string;
  model: string;
  provider?: string;
  upstream_model?: string;
  status: number;
  error?: string;
  in_tokens: number;
  out_tokens: number;
  cost_micros: number;
  latency_ms: number;
  cached?: boolean;
  stream?: boolean;
  at: string;
};

export type WebhookEvent = "request.failed" | "budget.exceeded" | "key.created" | "key.revoked";
/** Org webhook; the signing secret is returned only by create. Disabled automatically after 10 failed deliveries. */
export type Webhook = { id: string; org_id: string; url: string; events: string[]; enabled: boolean; failures: number; created_at: string };
export type WebhookTest = { ok: boolean; status: number; error?: string };

export type AuditEntry = { id: string; org_id: string; actor_id: string; actor: string; action: string; target: string; detail?: string; at: string };

export type MCPServer = { id: string; name: string; type: "http" | "sse"; url: string; tools: string[]; enabled: boolean };
export type MCPClient = { config: { id: string; name: string }; tools: { name: string; description?: string }[]; state: string };

/** Base path for org-scoped admin routes, or the platform scope ("/api/admin"). */
export const orgBase = (org: string) => `/api/orgs/${encodeURIComponent(org)}`;
export const PLATFORM = "/api/admin";

// ---- formatting ----

export function pathID(id: string) {
  return id.split("/").map(encodeURIComponent).join("/");
}

/** Formats micro-USD. Small amounts keep 4 decimals so they don't read as $0.00. */
export function usd(micros: number | undefined) {
  const v = (micros ?? 0) / 1e6;
  if (v === 0) return "$0";
  return "$" + (Math.abs(v) < 0.01 ? v.toFixed(4) : v.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }));
}

/** Price per 1M tokens from the API's decimal string. */
export function perMillion(p: string) {
  const v = Number(p);
  if (v === 0) return "Free";
  return "$" + (v < 0.1 ? v.toFixed(3) : v < 10 ? v.toFixed(2) : v.toFixed(1)).replace(/\.?0+$/, (m) => (m.startsWith(".") ? "" : m));
}

/** "$0.006 / min" from the API's USD-per-minute decimal string. */
export const perMinute = (p: string) => (Number(p) === 0 ? "Free" : perMillion(p) + " / min");

/** "$0.04 / image" style price from a USD decimal string with a unit. */
const per = (p: string, unit: string) => (Number(p) === 0 ? "Free" : unitUSD(p) + " / " + unit);

/** A small unit price from a USD decimal string, without trailing zeros ("$0.04", "$0.0015"). */
export const unitUSD = (p: string) => "$" + Number(p).toLocaleString("en-US", { maximumFractionDigits: 6 });

/** The headline price of a model: its per-unit price for media kinds, else input per 1M tokens. */
export function mainPrice(m: Pick<ModelInfo, "kind" | "pricing">) {
  const p = m.pricing;
  if (p.audio_minute) return perMinute(p.audio_minute);
  if (p.image) return per(p.image, "image");
  if (p.video_second) return per(p.video_second, "sec");
  if (p.request && (m.kind === "rerank" || m.kind === "ocr" || m.kind === "video" || Number(p.input) === 0)) return per(p.request, "request");
  return perMillion(p.input);
}

export const fmt = (n: number | undefined) => (n ?? 0).toLocaleString();

export function compact(n: number | undefined) {
  return Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(n ?? 0);
}

export const pct = (v: number | null | undefined, digits = 2) => (v == null ? "–" : (v * 100).toFixed(digits) + "%");

export const when = (t?: string) => (t ? new Date(t).toLocaleString() : "–");
