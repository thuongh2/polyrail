import { useState } from "react";
import { Plus } from "lucide-react";
import { TableFrame } from "@/components/console/bits";
import { SecretDialog } from "@/components/console/SecretDialog";
import { ConfirmDialog, Field, FormDialog } from "@/components/platform/kit";
import { DataState, EmptyState } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ago, api, get, when, type MgmtToken } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const roleLabel = { admin: "Admin", member: "Member", viewer: "Viewer" } as const;
const EXPIRY = [
  ["0", "Never"],
  ["30", "30 days"],
  ["90", "90 days"],
  ["365", "1 year"],
] as const;

function tokenStatus(t: MgmtToken) {
  if (t.revoked) return { label: "Revoked", cls: "text-muted-foreground" };
  if (t.expires_at && new Date(t.expires_at) < new Date()) return { label: "Expired", cls: "border-warning/60 text-foreground" };
  return { label: "Active", cls: "text-success" };
}

/** Org management tokens (admin): bearer tokens for the org admin API, for scripts and CI. */
export function MgmtTokens({ base }: { base: string }) {
  const { can } = useSession();
  const tokens = useAsync(() => get<MgmtToken[] | null>(`${base}/mgmt-tokens`).then((r) => r ?? []), [base]);
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [revoking, setRevoking] = useState<MgmtToken | null>(null);

  const create = (
    <Button size="sm" onClick={() => setCreating(true)}>
      <Plus aria-hidden />
      Create token
    </Button>
  );

  return (
    <>
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Send as <code className="font-mono">Authorization: Bearer pr-mgmt-…</code> to this organization's <code className="font-mono">/api/orgs/…</code>{" "}
          routes. A token acts with its role, never above its creator's current role, and cannot call <code className="font-mono">/v1</code>.
        </p>
        {create}
      </div>
      <DataState
        state={tokens}
        rows={3}
        empty={
          <EmptyState title="No management tokens" action={create}>
            Create one to manage keys, policies or usage exports from a script.
          </EmptyState>
        }
      >
        {(list) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Token</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead>Last used</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((t) => {
                  const st = tokenStatus(t);
                  return (
                    <TableRow key={t.id} className={t.revoked ? "text-muted-foreground" : undefined}>
                      <TableCell className="font-medium">{t.name}</TableCell>
                      <TableCell className="font-mono text-xs whitespace-nowrap">{t.prefix}…</TableCell>
                      <TableCell>{roleLabel[t.role] ?? t.role}</TableCell>
                      <TableCell className="whitespace-nowrap">{t.expires_at ? <span title={when(t.expires_at)}>{new Date(t.expires_at).toLocaleDateString()}</span> : <span className="text-muted-foreground">Never</span>}</TableCell>
                      <TableCell className="whitespace-nowrap">{t.last_used_at ? <span title={when(t.last_used_at)}>{ago(t.last_used_at)}</span> : <span className="text-muted-foreground">Never</span>}</TableCell>
                      <TableCell>
                        <Badge variant="outline" className={st.cls}>
                          {st.label}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right">
                        {!t.revoked && (
                          <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRevoking(t)} aria-label={`Revoke ${t.name}`}>
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

      {creating && (
        <CreateTokenDialog
          base={base}
          maxAdmin={can("admin")}
          onClose={() => setCreating(false)}
          onCreated={(name, s) => {
            setSecret({ name, secret: s });
            tokens.reload();
          }}
        />
      )}
      <SecretDialog
        open={!!secret}
        onClose={() => setSecret(null)}
        title="Copy your management token"
        description={`“${secret?.name}” was created.`}
        label="Management token"
        secret={secret?.secret ?? ""}
        warning="This is the only time the token is shown. Store it in your CI secrets; if you lose it, revoke it and create a new one."
      />
      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={`Revoke “${revoking?.name}”?`}
        description="Scripts using this token start failing immediately. Revoking can't be undone."
        confirmLabel="Revoke token"
        success="Token revoked"
        onConfirm={async () => {
          await api("PATCH", `${base}/mgmt-tokens/${encodeURIComponent(revoking!.id)}`, { revoked: true });
          tokens.reload();
        }}
      />
    </>
  );
}

function CreateTokenDialog({ base, maxAdmin, onClose, onCreated }: { base: string; maxAdmin: boolean; onClose: () => void; onCreated: (name: string, secret: string) => void }) {
  const [open, setOpen] = useState(true);
  const [name, setName] = useState("");
  const [role, setRole] = useState("viewer");
  const [days, setDays] = useState("90");
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title="Create management token"
      description="Pick the lowest role your script needs. Viewer can read usage, logs and exports."
      submitLabel="Create token"
      success="Token created"
      onSubmit={async () => {
        const r = await api<{ token: MgmtToken; secret: string }>("POST", `${base}/mgmt-tokens`, { name: name.trim(), role, expires_in_days: Number(days) });
        onCreated(r.token.name, r.secret);
      }}
    >
      <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} placeholder="ci-usage-export" autoComplete="off" />}</Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Role">
          {(id) => (
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger id={id} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="viewer">Viewer</SelectItem>
                <SelectItem value="member">Member</SelectItem>
                {maxAdmin && <SelectItem value="admin">Admin</SelectItem>}
              </SelectContent>
            </Select>
          )}
        </Field>
        <Field label="Expires">
          {(id) => (
            <Select value={days} onValueChange={setDays}>
              <SelectTrigger id={id} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {EXPIRY.map(([v, l]) => (
                  <SelectItem key={v} value={v}>
                    {l}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </Field>
      </div>
    </FormDialog>
  );
}
