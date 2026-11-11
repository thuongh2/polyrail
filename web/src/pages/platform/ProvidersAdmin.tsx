import { useState } from "react";
import { Link, useLocation } from "wouter";
import { ListPlus, Pencil, Plus, Trash2 } from "lucide-react";
import type { Scope } from "@/App";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { DiscoverDialog } from "@/components/platform/DiscoverDialog";
import { ConfirmDialog, EnabledText, Field, FormDialog, NoAccess, splitList } from "@/components/platform/kit";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, orgBase, PLATFORM, type Provider, type ProviderKey, type ProxyPool } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { providerLabel } from "@/lib/providers";
import { ProviderLogo } from "@/components/ProviderLogo";

/** Base path of the providers collection for a scope, or null when it can't be used. */
export function providersPath(scope: Scope, orgId?: string) {
  if (scope === "platform") return PLATFORM + "/providers";
  return orgId ? orgBase(orgId) + "/providers" : null;
}

/** Console path (relative to /app) of a provider's detail page. */
export const providerHref = (scope: Scope, id: string) =>
  `${scope === "platform" ? "/platform" : ""}/providers/${encodeURIComponent(id)}`;

export default function ProvidersAdmin({ scope = "org" }: { scope?: Scope }) {
  const { org, can, user } = useSession();
  const base = providersPath(scope, org?.id);
  const allowed = scope === "platform" ? !!user?.operator : can("admin");
  const state = useAsync(() => (base && allowed ? get<Provider[]>(base) : Promise.resolve([] as Provider[])), [base, allowed]);
  const [editing, setEditing] = useState<Provider | "new" | null>(null);
  const [deleting, setDeleting] = useState<Provider | null>(null);
  const [discovering, setDiscovering] = useState<Provider | null>(null);
  const [, navigate] = useLocation();

  const title = scope === "platform" ? "Public providers" : "Providers";
  const description =
    scope === "platform"
      ? "Upstream providers serving public models. Every organization can route to them; their keys are the platform's."
      : `Private providers for ${org?.name ?? "this organization"}. Their keys and models are visible only to this organization.`;

  if (!allowed || !base) {
    return (
      <>
        <PageHeader title={title} />
        <NoAccess>{scope === "platform" ? "Only operators manage public providers." : "Only organization admins manage providers."}</NoAccess>
      </>
    );
  }

  const add = (
    <Button onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add provider
    </Button>
  );

  return (
    <>
      <PageHeader title={title} description={description} actions={add} />
      <DataState
        state={state}
        empty={
          <EmptyState title="No providers yet" action={add}>
            Add an upstream API and its keys, then point models at it. Any OpenAI-compatible API works as type openai with its base URL.
          </EmptyState>
        }
      >
        {(ps) => (
          <div className="rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead>Base URL</TableHead>
                  <TableHead className="text-right">Keys</TableHead>
                  <TableHead className="text-right">Timeout</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {[...ps].sort((a, b) => a.name.localeCompare(b.name)).map((p) => (
                  <TableRow
                    key={p.id}
                    className="cursor-pointer"
                    // The name link is the keyboard path; a click anywhere else on the row (not a button) opens it too.
                    onClick={(e) => !(e.target as HTMLElement).closest("button, a") && navigate(providerHref(scope, p.id))}
                  >
                    <TableCell className="font-medium">
                      <span className="flex items-center gap-2">
                        <ProviderLogo type={p.type} />
                        <Link
                          href={providerHref(scope, p.id)}
                          className="rounded-sm underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
                        >
                          {p.name}
                        </Link>
                        {p.proxy?.url && (
                          <Badge variant="outline" title={`Upstream calls go through ${p.proxy.url}`}>
                            via proxy
                          </Badge>
                        )}
                        {p.proxy_pool && (
                          <Badge variant="outline" title={`Upstream calls rotate through proxy pool ${p.proxy_pool}`}>
                            via proxy pool
                          </Badge>
                        )}
                      </span>
                    </TableCell>
                    <TableCell className="text-sm" title={p.type}>{providerLabel(p.type)}</TableCell>
                    <TableCell className="max-w-72 truncate font-mono text-xs text-muted-foreground" title={p.base_url}>
                      {p.base_url || "Provider default"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{p.keys?.length ?? 0}</TableCell>
                    <TableCell className="text-right tabular-nums">{p.timeout_seconds ? `${p.timeout_seconds} s` : "Default"}</TableCell>
                    <TableCell><EnabledText on={p.enabled} /></TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={!p.enabled}
                          title={p.enabled ? undefined : "Enable the provider to list its models"}
                          aria-label={`Discover models from ${p.name}`}
                          onClick={() => setDiscovering(p)}
                        >
                          <ListPlus aria-hidden /> Discover models
                        </Button>
                        <Button size="icon" variant="ghost" aria-label={`Edit ${p.name}`} onClick={() => setEditing(p)}>
                          <Pencil aria-hidden />
                        </Button>
                        <Button size="icon" variant="ghost" aria-label={`Delete ${p.name}`} onClick={() => setDeleting(p)}>
                          <Trash2 aria-hidden />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </DataState>

      {editing && (
        <ProviderDialog
          key={editing === "new" ? "new" : editing.id}
          scope={scope}
          base={base}
          provider={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={state.reload}
        />
      )}
      {discovering && <DiscoverDialog key={discovering.id} base={base} provider={discovering} onClose={() => setDiscovering(null)} />}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "provider"}?`}
        description="Its stored API keys are deleted. Models that route to it lose that target and fail if it was their only one."
        confirmLabel="Delete provider"
        success="Provider deleted"
        onConfirm={async () => {
          await api("DELETE", `${base}/${encodeURIComponent(deleting!.id)}`);
          state.reload();
        }}
      />
    </>
  );
}

/** Self-hosted or workspace types that only work with a base URL. */
const needsURL = (t: string) => ["ollama", "sgl", "vllm", "databricks"].includes(t);

/** Types whose credentials live in per-key config; the dialog edits their first key. */
const CLOUD = ["azure", "bedrock", "bedrock_mantle", "sagemaker", "vertex", "cloudflare"] as const;
type Cloud = (typeof CLOUD)[number];
export const isCloud = (t: string): t is Cloud => (CLOUD as readonly string[]).includes(t);
/** AWS types share the key's "bedrock" config block; only bedrock takes an ARN. */
const isAWS = (t: string) => t === "bedrock" || t === "bedrock_mantle" || t === "sagemaker";

type CloudForm = {
  apiKey: string;
  endpoint: string;
  apiVersion: string;
  region: string;
  arn: string;
  accessKey: string;
  secretKey: string;
  sessionToken: string;
  projectId: string;
  projectNumber: string;
  credentials: string;
  deployments: string;
  accountId: string;
};

const deploymentsText = (d?: Record<string, string>) =>
  Object.entries(d ?? {})
    .map(([m, dep]) => `${m}=${dep}`)
    .join("\n");

/** Parses "model=deployment" lines; blank lines are skipped, a line without "=" is an error. */
function parseDeployments(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const line of text.split("\n").map((l) => l.trim()).filter(Boolean)) {
    const i = line.indexOf("=");
    if (i < 1 || i === line.length - 1) throw new Error(`Deployment "${line}" must look like model=deployment`);
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return Object.keys(out).length ? out : undefined;
}

function cloudForm(k?: ProviderKey): CloudForm {
  return {
    apiKey: "",
    endpoint: k?.azure?.endpoint ?? "",
    apiVersion: k?.azure?.api_version ?? "",
    region: k?.bedrock?.region ?? k?.vertex?.region ?? "",
    arn: k?.bedrock?.arn ?? "",
    accessKey: "",
    secretKey: "",
    sessionToken: "",
    projectId: k?.vertex?.project_id ?? "",
    projectNumber: k?.vertex?.project_number ?? "",
    credentials: "",
    deployments: deploymentsText(k?.azure?.deployments ?? k?.bedrock?.deployments ?? k?.vertex?.deployments),
    accountId: k?.cloudflare?.account_id ?? "",
  };
}

/** The first key of a cloud provider, built from the form. Empty secrets keep the stored ones. */
function cloudKey(type: Cloud, f: CloudForm, prev?: ProviderKey): ProviderKey {
  const deployments = parseDeployments(f.deployments);
  const opt = (v: string) => v.trim() || undefined;
  const k: ProviderKey = { id: prev?.id, name: prev?.name, weight: prev?.weight || 1, models: prev?.models, value: opt(f.apiKey) };
  if (type === "azure") k.azure = { endpoint: f.endpoint.trim(), api_version: opt(f.apiVersion), deployments };
  if (isAWS(type))
    k.bedrock = {
      region: f.region.trim(),
      arn: type === "bedrock" ? opt(f.arn) : undefined,
      deployments,
      access_key: opt(f.accessKey),
      secret_key: opt(f.secretKey),
      session_token: opt(f.sessionToken),
    };
  if (type === "cloudflare") {
    const account_id = f.accountId.trim().toLowerCase();
    if (!/^[0-9a-f]{32}$/.test(account_id)) throw new Error("Account ID must be 32 hexadecimal characters");
    k.cloudflare = { account_id };
  }
  if (type === "vertex")
    k.vertex = {
      project_id: f.projectId.trim(),
      project_number: opt(f.projectNumber),
      region: f.region.trim(),
      deployments,
      auth_credentials: opt(f.credentials),
    };
  return k;
}

export function ProviderDialog({
  scope, base, provider, onClose, onSaved,
}: { scope: Scope; base: string; provider?: Provider; onClose: () => void; onSaved: () => void }) {
  const { config } = useSession();
  const types = config?.provider_types[scope === "platform" ? "platform" : "org"] ?? ["openai"];
  // Org providers of these types run on the vendor's fixed API URL; the server refuses a base URL.
  const fixedURL = (t: string) => scope !== "platform" && !!config?.provider_types.org_fixed_url?.includes(t);
  const [open, setOpen] = useState(true);
  const [name, setName] = useState(provider?.name ?? "");
  const [type, setType] = useState(provider?.type ?? types[0] ?? "openai");
  const [baseURL, setBaseURL] = useState(provider?.base_url ?? "");
  const [keys, setKeys] = useState("");
  const [timeout, setTimeoutS] = useState(provider?.timeout_seconds ? String(provider.timeout_seconds) : "");
  const [enabled, setEnabled] = useState(provider?.enabled ?? true);
  const stored = provider?.keys?.length ?? 0;
  // Stored cloud config only applies while the type is unchanged.
  const sameType = !!provider && provider.type === type;
  const [cloud, setCloud] = useState<CloudForm>(() => cloudForm(provider && isCloud(provider.type) ? provider.keys?.[0] : undefined));
  const set = (k: keyof CloudForm) => (e: { target: { value: string } }) => setCloud((c) => ({ ...c, [k]: e.target.value }));
  // Org providers set their own proxy only when the operator allows it (ALLOW_TENANT_PROXY).
  const ownProxy = scope === "platform" || !!config?.tenant_proxy;
  const [proxyURL, setProxyURL] = useState(provider?.proxy?.url ?? "");
  const [proxyUser, setProxyUser] = useState(provider?.proxy?.username ?? "");
  const [proxyPass, setProxyPass] = useState("");
  const [proxyCA, setProxyCA] = useState(provider?.proxy?.ca_cert_pem ?? "");
  const [noProxy, setNoProxy] = useState(provider?.no_proxy ?? false);
  // Proxy pools are operator-managed (Platform → Proxies) and only platform providers use them.
  const [pool, setPool] = useState(provider?.proxy_pool ?? "");
  const pools = useAsync(() => (scope === "platform" ? get<ProxyPool[]>(PLATFORM + "/proxy-pools") : Promise.resolve([] as ProxyPool[])), [scope]);
  const single = ownProxy && !pool;
  // A stored password is kept while the proxy URL and username stay the same.
  const keepsProxyPass = !!provider?.proxy?.username && provider.proxy.url === proxyURL.trim() && provider.proxy.username === proxyUser.trim();

  const save = async () => {
    const entered = splitList(keys.replace(/,/g, "\n"));
    const body = {
      name: name.trim(),
      type,
      base_url: isCloud(type) || fixedURL(type) ? "" : baseURL.trim(),
      timeout_seconds: timeout ? Number(timeout) : 0,
      proxy:
        single && proxyURL.trim()
          ? {
              url: proxyURL.trim(),
              username: proxyUser.trim() || undefined,
              password: proxyPass || undefined,
              ca_cert_pem: proxyCA.trim() || undefined,
            }
          : undefined,
      no_proxy: noProxy,
      proxy_pool: pool || undefined,
      enabled,
      // Cloud types: the form edits the first key; other stored keys are kept as they are.
      // Otherwise new values replace every stored key; none entered keeps the stored ones (by id).
      keys: isCloud(type)
        ? [cloudKey(type, cloud, sameType ? provider?.keys?.[0] : undefined), ...(sameType ? (provider?.keys ?? []).slice(1) : [])]
        : entered.length
          ? entered.map((value) => ({ value, weight: 1 }))
          : (provider?.keys ?? []).map(({ id, name, weight, models }) => ({ id, name, weight, models })),
    };
    if (provider) await api("PUT", `${base}/${encodeURIComponent(provider.id)}`, body);
    else await api("POST", base, body);
    onSaved();
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title={provider ? `Edit ${provider.name}` : "Add provider"}
      description={scope === "platform" ? "A public provider: every organization's models may route to it." : "A private provider: only this organization can use it."}
      submitLabel={provider ? "Save changes" : "Add provider"}
      success={provider ? "Provider saved" : "Provider added"}
      onSubmit={save}
    >
      <Field label="Name">
        {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} required placeholder="openai-main" autoComplete="off" />}
      </Field>
      <div className="grid gap-4 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="provider-type">Type</Label>
          <Select value={type} onValueChange={setType}>
            <SelectTrigger id="provider-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {[...types].sort((a, b) => providerLabel(a).localeCompare(providerLabel(b))).map((t) => (
                <SelectItem key={t} value={t}>
                  <ProviderLogo type={t} size={16} />
                  {providerLabel(t)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {fixedURL(type) ? (
          <p className="self-end pb-2 text-sm text-muted-foreground">Uses the {providerLabel(type)} API.</p>
        ) : !isCloud(type) && (
          <Field
            label="Base URL"
            hint={
              needsURL(type)
                ? type === "databricks"
                  ? "Required: the workspace URL. Put the access token in API keys."
                  : "Required: the server's URL. The key is optional."
                : "Optional for hosted providers. Any OpenAI-compatible API works as type openai with its base URL."
            }
          >
            {(id, hint) => (
              <Input id={id} aria-describedby={hint} required={needsURL(type)} value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://api.example.com/v1" inputMode="url" autoComplete="off" />
            )}
          </Field>
        )}
      </div>
      {isCloud(type) ? (
        <CloudFields type={type} form={cloud} set={set} keep={sameType} tenant={scope !== "platform"} />
      ) : (
        <Field
          label="API keys"
          hint={
            provider
              ? `${stored} key${stored === 1 ? "" : "s"} stored. Leave empty to keep them; new keys replace all stored keys. Re-enter keys when you change the type or base URL.`
              : "One per line. Requests are spread across keys. Keys are encrypted and never shown again."
          }
        >
          {(id, hint) => (
            <Textarea
              id={id}
              aria-describedby={hint}
              value={keys}
              onChange={(e) => setKeys(e.target.value)}
              rows={3}
              className="font-mono text-xs"
              placeholder={provider ? "Leave empty to keep stored keys" : "sk-..."}
              autoComplete="off"
              spellCheck={false}
            />
          )}
        </Field>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Timeout (seconds)" hint="Empty uses the default.">
          {(id, hint) => (
            <Input id={id} aria-describedby={hint} type="number" min={1} max={600} value={timeout} onChange={(e) => setTimeoutS(e.target.value)} />
          )}
        </Field>
        <div className="flex items-center gap-3 sm:pt-6">
          <Switch id="provider-enabled" checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor="provider-enabled">Enabled</Label>
        </div>
      </div>
      <details className="rounded-md border px-3 py-2" open={!!provider?.proxy?.url || !!provider?.no_proxy || !!provider?.proxy_pool}>
        <summary className="cursor-pointer rounded-sm text-sm font-medium focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none">
          Proxy
        </summary>
        <div className="mt-3 grid gap-4 pb-1">
          {scope === "platform" && (
            <div className="grid content-start gap-1.5">
              <Label htmlFor="provider-proxy-pool">Proxy pool</Label>
              <Select value={pool || "none"} onValueChange={(v) => setPool(v === "none" ? "" : v)}>
                <SelectTrigger id="provider-proxy-pool" className="w-full" aria-describedby="provider-proxy-pool-hint">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">None: a single proxy below, or the global default</SelectItem>
                  {(pools.data ?? []).map((x) => (
                    <SelectItem key={x.id} value={x.id}>{x.name} ({x.members.length} member{x.members.length === 1 ? "" : "s"})</SelectItem>
                  ))}
                  {pool && !pools.data?.some((x) => x.id === pool) && <SelectItem value={pool}>{pool}</SelectItem>}
                </SelectContent>
              </Select>
              <p id="provider-proxy-pool-hint" className="text-xs text-muted-foreground">
                Rotates new connections across the pool's proxies. Manage pools on the Proxies page.
              </p>
            </div>
          )}
          {single && (
            <>
              <Field label="Proxy URL" hint="http://, https:// or socks5://host:port. Empty uses the gateway's global proxy, if any.">
                {(id, hint) => (
                  <Input id={id} aria-describedby={hint} value={proxyURL} onChange={(e) => setProxyURL(e.target.value)} placeholder="http://proxy.internal:3128" inputMode="url" autoComplete="off" spellCheck={false} />
                )}
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Username">
                  {(id) => <Input id={id} value={proxyUser} onChange={(e) => setProxyUser(e.target.value)} autoComplete="off" spellCheck={false} />}
                </Field>
                <Field label="Password" hint={keepsProxyPass ? "Leave blank to keep the stored password." : "Encrypted and never shown again."}>
                  {(id, hint) => (
                    <Input
                      id={id}
                      aria-describedby={hint}
                      type="password"
                      value={proxyPass}
                      onChange={(e) => setProxyPass(e.target.value)}
                      required={!!proxyUser.trim() && !keepsProxyPass}
                      placeholder={keepsProxyPass ? "Leave blank to keep" : undefined}
                      autoComplete="new-password"
                      spellCheck={false}
                    />
                  )}
                </Field>
              </div>
              <Field label="CA certificate" hint="Optional PEM, for a TLS-inspecting or https:// proxy.">
                {(id, hint) => (
                  <Textarea
                    id={id}
                    aria-describedby={hint}
                    value={proxyCA}
                    onChange={(e) => setProxyCA(e.target.value)}
                    rows={3}
                    className="font-mono text-xs"
                    placeholder="-----BEGIN CERTIFICATE-----"
                    autoComplete="off"
                    spellCheck={false}
                  />
                )}
              </Field>
            </>
          )}
          <div className="flex items-center gap-3">
            <Switch id="provider-no-proxy" checked={noProxy} onCheckedChange={setNoProxy} disabled={(single && !!proxyURL.trim()) || !!pool} />
            <Label htmlFor="provider-no-proxy">Don't use the global proxy</Label>
          </div>
          <p className="text-xs text-muted-foreground">
            The global proxy is set by the operator (UPSTREAM_PROXY_POOL or UPSTREAM_PROXY_URL). With this on, the provider connects directly.
          </p>
        </div>
      </details>
    </FormDialog>
  );
}

function CloudFields({
  type, form, set, keep, tenant,
}: {
  type: Cloud;
  form: CloudForm;
  set: (k: keyof CloudForm) => (e: { target: { value: string } }) => void;
  keep: boolean;
  tenant: boolean;
}) {
  const secret = (label: string, k: keyof CloudForm, hint?: string, required = false) => (
    <Field label={label} hint={hint}>
      {(id, h) => (
        <Input
          id={id}
          aria-describedby={h}
          type="password"
          value={form[k]}
          onChange={set(k)}
          required={required && !keep}
          placeholder={keep ? "Leave empty to keep" : undefined}
          autoComplete="new-password"
          spellCheck={false}
        />
      )}
    </Field>
  );
  const text = (label: string, k: keyof CloudForm, opts: { hint?: string; placeholder?: string; required?: boolean } = {}) => (
    <Field label={label} hint={opts.hint}>
      {(id, h) => (
        <Input id={id} aria-describedby={h} value={form[k]} onChange={set(k)} required={opts.required} placeholder={opts.placeholder} autoComplete="off" spellCheck={false} />
      )}
    </Field>
  );
  const keepNote = keep && (
    <p className="text-xs text-muted-foreground">Secrets are never shown. Leave a secret field empty to keep its stored value.</p>
  );
  const deployments = (hint: string) => (
    <Field label="Deployments" hint={hint}>
      {(id, h) => (
        <Textarea
          id={id}
          aria-describedby={h}
          value={form.deployments}
          onChange={set("deployments")}
          rows={3}
          className="font-mono text-xs"
          placeholder="model=deployment"
          autoComplete="off"
          spellCheck={false}
        />
      )}
    </Field>
  );

  if (type === "azure")
    return (
      <>
        {text("Endpoint", "endpoint", { required: true, placeholder: "https://my-resource.openai.azure.com" })}
        <div className="grid gap-4 sm:grid-cols-2">
          {secret("API key", "apiKey", keep ? undefined : "Encrypted and never shown again.", true)}
          {text("API version", "apiVersion", { hint: "Empty uses 2024-10-21.", placeholder: "2024-10-21" })}
        </div>
        {keepNote}
        {deployments("One model=deployment per line. When set, only these models are served.")}
      </>
    );
  if (type === "cloudflare")
    return (
      <>
        <div className="grid gap-4 sm:grid-cols-2">
          {secret("API token", "apiKey", keep ? undefined : "A Workers AI token. Encrypted and never shown again.", true)}
          <Field label="Account ID" hint="32 hexadecimal characters, from the Cloudflare dashboard.">
            {(id, h) => (
              <Input
                id={id}
                aria-describedby={h}
                value={form.accountId}
                onChange={set("accountId")}
                required
                pattern="[0-9a-fA-F]{32}"
                title="32 hexadecimal characters"
                className="font-mono text-xs"
                autoComplete="off"
                spellCheck={false}
              />
            )}
          </Field>
        </div>
        {keepNote}
      </>
    );
  if (isAWS(type))
    return (
      <>
        <div className="grid gap-4 sm:grid-cols-2">
          {text("Region", "region", { required: true, placeholder: "us-east-1" })}
          {type === "bedrock" && text("ARN", "arn", { hint: "Optional inference profile ARN prefix." })}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          {secret("Access key ID", "accessKey")}
          {secret("Secret access key", "secretKey")}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          {secret("Session token", "sessionToken", "Only for temporary credentials.")}
          {secret(type === "sagemaker" ? "API key" : "Bedrock API key", "apiKey", "Optional; used instead of access keys when set.")}
        </div>
        <p className="text-xs text-muted-foreground">
          {tenant
            ? "Enter an access key pair or an API key."
            : "Without credentials, requests use the gateway host's AWS identity (IAM role, environment)."}
        </p>
        {keepNote}
        {deployments(
          type === "sagemaker"
            ? "One model=endpoint per line, mapping model names to SageMaker endpoints. When set, only these models are served."
            : "One model=profile per line, mapping model names to inference profiles. When set, only these models are served.",
        )}
      </>
    );
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-3">
        {text("Project ID", "projectId", { required: true, placeholder: "my-project" })}
        {text("Project number", "projectNumber", { placeholder: "Optional" })}
        {text("Region", "region", { required: true, placeholder: "us-central1" })}
      </div>
      <Field label="Service account JSON" hint="Without it, requests use the gateway host's Google credentials. Encrypted and never shown again.">
        {(id, h) => (
          <Textarea
            id={id}
            aria-describedby={h}
            value={form.credentials}
            onChange={set("credentials")}
            rows={4}
            className="font-mono text-xs"
            placeholder={keep ? "Leave empty to keep" : '{"type": "service_account", ...}'}
            autoComplete="off"
            spellCheck={false}
          />
        )}
      </Field>
      {keepNote}
      {deployments("One model=endpoint per line. When set, only these models are served.")}
    </>
  );
}
