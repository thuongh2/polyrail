import { useState, type FormEvent } from "react";
import { Plus, TriangleAlert } from "lucide-react";
import { CopyButton, Field, TableFrame } from "@/components/console/bits";
import { KBPicker } from "@/components/console/KBPicker";
import { FormDialog } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, compact, get, kbApi, orgBase, usd, when, type APIKey, type KB, type Member, type ModelView } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const day = (t?: string) => (t ? new Date(t).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "–");

function keyStatus(k: APIKey) {
  if (k.revoked) return { label: "Revoked", cls: "text-muted-foreground" };
  if (k.expires_at && new Date(k.expires_at) < new Date()) return { label: "Expired", cls: "border-warning/60 text-foreground" };
  return { label: "Active", cls: "text-success" };
}

export default function Keys() {
  const { org, user, can } = useSession();
  const base = org ? orgBase(org.id) : "";
  const keys = useAsync(() => get<APIKey[]>(`${base}/keys`), [base]);
  const members = useAsync(() => get<Member[]>(`${base}/members`).catch(() => [] as Member[]), [base]);
  const [show, setShow] = useQueryParam("show", "active");
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [revoking, setRevoking] = useState<APIKey | null>(null);
  const [grounding, setGrounding] = useState<APIKey | null>(null);
  const admin = can("admin");
  const kbs = useAsync(() => (admin && org ? kbApi(org.id).list() : Promise.resolve([] as KB[])), [org?.id, admin]);

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const canCreate = can("member");
  const canRevoke = (k: APIKey) => !k.revoked && (can("admin") || (can("member") && k.created_by === user?.id));
  const kbName = (id: string) => kbs.data?.find((k) => k.id === id)?.name ?? id;
  const who = (id: string) => (id === user?.id ? "You" : members.data?.find((m) => m.user_id === id)?.email ?? "–");

  async function revoke(k: APIKey) {
    try {
      await api("PATCH", `${base}/keys/${k.id}`, { revoked: true });
      toast(`Revoked “${k.name}”`);
      keys.reload();
    } catch (e) {
      toast((e as Error).message, { error: true });
    }
  }

  const visible = (keys.data ?? []).filter((k) => show === "all" || !k.revoked);
  const revokedCount = (keys.data ?? []).filter((k) => k.revoked).length;

  return (
    <>
      <PageHeader
        title="API keys"
        description="Keys authenticate requests to /v1. Each key can carry its own rate limits, monthly budget, model list and expiry."
        actions={
          canCreate && (
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus aria-hidden />
              Create key
            </Button>
          )
        }
      />
      {revokedCount > 0 && (
        <div className="mb-3 flex items-center gap-2">
          <Checkbox id="show-revoked" checked={show === "all"} onCheckedChange={(v) => setShow(v ? "all" : "active")} />
          <Label htmlFor="show-revoked" className="font-normal text-muted-foreground">
            Show {revokedCount} revoked
          </Label>
        </div>
      )}
      <DataState
        state={keys}
        rows={4}
        empty={
          <EmptyState
            title="No API keys yet"
            action={canCreate && <Button size="sm" onClick={() => setCreating(true)}>Create key</Button>}
          >
            Create a key to call the gateway from your code or the Playground.
          </EmptyState>
        }
      >
        {() => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Key</TableHead>
                  <TableHead>Limits</TableHead>
                  <TableHead>Models</TableHead>
                  <TableHead>Knowledge bases</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visible.map((k) => {
                  const st = keyStatus(k);
                  const limits = [
                    k.rpm ? `${compact(k.rpm)} rpm` : "",
                    k.tpm ? `${compact(k.tpm)} tpm` : "",
                    k.monthly_budget_micros ? `${usd(k.monthly_budget_micros)}/mo` : "",
                  ].filter(Boolean);
                  return (
                    <TableRow key={k.id} className={k.revoked ? "text-muted-foreground" : undefined}>
                      <TableCell className="font-medium">{k.name}</TableCell>
                      <TableCell className="font-mono text-xs whitespace-nowrap">{k.prefix}…</TableCell>
                      <TableCell className="whitespace-nowrap tabular-nums">{limits.length ? limits.join(", ") : <span className="text-muted-foreground">None</span>}</TableCell>
                      <TableCell className="max-w-56">
                        {k.models?.length ? (
                          <span className="block truncate font-mono text-xs" title={k.models.join(", ")}>
                            {k.models.join(", ")}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">All</span>
                        )}
                      </TableCell>
                      <TableCell className="max-w-48">
                        {k.kb_ids?.length ? (
                          <span className="block truncate text-xs" title={k.kb_ids.map(kbName).join(", ")}>
                            {k.kb_ids.map(kbName).join(", ")}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">None</span>
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap">{k.expires_at ? day(k.expires_at) : <span className="text-muted-foreground">Never</span>}</TableCell>
                      <TableCell>
                        <Badge variant="outline" className={st.cls}>
                          {st.label}
                        </Badge>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <span title={when(k.created_at)}>{day(k.created_at)}</span>
                        <span className="block text-xs text-muted-foreground">{who(k.created_by)}</span>
                      </TableCell>
                      <TableCell className="text-right whitespace-nowrap">
                        {admin && !k.revoked && (
                          <Button variant="ghost" size="sm" onClick={() => setGrounding(k)} aria-label={`Knowledge bases for ${k.name}`}>
                            Knowledge bases
                          </Button>
                        )}
                        {canRevoke(k) && (
                          <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRevoking(k)} aria-label={`Revoke ${k.name}`}>
                            Revoke
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>

      <CreateKeyDialog
        open={creating}
        onOpenChange={setCreating}
        base={base}
        kbs={admin ? kbs : undefined}
        onCreated={(name, s) => {
          setCreating(false);
          setSecret({ name, secret: s });
          keys.reload();
        }}
      />

      <Dialog open={!!secret} onOpenChange={(o) => !o && setSecret(null)}>
        <DialogContent onInteractOutside={(e) => e.preventDefault()}>
          <DialogHeader>
            <DialogTitle>Copy your new key</DialogTitle>
            <DialogDescription>“{secret?.name}” was created.</DialogDescription>
          </DialogHeader>
          <div className="flex items-start gap-2 rounded-md border border-warning/50 bg-warning/10 p-3 text-sm">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
            <p>This is the only time the key is shown. Store it somewhere safe; if you lose it, revoke it and create a new one.</p>
          </div>
          <div className="flex items-center gap-2">
            <Label htmlFor="new-secret" className="sr-only">
              API key
            </Label>
            <Input id="new-secret" readOnly value={secret?.secret ?? ""} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
            <CopyButton text={secret?.secret ?? ""} />
          </div>
          <DialogFooter>
            <Button onClick={() => setSecret(null)}>I have saved it</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <KeyKBDialog key={grounding?.id} k={grounding} base={base} kbs={kbs} onClose={() => setGrounding(null)} onSaved={keys.reload} />

      <AlertDialog open={!!revoking} onOpenChange={(o) => !o && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke “{revoking?.name}”?</AlertDialogTitle>
            <AlertDialogDescription>
              Requests using this key start failing immediately. Revoking can't be undone; create a new key if you need access again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction className={buttonVariants({ variant: "destructive" })} onClick={() => revoking && revoke(revoking)}>
              Revoke key
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

type KBList = { data?: KB[]; error?: Error };

/** Admin-only: which knowledge bases ground this key's chat requests (kb_ids, max 3). */
function KeyKBDialog({ k, base, kbs, onClose, onSaved }: { k: APIKey | null; base: string; kbs: KBList; onClose: () => void; onSaved: () => void }) {
  const [picked, setPicked] = useState<string[]>(k?.kb_ids ?? []);
  return (
    <FormDialog
      open={!!k}
      onOpenChange={(o) => !o && onClose()}
      title={`Knowledge bases for “${k?.name}”`}
      description="Chat requests with this key are grounded in the excerpts that best match the latest user message. The X-GW-KB header overrides this per request."
      submitLabel="Save"
      success="Key updated"
      onSubmit={async () => {
        await api("PATCH", `${base}/keys/${k!.id}`, { kb_ids: picked });
        onSaved();
      }}
    >
      <KBPicker legend="Knowledge bases" help="None selected: requests are not grounded." kbs={kbs.data} error={kbs.error} value={picked} onChange={setPicked} />
    </FormDialog>
  );
}

function CreateKeyDialog({
  open,
  onOpenChange,
  base,
  kbs,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  base: string;
  kbs?: KBList; // admins only: kb_ids is an admin field
  onCreated: (name: string, secret: string) => void;
}) {
  const models = useAsync(() => (open ? get<ModelView[]>(`${base}/models`) : Promise.resolve([] as ModelView[])), [base, open]);
  const [name, setName] = useState("");
  const [rpm, setRpm] = useState("");
  const [tpm, setTpm] = useState("");
  const [budget, setBudget] = useState("");
  const [expires, setExpires] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [kbIDs, setKBIDs] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const reset = () => {
    setName(""), setRpm(""), setTpm(""), setBudget(""), setExpires(""), setPicked([]), setKBIDs([]), setErr("");
  };
  const num = (s: string) => (s.trim() === "" ? undefined : Number(s));

  async function submit(e: FormEvent) {
    e.preventDefault();
    const body = { name: name.trim(), rpm: num(rpm), tpm: num(tpm), monthly_budget_usd: num(budget), expires_in_days: num(expires), models: picked.length ? picked : undefined, kb_ids: kbIDs.length ? kbIDs : undefined };
    if ([body.rpm, body.tpm, body.monthly_budget_usd, body.expires_in_days].some((v) => v !== undefined && (!Number.isFinite(v) || v < 0))) {
      setErr("Limits must be positive numbers, or empty for none.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ key: APIKey; secret: string }>("POST", `${base}/keys`, body);
      reset();
      onCreated(r.key.name, r.secret);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => (o || reset(), onOpenChange(o))}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Create API key</DialogTitle>
          <DialogDescription>Leave a limit empty for none. Only admins can change limits later.</DialogDescription>
        </DialogHeader>
        <form id="create-key" onSubmit={submit} className="grid gap-4">
          <Field id="k-name" label="Name">
            <Input id="k-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="production backend" autoFocus />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field id="k-rpm" label="Requests per minute">
              <Input id="k-rpm" inputMode="numeric" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </Field>
            <Field id="k-tpm" label="Tokens per minute">
              <Input id="k-tpm" inputMode="numeric" type="number" min={0} value={tpm} onChange={(e) => setTpm(e.target.value)} />
            </Field>
            <Field id="k-budget" label="Monthly budget (USD)">
              <Input id="k-budget" inputMode="decimal" type="number" min={0} step="0.01" value={budget} onChange={(e) => setBudget(e.target.value)} />
            </Field>
            <Field id="k-exp" label="Expires in (days)">
              <Input id="k-exp" inputMode="numeric" type="number" min={0} value={expires} onChange={(e) => setExpires(e.target.value)} placeholder="Never" />
            </Field>
          </div>
          <fieldset className="grid gap-1.5">
            <legend className="mb-1.5 text-sm font-medium">Allowed models</legend>
            <p className="text-xs text-muted-foreground">{picked.length ? `${picked.length} selected` : "None selected: the key can use every model."}</p>
            <div className="max-h-40 overflow-y-auto rounded-md border p-2">
              {models.error ? (
                <p className="text-sm text-destructive">{models.error.message}</p>
              ) : !models.data ? (
                <p className="text-sm text-muted-foreground">Loading models…</p>
              ) : (
                models.data.map((m) => (
                  <label key={m.id} className="flex items-center gap-2 rounded px-1 py-1 text-sm hover:bg-accent">
                    <Checkbox
                      checked={picked.includes(m.id)}
                      onCheckedChange={(v) => setPicked((p) => (v ? [...p, m.id] : p.filter((x) => x !== m.id)))}
                    />
                    <span className="font-mono text-xs">{m.id}</span>
                  </label>
                ))
              )}
            </div>
          </fieldset>
          {kbs && (
            <KBPicker
              legend="Knowledge bases"
              help="None selected: requests are not grounded. Up to 3."
              kbs={kbs.data}
              error={kbs.error}
              value={kbIDs}
              onChange={setKBIDs}
            />
          )}
          {err && (
            <p role="alert" className="text-sm text-destructive">
              {err}
            </p>
          )}
        </form>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" form="create-key" disabled={busy}>
            {busy ? "Creating…" : "Create key"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
