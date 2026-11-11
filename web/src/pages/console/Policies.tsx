import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { TableFrame } from "@/components/console/bits";
import { CheckList, ChipInput, Part, SwitchRow, providerOptions, type Option } from "@/components/console/pickers";
import { ConfirmDialog, Field, FormDialog, NoAccess } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  api, get, orgBase, usdLimit, type APIKey, type BudgetInterval, type Member, type ModelView, type Policy, type PolicyFilter,
  type PolicyInput,
} from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

export const intervalLabel: Record<BudgetInterval, string> = { daily: "day", weekly: "week", monthly: "month", lifetime: "lifetime" };
/** "$50 / month", "$200 lifetime". */
export const budgetText = (micros: number, interval: BudgetInterval) => `${usdLimit(micros)}${interval === "lifetime" ? " lifetime" : " / " + intervalLabel[interval]}`;

const REGIONS: Option[] = [
  { value: "global", label: "Global" },
  { value: "europe", label: "Europe" },
  { value: "us", label: "United States" },
];

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;

function scopeText(p: Policy) {
  const parts: string[] = [];
  if (p.is_workspace_default) parts.push("Workspace default");
  if (p.is_org_default) {
    const out = (p.opted_out_member_ids?.length ?? 0) + (p.opted_out_key_ids?.length ?? 0);
    parts.push(out ? `Org default, ${out} opted out` : "Org default");
  }
  if (p.member_ids?.length) parts.push(plural(p.member_ids.length, "member"));
  if (p.key_ids?.length) parts.push(plural(p.key_ids.length, "key"));
  return parts.length ? parts.join(" · ") : "Not assigned";
}

export default function Policies() {
  const { org, wsCan, wsList, ws, wsName } = useSession();
  const admin = wsCan("admin");
  const base = org ? orgBase(org.id) : "";
  const policies = useAsync(() => (admin && base ? get<Policy[]>(wsList(`${base}/policies`)) : Promise.resolve([] as Policy[])), [base, admin]);
  const [editing, setEditing] = useState<Policy | "new" | null>(null);
  const [deleting, setDeleting] = useState<Policy | null>(null);

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const description =
    "Budgets, model and provider allowlists, prompt filters and privacy rules for members and API keys. Every policy that applies to a key is enforced; allowlists intersect.";
  if (!admin)
    return (
      <>
        <PageHeader title="Policies" description={description} />
        <NoAccess>Only workspace admins can view and manage policies. The policy that applies to one of your keys is shown on the API keys page.</NoAccess>
      </>
    );

  const create = (
    <Button size="sm" onClick={() => setEditing("new")}>
      <Plus aria-hidden />
      Create policy
    </Button>
  );

  return (
    <>
      <PageHeader title="Policies" description={description} actions={create} />
      <DataState
        state={policies}
        rows={3}
        empty={
          <EmptyState title="No policies yet" action={create}>
            A policy can cap spend per person or per key, limit which models and providers are used, redact or block
            prompt text, and require providers that keep no data.
          </EmptyState>
        }
      >
        {(list) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  {!ws && <TableHead>Workspace</TableHead>}
                  <TableHead>Applies to</TableHead>
                  <TableHead>Budget</TableHead>
                  <TableHead>Allowlists</TableHead>
                  <TableHead>Privacy</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((p) => {
                  const allow = [
                    p.allowed_models?.length ? plural(p.allowed_models.length, "model") : "",
                    p.allowed_providers?.length ? plural(p.allowed_providers.length, "provider") : "",
                    p.filters?.length ? plural(p.filters.length, "filter") : "",
                  ].filter(Boolean);
                  return (
                    <TableRow key={p.id}>
                      <TableCell className="max-w-64">
                        <button type="button" className="rounded-sm text-left font-medium hover:underline focus-visible:outline-2 focus-visible:outline-ring" onClick={() => setEditing(p)}>
                          {p.name}
                        </button>
                        {p.description && <span className="block truncate text-xs text-muted-foreground" title={p.description}>{p.description}</span>}
                      </TableCell>
                      {!ws && <TableCell className="whitespace-nowrap">{wsName(p.workspace_id)}</TableCell>}
                      <TableCell className="whitespace-nowrap">{scopeText(p)}</TableCell>
                      <TableCell className="whitespace-nowrap tabular-nums">
                        {p.budget ? (
                          <>
                            {budgetText(p.budget.limit_micros, p.budget.interval)}
                            {!p.budget.include_byok && (
                              <span className="block text-xs text-amber-600 dark:text-amber-400">Own providers not counted</span>
                            )}
                          </>
                        ) : (
                          <span className="text-muted-foreground">None</span>
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap">{allow.length ? allow.join(", ") : <span className="text-muted-foreground">Any model, any provider</span>}</TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          {p.require_zdr && <Badge variant="outline">ZDR</Badge>}
                          {p.deny_data_collection && <Badge variant="outline">No training</Badge>}
                          {!!p.allowed_regions?.length && (
                            <Badge variant="outline">Regions: {p.allowed_regions.map((r) => REGIONS.find((o) => o.value === r)?.label ?? r).join(", ")}</Badge>
                          )}
                          {!p.require_zdr && !p.deny_data_collection && !p.allowed_regions?.length && <span className="text-muted-foreground">None</span>}
                        </div>
                      </TableCell>
                      <TableCell className="text-right whitespace-nowrap">
                        <Button variant="ghost" size="icon" aria-label={`Edit ${p.name}`} title="Edit" onClick={() => setEditing(p)}>
                          <Pencil aria-hidden />
                        </Button>
                        <Button variant="ghost" size="icon" className="text-destructive hover:text-destructive" aria-label={`Delete ${p.name}`} title="Delete" onClick={() => setDeleting(p)}>
                          <Trash2 aria-hidden />
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>

      {editing && (
        <PolicyDialog
          key={editing === "new" ? "new" : editing.id}
          base={base}
          policy={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={policies.reload}
        />
      )}

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete “${deleting?.name}”?`}
        description="Its budgets, allowlists, filters and privacy rules stop applying at once. Spend already counted is kept."
        confirmLabel="Delete policy"
        success="Policy deleted"
        onConfirm={async () => {
          await api("DELETE", `${base}/policies/${encodeURIComponent(deleting!.id)}`);
          policies.reload();
        }}
      />
    </>
  );
}

type FilterRow = PolicyFilter & { k: number };

function PolicyDialog({ base, policy, onClose, onSaved }: { base: string; policy?: Policy; onClose: () => void; onSaved: () => void }) {
  const [open, setOpen] = useState(true);
  const { can, ws, workspaces } = useSession();
  // The policy's workspace: stored on edit, the current one on create ("All workspaces" creates in the default).
  const pws = policy ? (policy.workspace_id ?? "default") : (ws?.id ?? "default");
  const pwsName = workspaces.find((w) => w.id === pws)?.name ?? pws;
  const inWS = (path: string) => `${path}?workspace=${encodeURIComponent(pws)}`;
  const members = useAsync(() => get<Member[]>(`${base}/members`), [base]);
  const keys = useAsync(() => get<APIKey[]>(inWS(`${base}/keys`)), [base]);
  const models = useAsync(() => get<ModelView[]>(inWS(`${base}/models`)).catch(() => [] as ModelView[]), [base]);
  const providers = useAsync(() => providerOptions(base), [base]);

  const [name, setName] = useState(policy?.name ?? "");
  const [desc, setDesc] = useState(policy?.description ?? "");
  const [isDefault, setDefault] = useState(policy?.is_org_default ?? false);
  const [wsDefault, setWsDefault] = useState(policy?.is_workspace_default ?? false);
  // Org defaults live in the default workspace and only org admins set them.
  const orgDefaultAllowed = can("admin") && pws === "default";
  const [memberIDs, setMemberIDs] = useState(policy?.member_ids ?? []);
  const [keyIDs, setKeyIDs] = useState(policy?.key_ids ?? []);
  const [outMembers, setOutMembers] = useState(policy?.opted_out_member_ids ?? []);
  const [outKeys, setOutKeys] = useState(policy?.opted_out_key_ids ?? []);
  const [hasBudget, setHasBudget] = useState(!!policy?.budget);
  const [limit, setLimit] = useState(policy?.budget ? String(policy.budget.limit_micros / 1e6) : "");
  const [interval, setBudgetInterval] = useState<BudgetInterval>(policy?.budget?.interval ?? "monthly");
  const [byok, setByok] = useState(policy?.budget?.include_byok ?? true);
  const [allowedModels, setAllowedModels] = useState(policy?.allowed_models ?? []);
  const [allowedProviders, setAllowedProviders] = useState(policy?.allowed_providers ?? []);
  const [filters, setFilters] = useState<FilterRow[]>((policy?.filters ?? []).map((f, k) => ({ ...f, k })));
  const [zdr, setZdr] = useState(policy?.require_zdr ?? false);
  const [noTraining, setNoTraining] = useState(policy?.deny_data_collection ?? false);
  const [regions, setRegions] = useState(policy?.allowed_regions ?? []);

  const memberOpts = members.data?.map((m) => ({ value: m.user_id, label: m.email, hint: m.role }));
  const keyOpts = keys.data?.filter((k) => !k.revoked).map((k) => ({ value: k.id, label: k.name, hint: k.prefix + "…" }));
  const setFilter = (k: number, patch: Partial<PolicyFilter>) => setFilters((fs) => fs.map((f) => (f.k === k ? { ...f, ...patch } : f)));

  const save = async () => {
    const usdLimit = Number(limit);
    if (hasBudget && !(usdLimit > 0)) throw new Error("The budget needs a limit above $0.");
    if (filters.some((f) => !f.pattern.trim())) throw new Error("Every filter needs a pattern, or remove the empty row.");
    const body: PolicyInput = {
      name: name.trim(),
      description: desc.trim() || undefined,
      is_org_default: isDefault,
      is_workspace_default: !isDefault && wsDefault,
      ...(policy ? {} : { workspace_id: pws }),
      member_ids: isDefault ? [] : memberIDs,
      key_ids: isDefault ? [] : keyIDs,
      opted_out_member_ids: isDefault ? outMembers : [],
      opted_out_key_ids: isDefault ? outKeys : [],
      budget: hasBudget ? { limit_usd: usdLimit, interval, include_byok: byok } : undefined,
      allowed_models: allowedModels,
      allowed_providers: allowedProviders,
      filters: filters.map(({ name, pattern, action }) => ({ name: name?.trim() || undefined, pattern, action })),
      require_zdr: zdr,
      deny_data_collection: noTraining,
      allowed_regions: regions,
    };
    if (policy) await api("PUT", `${base}/policies/${encodeURIComponent(policy.id)}`, body);
    else await api("POST", `${base}/policies`, body);
    onSaved();
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      wide
      title={policy ? `Edit ${policy.name}` : "Create policy"}
      description={`In the ${pwsName} workspace. Every policy that reaches a key applies: allowlists intersect, privacy rules add up and each budget is checked on its own.`}
      submitLabel={policy ? "Save policy" : "Create policy"}
      success={policy ? "Policy saved" : "Policy created"}
      onSubmit={save}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} placeholder="Engineering" autoComplete="off" />}</Field>
        <Field label="Description">{(id) => <Input id={id} value={desc} onChange={(e) => setDesc(e.target.value)} maxLength={1000} placeholder="Optional" autoComplete="off" />}</Field>
      </div>

      <Part title="Apply to">
        <div className="grid gap-4">
          {(orgDefaultAllowed || isDefault) && (
            <SwitchRow id="pol-default" label="Organization default" help="Applies to every member and API key of every workspace, except the ones you opt out below.">
              <Switch id="pol-default" checked={isDefault} disabled={!orgDefaultAllowed} onCheckedChange={(c) => (setDefault(c), c && setWsDefault(false))} />
            </SwitchRow>
          )}
          {!isDefault && (
            <SwitchRow
              id="pol-ws-default"
              label="Default for this workspace"
              help={`Applies to every API key of the ${pwsName} workspace. Its budget is one limit shared by all of them.`}
            >
              <Switch id="pol-ws-default" checked={wsDefault} onCheckedChange={setWsDefault} />
            </SwitchRow>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            {isDefault ? (
              <>
                <CheckList legend="Opted-out members" help="Keys they create are not covered." options={memberOpts} value={outMembers} onChange={setOutMembers} error={members.error} loading={members.loading} />
                <CheckList legend="Opted-out API keys" options={keyOpts} value={outKeys} onChange={setOutKeys} error={keys.error} loading={keys.loading} empty="No active API keys." />
              </>
            ) : (
              <>
                <CheckList legend="Members" help="Covers every key the member creates; a budget counts their spend across all of them." options={memberOpts} value={memberIDs} onChange={setMemberIDs} error={members.error} loading={members.loading} />
                <CheckList legend="API keys" help="A budget counts this key's own spend." options={keyOpts} value={keyIDs} onChange={setKeyIDs} error={keys.error} loading={keys.loading} empty="No active API keys." />
              </>
            )}
          </div>
        </div>
      </Part>

      <Part title="Budget">
        <div className="grid gap-4">
          <SwitchRow id="pol-budget" label="Limit spend" help="Requests over the limit are refused with 403 policy_budget_exceeded until the interval resets (UTC).">
            <Switch id="pol-budget" checked={hasBudget} onCheckedChange={setHasBudget} />
          </SwitchRow>
          {hasBudget && (
            <div className="grid items-start gap-4 sm:grid-cols-3">
              <Field label="Limit (USD)">
                {(id) => <Input id={id} type="number" inputMode="decimal" min={0.01} step="0.01" required value={limit} onChange={(e) => setLimit(e.target.value)} placeholder="50" />}
              </Field>
              <Field label="Resets">
                {(id) => (
                  <Select value={interval} onValueChange={(v) => setBudgetInterval(v as BudgetInterval)}>
                    <SelectTrigger id={id} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="daily">Every day</SelectItem>
                      <SelectItem value="weekly">Every week (Monday)</SelectItem>
                      <SelectItem value="monthly">Every month (the 1st)</SelectItem>
                      <SelectItem value="lifetime">Never (lifetime)</SelectItem>
                    </SelectContent>
                  </Select>
                )}
              </Field>
              <div className="flex items-center gap-3 sm:pt-7">
                <Switch id="pol-byok" checked={byok} onCheckedChange={setByok} aria-describedby="pol-byok-help" />
                <label htmlFor="pol-byok" className="text-sm">
                  Count own providers
                </label>
              </div>
              <p id="pol-byok-help" className="text-xs text-muted-foreground sm:col-span-3">
                Spend is the model's price for each request. With "Count own providers" off, requests to models
                on this organization's own providers (Organization → Providers) are not counted, and models
                priced at $0 never count.
              </p>
            </div>
          )}
        </div>
      </Part>

      <Part title="Allowed models and providers" description="Empty allows everything.">
        <div className="grid gap-4">
          <ChipInput
            label="Allowed models"
            value={allowedModels}
            onChange={setAllowedModels}
            placeholder="openai/*"
            suggestions={models.data?.map((m) => m.id)}
            help={<>Model ids or globs; <code className="font-mono">*</code> matches any run of characters, including <code className="font-mono">/</code>. Press Enter to add.</>}
          />
          <CheckList legend="Allowed providers" help="A provider type covers every provider of that type." options={providers.data} value={allowedProviders} onChange={setAllowedProviders} error={providers.error} loading={providers.loading} />
        </div>
      </Part>

      <Part
        title="Prompt filters"
        description="RE2 regular expressions matched on request text. Redact replaces matches with [REDACTED:name]; block refuses the request."
      >
        {filters.length === 0 ? (
          <p className="text-sm text-muted-foreground">No filters.</p>
        ) : (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-36">Name</TableHead>
                  <TableHead>Pattern</TableHead>
                  <TableHead className="w-32">Action</TableHead>
                  <TableHead className="w-10">
                    <span className="sr-only">Remove</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filters.map((f, i) => (
                  <TableRow key={f.k}>
                    <TableCell className="p-1.5">
                      <Input aria-label={`Filter ${i + 1} name`} value={f.name ?? ""} onChange={(e) => setFilter(f.k, { name: e.target.value })} placeholder="card" maxLength={64} />
                    </TableCell>
                    <TableCell className="p-1.5">
                      <Input aria-label={`Filter ${i + 1} pattern`} className="font-mono text-xs" spellCheck={false} value={f.pattern} onChange={(e) => setFilter(f.k, { pattern: e.target.value })} placeholder="\b\d{16}\b" />
                    </TableCell>
                    <TableCell className="p-1.5">
                      <Select value={f.action} onValueChange={(v) => setFilter(f.k, { action: v as PolicyFilter["action"] })}>
                        <SelectTrigger aria-label={`Filter ${i + 1} action`} className="w-full">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="redact">Redact</SelectItem>
                          <SelectItem value="block">Block</SelectItem>
                        </SelectContent>
                      </Select>
                    </TableCell>
                    <TableCell className="p-1.5">
                      <Button type="button" variant="ghost" size="icon" aria-label={`Remove filter ${i + 1}`} onClick={() => setFilters((fs) => fs.filter((x) => x.k !== f.k))}>
                        <Trash2 aria-hidden />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableFrame>
        )}
        <div>
          <Button type="button" variant="outline" size="sm" onClick={() => setFilters((fs) => [...fs, { k: Date.now(), name: "", pattern: "", action: "redact" }])}>
            <Plus aria-hidden />
            Add filter
          </Button>
        </div>
      </Part>

      <Part title="Privacy" description="Providers declare their data handling in their settings; undeclared providers fail both rules.">
        <div className="grid gap-4">
          <SwitchRow id="pol-zdr" label="Require zero data retention" help="Only providers that declare they keep no prompts or outputs.">
            <Switch id="pol-zdr" checked={zdr} onCheckedChange={setZdr} />
          </SwitchRow>
          <SwitchRow id="pol-dc" label="Deny data collection" help="Only providers that declare they do not train on prompts.">
            <Switch id="pol-dc" checked={noTraining} onCheckedChange={setNoTraining} />
          </SwitchRow>
          <CheckList legend="Allowed regions" help="Only providers whose declared region is checked here. A provider without a declared region counts as Global. Several policies intersect." options={REGIONS} value={regions} onChange={setRegions} />
        </div>
      </Part>
    </FormDialog>
  );
}
