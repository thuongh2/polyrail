import { useState, type FormEvent } from "react";
import { UserPlus } from "lucide-react";
import { Field, TableFrame } from "@/components/console/bits";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter,
  AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button, buttonVariants } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, orgBase, type Member, type Role } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const ROLES: { value: Role; label: string; help: string }[] = [
  { value: "viewer", label: "Viewer", help: "Sees usage, logs, keys and settings." },
  { value: "member", label: "Member", help: "Also creates API keys and revokes their own." },
  { value: "admin", label: "Admin", help: "Also manages members, providers, models, settings and all keys." },
  { value: "owner", label: "Owner", help: "Also manages owners and can delete the organization." },
];

export default function Members() {
  const { org, user, can } = useSession();
  const base = org ? orgBase(org.id) : "";
  const members = useAsync(() => get<Member[]>(`${base}/members`), [base]);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Member | null>(null);
  if (!org) return <EmptyState title="You are not in an organization yet" />;

  const isAdmin = can("admin");
  const isOwner = can("owner");
  // Admins manage non-owner roles; only owners assign or change owners (mirrors the API's canAssign).
  const assignable = ROLES.filter((r) => isOwner || r.value !== "owner");
  const canManage = (m: Member) => isAdmin && (isOwner || m.role !== "owner");

  async function setRole(m: Member, role: Role) {
    try {
      await api("PUT", `${base}/members/${m.user_id}`, { role });
      toast(`${m.email} is now ${role}`);
      members.reload();
    } catch (e) {
      toast((e as Error).message, { error: true });
    }
  }
  async function remove(m: Member) {
    try {
      await api("DELETE", `${base}/members/${m.user_id}`);
      toast(`Removed ${m.email}`);
      members.reload();
    } catch (e) {
      toast((e as Error).message, { error: true });
    }
  }

  return (
    <>
      <PageHeader
        title="Members"
        description={isAdmin ? "People in this organization and what they can do." : "People in this organization. Only admins can change roles."}
        actions={
          isAdmin && (
            <Button size="sm" onClick={() => setAdding(true)}>
              <UserPlus aria-hidden />
              Add member
            </Button>
          )
        }
      />
      <DataState state={members} rows={3} empty={<EmptyState title="No members" />}>
        {(ms) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Email</TableHead>
                  <TableHead className="hidden sm:table-cell">Name</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {ms.map((m) => {
                  const me = m.user_id === user?.id;
                  return (
                    <TableRow key={m.user_id}>
                      <TableCell className="font-medium">
                        {m.email}
                        {me && <span className="ml-1 font-normal text-muted-foreground">(you)</span>}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">{m.name || <span className="text-muted-foreground">–</span>}</TableCell>
                      <TableCell>
                        {canManage(m) ? (
                          <Select value={m.role} onValueChange={(r) => setRole(m, r as Role)}>
                            <SelectTrigger size="sm" className="w-28" aria-label={`Role for ${m.email}`}>
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {assignable.map((r) => (
                                <SelectItem key={r.value} value={r.value}>
                                  {r.label}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        ) : (
                          <span className="capitalize">{m.role}</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        {canManage(m) && (
                          <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRemoving(m)}>
                            {me ? "Leave" : "Remove"}
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

      <dl className="mt-6 grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[auto_minmax(0,1fr)]">
        {ROLES.map((r) => (
          <div key={r.value} className="contents">
            <dt className="font-medium">{r.label}</dt>
            <dd className="mb-2 text-muted-foreground sm:mb-0">{r.help}</dd>
          </div>
        ))}
      </dl>

      <AddMemberDialog open={adding} onOpenChange={setAdding} base={base} roles={assignable} onAdded={members.reload} />

      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {removing?.user_id === user?.id ? "Leave this organization?" : `Remove ${removing?.email}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              They lose access to this organization right away. API keys they created keep working until an admin revokes them.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction className={buttonVariants({ variant: "destructive" })} onClick={() => removing && remove(removing)}>
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function AddMemberDialog({
  open,
  onOpenChange,
  base,
  roles,
  onAdded,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  base: string;
  roles: typeof ROLES;
  onAdded: () => void;
}) {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr("");
    try {
      await api("POST", `${base}/members`, { email: email.trim(), role, password: password || undefined });
      toast(`Added ${email.trim()}`);
      setEmail(""), setPassword(""), setRole("member");
      onOpenChange(false);
      onAdded();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => (setErr(""), onOpenChange(o))}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add member</DialogTitle>
          <DialogDescription>Existing users are added by email. A new email creates the account.</DialogDescription>
        </DialogHeader>
        <form id="add-member" onSubmit={submit} className="grid gap-4">
          <Field id="m-email" label="Email">
            <Input id="m-email" type="email" required autoComplete="off" value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Field id="m-role" label="Role" help={roles.find((r) => r.value === role)?.help}>
            <Select value={role} onValueChange={(v) => setRole(v as Role)}>
              <SelectTrigger id="m-role" className="w-full" aria-describedby="m-role-help">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {roles.map((r) => (
                  <SelectItem key={r.value} value={r.value}>
                    {r.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field id="m-pass" label="Initial password (optional)" help="At least 10 characters. Only used when the account is new; leave empty for users who sign in with SSO.">
            <Input id="m-pass" type="password" autoComplete="new-password" minLength={10} value={password} onChange={(e) => setPassword(e.target.value)} aria-describedby="m-pass-help" />
          </Field>
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
          <Button type="submit" form="add-member" disabled={busy}>
            {busy ? "Adding…" : "Add member"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
