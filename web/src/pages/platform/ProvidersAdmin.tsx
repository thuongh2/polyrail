import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import type { Scope } from "@/App";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { ConfirmDialog, EnabledText, Field, FormDialog, NoAccess, splitList } from "@/components/platform/kit";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, orgBase, PLATFORM, type Provider } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

/** Base path of the providers collection for a scope, or null when it can't be used. */
export function providersPath(scope: Scope, orgId?: string) {
  if (scope === "platform") return PLATFORM + "/providers";
  return orgId ? orgBase(orgId) + "/providers" : null;
}

export default function ProvidersAdmin({ scope = "org" }: { scope?: Scope }) {
  const { org, can, user } = useSession();
  const base = providersPath(scope, org?.id);
  const allowed = scope === "platform" ? !!user?.operator : can("admin");
  const state = useAsync(() => (base && allowed ? get<Provider[]>(base) : Promise.resolve([] as Provider[])), [base, allowed]);
  const [editing, setEditing] = useState<Provider | "new" | null>(null);
  const [deleting, setDeleting] = useState<Provider | null>(null);

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
                  <TableRow key={p.id}>
                    <TableCell className="font-medium">{p.name}</TableCell>
                    <TableCell className="font-mono text-xs">{p.type}</TableCell>
                    <TableCell className="max-w-72 truncate font-mono text-xs text-muted-foreground" title={p.base_url}>
                      {p.base_url || "Provider default"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{p.keys?.length ?? 0}</TableCell>
                    <TableCell className="text-right tabular-nums">{p.timeout_seconds ? `${p.timeout_seconds} s` : "Default"}</TableCell>
                    <TableCell><EnabledText on={p.enabled} /></TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
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

function ProviderDialog({
  scope, base, provider, onClose, onSaved,
}: { scope: Scope; base: string; provider?: Provider; onClose: () => void; onSaved: () => void }) {
  const { config } = useSession();
  const types = config?.provider_types[scope === "platform" ? "platform" : "org"] ?? ["openai"];
  const [open, setOpen] = useState(true);
  const [name, setName] = useState(provider?.name ?? "");
  const [type, setType] = useState(provider?.type ?? types[0] ?? "openai");
  const [baseURL, setBaseURL] = useState(provider?.base_url ?? "");
  const [keys, setKeys] = useState("");
  const [timeout, setTimeoutS] = useState(provider?.timeout_seconds ? String(provider.timeout_seconds) : "");
  const [enabled, setEnabled] = useState(provider?.enabled ?? true);
  const stored = provider?.keys?.length ?? 0;

  const save = async () => {
    const entered = splitList(keys.replace(/,/g, "\n"));
    const body = {
      name: name.trim(),
      type,
      base_url: baseURL.trim(),
      timeout_seconds: timeout ? Number(timeout) : 0,
      enabled,
      // New values replace every stored key; none entered keeps the stored ones (by id, without values).
      keys: entered.length
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
      <div className="grid gap-4 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)]">
        <div className="grid content-start gap-1.5">
          <Label htmlFor="provider-type">Type</Label>
          <Select value={type} onValueChange={setType}>
            <SelectTrigger id="provider-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {types.map((t) => (
                <SelectItem key={t} value={t}>
                  {t}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Field label="Base URL" hint="Optional for hosted providers. Any OpenAI-compatible API works as type openai with its base URL.">
          {(id, hint) => (
            <Input id={id} aria-describedby={hint} value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="https://api.example.com/v1" inputMode="url" autoComplete="off" />
          )}
        </Field>
      </div>
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
    </FormDialog>
  );
}
