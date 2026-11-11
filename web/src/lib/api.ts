// Typed client for the gateway's HTTP API. Shapes mirror the Go handlers (docs/API.md).

export class ApiError extends Error {
  /** code and the rest of the error object, such as login_url on sso_required. */
  constructor(public status: number, message: string, public code?: string, public detail?: Record<string, unknown>) {
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
  if (!res.ok) throw new ApiError(res.status, data?.error?.message || res.statusText, data?.error?.code, data?.error);
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
  privacy?: ProviderPrivacy; // declared by the provider's admin; absent = undeclared
};

/** What a provider's admin declares about prompt data (docs/API.md "Policies"). */
export type ProviderPrivacy = { zdr: boolean; trains_on_data: boolean; retention_days?: number; region?: string };

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
/** A workspace the caller is in, with their role there (docs/API.md "Workspaces"). The default one has id "default". */
export type Workspace = { id: string; org_id?: string; name: string; slug: string; is_default: boolean; created_at?: string; role: Role };
export type OrgRef = { id: string; name: string; role: Role; workspaces?: Workspace[] };
/** GET …/workspaces/{ws}/members; via: org_role (org owners/admins, everyone in the default), member (added) or idp (group mapping). */
export type WorkspaceMember = { user_id: string; email: string; name: string; role: Role; via: "org_role" | "member" | "idp" };
export type AuthConfig = { signup: boolean; sso: boolean; tenant_proxy?: boolean; provider_types: { platform: string[]; org: string[]; org_fixed_url?: string[] };
  /** Alert channel services this build offers per scope (smtp and generic need -tags alertsfull). */
  alert_services?: { platform: string[]; org: string[] };
};

export const authApi = {
  config: () => get<AuthConfig>("/api/auth/config"),
  me: () => get<{ user: User; orgs: OrgRef[] }>("/api/auth/me"),
  /** Like me() but 200 with user null when signed out. */
  session: () => get<{ user: User | null; orgs: OrgRef[] }>("/api/auth/session"),
  /** How an email signs in (docs/API.md "Discovery for the login page"). */
  sso: (email: string) => get<SSODiscovery>(`/api/auth/sso?email=${encodeURIComponent(email)}`),
  login: (email: string, password: string) => api("POST", "/api/auth/login", { email, password }),
  signup: (email: string, password: string, name: string) => api("POST", "/api/auth/signup", { email, password, name }),
  logout: () => api("POST", "/api/auth/logout"),
  password: (current: string, next: string) => api("POST", "/api/auth/password", { current, new: next }),
};

export type SSODiscovery = { method: "saml" | "oidc" | "password"; login_url: string; password_allowed: boolean };

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
  content_logging?: ContentLogging;
  created_at: string;
  spent_micros?: number; // GET /api/orgs/{org} only
  balance_micros?: number;
};

/** Stores request/response bodies for retention_days (1–90). */
export type ContentLogging = { enabled: boolean; retention_days: number };

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
  workspace_id?: string; // absent = the default workspace
  kb_ids?: string[]; // knowledge bases that ground this key's chat requests (docs/KB.md §4)
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
  /** Outbound proxy for this provider's upstream calls; password is write-only. */
  proxy?: ProviderProxy;
  /** Connect directly even when the gateway has a global proxy. */
  no_proxy?: boolean;
  /** Proxy pool id (platform providers only; exclusive with proxy). */
  proxy_pool?: string;
  privacy?: ProviderPrivacy;
  enabled: boolean;
  created_at: string;
  workspace_id?: string; // org providers; absent = the default workspace
  /** Org providers: every workspace's models may target it (org admins set it). */
  shared_across_workspaces?: boolean;
};

export type ProviderProxy = { url: string; username?: string; password?: string; ca_cert_pem?: string };

export type ProxyStrategy = "round-robin" | "random" | "sticky" | "latency";
/** This node's view of a pool member: state from dials and health probes. */
export type ProxyHealth = { state: "up" | "down" | "set_aside" | "unknown"; latency_ms?: number; set_aside_until?: string };
export type ProxyMember = {
  id: string;
  url: string;
  username?: string;
  ca_cert_pem?: string;
  label?: string;
  enabled: boolean;
  health?: ProxyHealth;
};
export type ProxyPool = {
  id: string;
  name: string;
  strategy: ProxyStrategy;
  sticky_minutes: number;
  fail_closed: boolean;
  members: ProxyMember[];
  created_at: string;
};
export type ProxyTestResult = { member_id: string; label?: string; ok: boolean; latency_ms: number; error?: string };

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
export type UsageReport = { total: UsageRow; daily: UsageRow[]; models: UsageRow[]; keys: UsageRow[]; end_users?: UsageRow[]; workspaces?: UsageRow[] };

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
  end_user?: string; // the request's `user` field
  at: string;
};

/** GET …/logs/{id}/content (admin, when content logging was on for the request). */
export type LogContent = { log_id: string; at: string; request: string; response: string; request_truncated?: boolean; response_truncated?: boolean };

// ---- policies, presets, management tokens ----

export type BudgetInterval = "daily" | "weekly" | "monthly" | "lifetime";
export type PolicyFilter = { name?: string; pattern: string; action: "redact" | "block" };
export type Policy = {
  id: string;
  org_id: string;
  name: string;
  description?: string;
  is_org_default: boolean;
  workspace_id?: string; // absent = the default workspace
  /** Applies to every key of its workspace; its budget is one workspace-wide counter. */
  is_workspace_default?: boolean;
  member_ids?: string[];
  key_ids?: string[];
  opted_out_member_ids?: string[];
  opted_out_key_ids?: string[];
  budget?: { limit_micros: number; interval: BudgetInterval; include_byok?: boolean };
  allowed_models?: string[];
  allowed_providers?: string[];
  filters?: PolicyFilter[];
  require_zdr?: boolean;
  deny_data_collection?: boolean;
  allowed_regions?: string[];
  created_at: string;
  updated_at: string;
};
/** POST/PUT body: the budget limit goes in USD. */
export type PolicyInput = Omit<Policy, "id" | "org_id" | "created_at" | "updated_at" | "budget"> & {
  budget?: { limit_usd: number; interval: BudgetInterval; include_byok: boolean };
};
export type EffectiveBudget = {
  policy_id: string;
  policy: string;
  scope: "member" | "key" | "workspace";
  subject: string;
  limit_micros: number;
  interval: BudgetInterval;
  include_byok: boolean;
  spent_micros: number;
  resets_at?: string; // absent for lifetime
};
export type EffectivePolicy = {
  policies: { id: string; name: string; via: ("org_default" | "workspace_default" | "member" | "key")[] }[];
  allowed_models?: string[][];
  allowed_providers?: string[][];
  allowed_regions?: string[][];
  require_zdr: boolean;
  deny_data_collection: boolean;
  filters?: { policy: string; name?: string; pattern: string; action: "redact" | "block" }[];
  budgets?: EffectiveBudget[];
};

export type ProviderPrefs = {
  order?: string[];
  only?: string[];
  ignore?: string[];
  allow_fallbacks?: boolean;
  require_parameters?: boolean;
  sort?: string | { by: string; partition?: string };
  max_price?: { prompt?: number; completion?: number; request?: number; image?: number };
  data_collection?: "" | "allow" | "deny";
  zdr?: boolean;
};
export type PresetParams = { temperature?: number; top_p?: number; max_tokens?: number; system?: string; [k: string]: unknown };
export type Preset = {
  id: string;
  org_id: string;
  slug: string;
  model?: string;
  models?: string[];
  provider?: ProviderPrefs;
  params?: PresetParams;
  created_at: string;
  workspace_id?: string; // absent = the default workspace
};

export type MgmtToken = {
  id: string;
  org_id: string;
  name: string;
  prefix: string;
  role: Exclude<Role, "owner">;
  workspace_id?: string; // limited to this workspace ("default" included); absent = whole org
  created_by: string;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
  created_at: string;
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

// ---- knowledge bases (docs/KB.md §1, §5) ----

export type KB = {
  id: string;
  org_id: string;
  name: string;
  description?: string;
  embedding_model: string;
  dimensions?: number; // 0 / absent = the model's native size
  chunk_chars: number;
  overlap_chars: number;
  top_k: number;
  min_score: number;
  max_context_tokens: number;
  rev: number;
  docs: number;
  chunks: number;
  bytes: number;
  created_by_key?: string;
  created_at: string;
  updated_at: string;
  workspace_id?: string; // absent = the default workspace
};
/** Fields accepted by POST/PATCH …/kbs. */
export type KBInput = Pick<KB, "name" | "embedding_model" | "chunk_chars" | "overlap_chars" | "top_k" | "min_score" | "max_context_tokens"> & {
  description?: string;
  dimensions?: number;
  workspace_id?: string; // create only
};
export type KBDocStatus = "queued" | "processing" | "ready" | "failed";
export type KBDoc = {
  id: string;
  kb_id: string;
  org_id: string;
  filename: string;
  source: "upload" | "text" | "url";
  url?: string;
  content_type?: string;
  bytes: number;
  chars: number;
  chunks: number;
  in_tokens: number; // estimated embedded tokens (chars/4); billed cost is in the request logs
  embedding_model?: string;
  status: KBDocStatus;
  error?: string;
  attempts: number;
  attributes?: Record<string, string>;
  created_at: string;
  updated_at: string;
};
export type KBHit = { kb_id?: string; doc_id: string; filename?: string; seq: number; score: number; section?: string; text: string };

/** KB defaults and limits from docs/KB.md §1. */
export const kbLimits = {
  chunk_chars: { def: 1600, min: 200, max: 8000 },
  overlap_chars: { def: 200, min: 0 }, // max = chunk_chars / 2
  top_k: { def: 5, min: 1, max: 20 },
  min_score: { def: 0.25, min: 0, max: 1 },
  max_context_tokens: { def: 2000, min: 100, max: 16000 },
  perRequest: 3,
};

/** Thrown when a server without knowledge bases answers with something else (such as the SPA page). */
const noKB = () => new ApiError(404, "Knowledge bases are not available on this server.");

/** A list from either a bare array or a `{data}` / `{hits}` envelope. */
const listOf = <T>(r: unknown): T[] => {
  if (Array.isArray(r)) return r as T[];
  const o = (r ?? {}) as { data?: T[]; hits?: T[] };
  const l = o.data ?? o.hits;
  if (!Array.isArray(l)) throw noKB();
  return l;
};

/** Hit fields as the Go Hit struct may spell them (doc / Doc) → KBHit. */
const hitOf = (h: Record<string, unknown>): KBHit => ({
  kb_id: (h.kb_id ?? h.kb) as string | undefined,
  doc_id: (h.doc_id ?? h.doc ?? "") as string,
  filename: h.filename as string | undefined,
  seq: Number(h.seq ?? 0),
  score: Number(h.score ?? 0),
  section: h.section as string | undefined,
  text: String(h.text ?? ""),
});

/** Knowledge bases of an org; `wsList` (from useSession) limits the list to the current workspace. */
export function kbApi(org: string, wsList: (path: string) => string = (p) => p) {
  const b = `${orgBase(org)}/kbs`;
  const kb = (id: string) => `${b}/${encodeURIComponent(id)}`;
  return {
    list: () => get<unknown>(wsList(b)).then(listOf<KB>),
    get: (id: string) =>
      get<KB>(kb(id)).then((r) => {
        if (!r?.id) throw noKB();
        return r;
      }),
    create: (body: KBInput) => api<KB>("POST", b, body),
    update: (id: string, body: Partial<KBInput>) => api<KB>("PATCH", kb(id), body),
    remove: (id: string) => api("DELETE", kb(id)),
    reindex: (id: string) => api("POST", `${kb(id)}/reindex`),
    // shortcut: first page only (5,000 docs/KB max); add ?after= paging when a KB lists that many.
    docs: (id: string) => get<unknown>(`${kb(id)}/docs`).then(listOf<KBDoc>),
    addText: (id: string, filename: string, text: string) => api<KBDoc>("POST", `${kb(id)}/docs`, { filename, text }),
    addURL: (id: string, url: string) => api<KBDoc>("POST", `${kb(id)}/docs`, { url }),
    upload: async (id: string, file: File) => {
      const fd = new FormData();
      fd.append("file", file);
      const res = await fetch(`${kb(id)}/docs`, { method: "POST", headers: { "X-GW-CSRF": "1" }, body: fd });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new ApiError(res.status, data?.error?.message || res.statusText, data?.error?.code, data?.error);
      return data as KBDoc;
    },
    removeDoc: (id: string, doc: string) => api("DELETE", `${kb(id)}/docs/${encodeURIComponent(doc)}`),
    search: (id: string, query: string, top_k?: number) =>
      api<unknown>("POST", `${kb(id)}/search`, { query, top_k }).then((r) => listOf<Record<string, unknown>>(r).map(hitOf)),
  };
}

// ---- provider keys and alert channels (docs/KEYS.md) ----

export type KeyStatus = "available" | "cooldown" | "quarantined" | "disabled";
/** One stored key as GET …/providers/{id}/keys lists it; the value is never returned. */
export type ProviderKeyView = {
  id: string;
  name: string;
  /** First 4 chars + "****" + last 4; the key name for cloud types. */
  masked: string;
  weight: number;
  models?: string[] | null;
  enabled: boolean;
  status: KeyStatus;
  status_reason?: string;
  cooldown_until?: string | null;
  last_used_at?: string | null;
  stats_24h: { requests: number; errors: number; avg_latency_ms: number };
};
export type KeyAddResult = { added: number; duplicates: number; invalid: number };
export type KeyTest = { ok: boolean; status: number; latency_ms: number; error?: string };
export type KeyBulkAction = "enable" | "disable" | "delete" | "test" | "clear";
/** One per-id entry of POST …/keys/bulk; test results carry the KeyTest fields. */
export type KeyBulkResult = { id: string; ok: boolean; error?: string; status?: number; latency_ms?: number };
export type KeyStatPoint = { hour: string; requests: number; errors: number; avg_latency_ms: number; in_tokens: number; out_tokens: number };
export type KeyPatch = { enabled?: boolean; weight?: number; name?: string; models?: string[] };

/** A bare array or a {data}/{results} envelope as an array (empty when neither). */
const arrayOf = <T>(r: unknown): T[] => {
  if (Array.isArray(r)) return r as T[];
  const o = (r ?? {}) as { data?: T[]; results?: T[] };
  return o.data ?? o.results ?? [];
};

/** A list endpoint's array; anything else (such as the SPA page from a server without the route) is an error. */
const listOr = <T>(what: string) => (r: unknown): T[] => {
  if (!Array.isArray(r) && !Array.isArray((r as { data?: unknown })?.data)) throw new ApiError(404, `${what} not available on this server.`);
  return arrayOf<T>(r);
};

/** Keys of one provider; `providers` is the scope's providers collection (see providersPath). */
export function keysApi(providers: string, providerId: string) {
  const b = `${providers}/${encodeURIComponent(providerId)}/keys`;
  const k = (id: string) => `${b}/${encodeURIComponent(id)}`;
  return {
    list: () => get<unknown>(b).then(listOr<ProviderKeyView>("Key management is")),
    add: (keys: string, opts: { weight?: number; models?: string[] } = {}) => api<KeyAddResult>("POST", b, { keys, ...opts }),
    update: (id: string, body: KeyPatch) => api<ProviderKeyView>("PATCH", k(id), body),
    remove: (id: string) => api("DELETE", k(id)),
    bulk: (action: KeyBulkAction, ids: string[]) => api<unknown>("POST", `${b}/bulk`, { action, ids }).then(arrayOf<KeyBulkResult>),
    test: (id: string, model?: string) => api<KeyTest>("POST", `${k(id)}/test`, model ? { model } : {}),
    stats: (id: string, hours = 72) => get<unknown>(`${k(id)}/stats?hours=${hours}`).then(listOr<KeyStatPoint>("Key statistics are")),
  };
}

export type AlertEvent = "key.quarantined" | "key.recovered" | "provider.unavailable" | "budget.exceeded" | "request.failed";
/** An alert channel; its shoutrrr URL is write-only, the list shows the service name only. */
export type AlertChannel = {
  id: string;
  org_id?: string;
  name: string;
  service?: string;
  events: string[];
  enabled: boolean;
  failures: number;
  created_at: string;
};
export type AlertInput = { name?: string; url?: string; events?: string[]; enabled?: boolean };
export type AlertTest = { ok: boolean; error?: string };

/** Alert channels of a scope: PLATFORM or orgBase(org). */
export function alertsApi(scopeBase: string) {
  const b = `${scopeBase}/alerts`;
  const a = (id: string) => `${b}/${encodeURIComponent(id)}`;
  return {
    list: () => get<unknown>(b).then(listOr<AlertChannel>("Alert channels are")),
    create: (body: AlertInput) => api<AlertChannel>("POST", b, body),
    update: (id: string, body: AlertInput) => api<AlertChannel>("PATCH", a(id), body),
    remove: (id: string) => api("DELETE", a(id)),
    test: (id: string) => api<AlertTest>("POST", `${a(id)}/test`),
  };
}

// ---- enterprise identity (docs/API.md "Enterprise identity", owner only) ----

export type Domain = {
  id: string;
  org_id: string;
  name: string;
  token: string;
  verified: boolean;
  verified_at?: string;
  created_at: string;
  record: { type: "TXT"; name: string; value: string };
};
export type GroupMapping = { group: string; role: Role | ""; workspace_id?: string };
export type SSOSettings = {
  saml: {
    enabled: boolean;
    idp_metadata_url?: string;
    idp_metadata_xml?: string;
    idp_entity_id?: string;
    metadata_fetched_at?: string | null;
    default_role: Role;
    attribute_email?: string;
    attribute_name?: string;
    attribute_groups?: string;
    allow_idp_initiated: boolean;
    enforce: boolean;
    sp_certificate?: string;
  };
  scim: { enabled: boolean; default_role: Role; token_prefix?: string; token_created_at?: string };
  group_mappings: GroupMapping[] | null;
  sp: { entity_id: string; metadata_url: string; acs_url: string; login_url: string; certificate: string };
  scim_base_url: string;
  updated_at: string;
};
export type SAMLInput = {
  enabled: boolean;
  idp_metadata_url?: string;
  idp_metadata_xml?: string;
  default_role?: Role;
  attribute_email?: string;
  attribute_name?: string;
  attribute_groups?: string;
  allow_idp_initiated?: boolean;
  enforce?: boolean;
};

export function identityApi(org: string) {
  const b = orgBase(org);
  const d = (id: string) => `${b}/domains/${encodeURIComponent(id)}`;
  return {
    domains: () => get<Domain[]>(`${b}/domains`),
    addDomain: (name: string) => api<Domain>("POST", `${b}/domains`, { name }),
    verifyDomain: (id: string) => api<Domain>("POST", `${d(id)}/verify`),
    removeDomain: (id: string) => api("DELETE", d(id)),
    sso: () => get<SSOSettings>(`${b}/sso`),
    putSAML: (body: SAMLInput) => api<SSOSettings>("PUT", `${b}/sso/saml`, body),
    putSCIM: (body: { enabled: boolean; default_role?: Role }) => api<SSOSettings>("PUT", `${b}/sso/scim`, body),
    rotateSCIMToken: () => api<{ token: string; scim_base_url: string }>("POST", `${b}/sso/scim/token`),
    putGroupMappings: (m: GroupMapping[]) => api<SSOSettings>("PUT", `${b}/sso/group-mappings`, m),
  };
}

// ---- trace exports (docs/API.md "Trace exports") ----

export type TraceExportType = "otlp" | "langfuse";
export type TraceExport = {
  id: string;
  org_id?: string;
  name: string;
  type: TraceExportType;
  endpoint: string;
  header_names: string[] | null;
  sample_rate: number;
  key_ids?: string[];
  include_content: boolean;
  enabled: boolean;
  failures: number;
  created_at: string;
  workspace_id?: string; // org exports; absent = the default workspace
};
export type TraceExportInput = {
  workspace_id?: string; // create only
  name?: string;
  type?: TraceExportType;
  endpoint?: string;
  headers?: Record<string, string>;
  langfuse?: { public_key: string; secret_key: string };
  sample_rate?: number;
  key_ids?: string[];
  include_content?: boolean;
  enabled?: boolean;
};

/** Trace exports of a scope: PLATFORM or orgBase(org). */
export function traceExportsApi(scopeBase: string, wsList: (path: string) => string = (p) => p) {
  const b = `${scopeBase}/trace-exports`;
  const t = (id: string) => `${b}/${encodeURIComponent(id)}`;
  return {
    list: () => get<unknown>(wsList(b)).then(listOr<TraceExport>("Trace exports are")),
    create: (body: TraceExportInput) => api<TraceExport>("POST", b, body),
    update: (id: string, body: TraceExportInput) => api<TraceExport>("PATCH", t(id), body),
    remove: (id: string) => api("DELETE", t(id)),
    test: (id: string) => api<{ ok: boolean; status?: number; error?: string }>("POST", `${t(id)}/test`),
  };
}

/** "3 minutes ago" style relative time; "–" when absent. */
export function ago(t?: string, now = Date.now()) {
  const d = t ? new Date(t).getTime() : NaN;
  if (Number.isNaN(d)) return "–";
  const s = Math.round((d - now) / 1000);
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  const abs = Math.abs(s);
  if (abs < 60) return rtf.format(s, "second");
  if (abs < 3600) return rtf.format(Math.round(s / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(s / 3600), "hour");
  if (abs < 86400 * 30) return rtf.format(Math.round(s / 86400), "day");
  return new Date(d).toLocaleDateString();
}

/** Human size for byte counts. */
export function bytes(n: number | undefined) {
  const v = n ?? 0;
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  return `${(v / 1024 / 1024).toFixed(1)} MB`;
}

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

/** Like usd() but whole dollars without cents ("$50"), for limits people typed. */
export const usdLimit = (micros: number) => (micros % 1e6 === 0 ? "$" + (micros / 1e6).toLocaleString() : usd(micros));

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
