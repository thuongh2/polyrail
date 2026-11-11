import { useState } from "react";
import { Pencil, Plus, Trash2, UserPlus, Users } from "lucide-react";
import { TableFrame } from "@/components/console/bits";
import { ConfirmDialog, Field, FormDialog } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ApiError, api, get, orgBase, type Member, type Role, type Workspace, type WorkspaceMember } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { roleAtLeast, useSession } from "@/lib/session";

const roleLabel: Record<Role, string> = { owner: "Owner", admin: "Admin", member: "Member", viewer: "Viewer" };
const WS_ROLES: Role[] = ["viewer", "member", "admin"];

/** "my-team" from "My Team!" (the API's slug rule: ^[a-z0-9][a-z0-9-]{0,62}$). */
export const slugify = (s: string) =>
  s
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/-+$/, "");

/** Names of the 409 counts from DELETE …/workspaces/{ws}, singular and plural. */
const what: Record<string, [string, string]> = {
  api_keys: ["key", "keys"],
  providers: ["provider", "providers"],
  models: ["model", "models"],
  policies: ["policy", "policies"],
  presets: ["preset", "presets"],
  trace_exports: ["trace export", "trace exports"],
  knowledge_bases: ["knowledge base", "knowledge bases"],
  mgmt_tokens: ["management token", "management tokens"],
};

/** "Move or delete 3 keys, 1 provider first." from a 409's counts. */
export function contentsMessage(counts: Record<string, number>) {
  const parts = Object.entries(counts)
    .filter(([, n]) => n > 0)
    .map(([k, n]) => `${n} ${(what[k] ?? [k.replace(/_/g, " "), k.replace(/_/g, " ")])[n === 1 ? 0 : 1]}`);
  return `This workspace is not empty. Move or delete ${parts.join(", ")} first.`;
}

export default function Workspaces() {
  const { org, can, refresh, setWs, wsId } = useSession();
  const base = org ? orgBase(org.id) + "/workspaces" : "";
  const list = useAsync(() => (base ? get<Workspace[]>(base) : Promise.resolve([] as Workspace[])), [base]);
  const [tab, setTab] = useQueryParam("tab", "list");
  const [selected, setSelected] = useQueryParam("w", "");
  const [editing, setEditing] = useState<Workspace | "new" | null>(null);
  const [deleting, setDeleting] = useState<Workspace | null>(null);

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const orgAdmin = can("admin");
  const admins = (list.data ?? []).filter((w) => roleAtLeast(w.role, "admin"));
  const reload = () => {
    list.reload();
    refresh().catch(() => {}); // the switcher lists workspaces from the session
  };
  const openMembers = (w: Workspace) => {
    setSelected(w.id);
    setTab("members");
  };

  const create = orgAdmin && (
    <Button size="sm" onClick={() => setEditing("new")}>
      <Plus aria-hidden />
      Create workspace
    </Button>
  );

  return (
    <>
      <PageHeader
        title="Workspaces"
        description="Workspaces split API keys, providers, models, policies, presets, knowledge bases and trace exports into separate groups with their own members. Credits, webhooks, alerts and SSO stay organization-wide."
        actions={tab === "list" && create}
      />
      {admins.length > 0 && (
        <Tabs value={tab === "members" ? "members" : "list"} onValueChange={setTab} className="mb-4">
          <TabsList aria-label="Workspaces view">
            <TabsTrigger value="list">Workspaces</TabsTrigger>
            <TabsTrigger value="members">Members</TabsTrigger>
          </TabsList>
        </Tabs>
      )}
      {tab === "members" && admins.length > 0 ? (
        <WorkspaceMembers base={base} orgBasePath={orgBase(org.id)} workspaces={admins} selected={admins.some((w) => w.id === selected) ? selected : (admins.find((w) => w.id === wsId) ?? admins[0]).id} onSelect={setSelected} />
      ) : (
        <DataState
          state={list}
          rows={3}
          empty={
            <EmptyState title="No workspaces" action={create}>
              Every organization has a default workspace; it appears here once loaded.
            </EmptyState>
          }
        >
          {(ws) => (
            <TableFrame>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Slug</TableHead>
                    <TableHead>Your role</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead>
                      <span className="sr-only">Actions</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {ws.map((w) => (
                    <TableRow key={w.id}>
                      <TableCell className="font-medium">
                        <span className="flex items-center gap-2">
                          {w.name}
                          {w.is_default && w.name !== "Default" && <Badge variant="outline">Default</Badge>}
                          {w.id === wsId && <span className="text-xs font-normal text-muted-foreground">Current</span>}
                        </span>
                      </TableCell>
                      <TableCell className="font-mono text-xs">{w.slug}</TableCell>
                      <TableCell>{roleLabel[w.role] ?? w.role}</TableCell>
                      <TableCell className="whitespace-nowrap">{w.created_at ? new Date(w.created_at).toLocaleDateString() : "–"}</TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          {w.id !== wsId && (
                            <Button size="sm" variant="ghost" onClick={() => setWs(w.id)} aria-label={`Switch to ${w.name}`}>
                              Switch
                            </Button>
                          )}
                          {roleAtLeast(w.role, "admin") && (
                            <Button size="sm" variant="outline" onClick={() => openMembers(w)} aria-label={`Members of ${w.name}`}>
                              <Users aria-hidden /> Members
                            </Button>
                          )}
                          {orgAdmin && (
                            <Button size="icon" variant="ghost" aria-label={`Rename ${w.name}`} title="Rename" onClick={() => setEditing(w)}>
                              <Pencil aria-hidden />
                            </Button>
                          )}
                          {orgAdmin && !w.is_default && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="text-destructive hover:text-destructive"
                              aria-label={`Delete ${w.name}`}
                              title="Delete"
                              onClick={() => setDeleting(w)}
                            >
                              <Trash2 aria-hidden />
                            </Button>
                          )}
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </TableFrame>
          )}
        </DataState>
      )}
      {!orgAdmin && tab === "list" && (
        <p className="mt-3 text-sm text-muted-foreground">Organization owners and admins create, rename and delete workspaces.</p>
      )}

      {editing && (
        <WorkspaceDialog
          key={editing === "new" ? "new" : editing.id}
          base={base}
          workspace={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={reload}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete the ${deleting?.name ?? ""} workspace?`}
        description="Only an empty workspace can be deleted: nothing in it is deleted with it. Its explicit members lose access."
        confirmLabel="Delete workspace"
        success="Workspace deleted"
        onConfirm={async () => {
          try {
            await api("DELETE", `${base}/${encodeURIComponent(deleting!.id)}`);
          } catch (e) {
            const counts = e instanceof ApiError && e.status === 409 ? (e.detail?.counts as Record<string, number> | undefined) : undefined;
            if (counts) throw new Error(contentsMessage(counts));
            throw e;
          }
          if (deleting!.id === wsId) setWs("default");
          reload();
        }}
      />
    </>
  );
}

function WorkspaceDialog({ base, workspace, onClose, onSaved }: { base: string; workspace?: Workspace; onClose: () => void; onSaved: () => void }) {
  const [open, setOpen] = useState(true);
  const [name, setName] = useState(workspace?.name ?? "");
  const [slug, setSlug] = useState(workspace?.slug ?? "");
  // The slug follows the name until edited by hand (create only).
  const [slugTouched, setSlugTouched] = useState(!!workspace);
  const fixedSlug = !!workspace?.is_default;
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title={workspace ? `Rename ${workspace.name}` : "Create workspace"}
      description={workspace ? "Links that use the old slug stop working." : "You become an admin of it as an organization admin. Add members after creating it."}
      submitLabel={workspace ? "Save" : "Create workspace"}
      success={workspace ? "Workspace renamed" : "Workspace created"}
      onSubmit={async () => {
        const body = { name: name.trim(), slug: fixedSlug ? undefined : slug.trim() };
        if (workspace) await api("PATCH", `${base}/${encodeURIComponent(workspace.id)}`, body);
        else await api("POST", base, body);
        onSaved();
      }}
    >
      <Field label="Name">
        {(id) => (
          <Input
            id={id}
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              if (!slugTouched) setSlug(slugify(e.target.value));
            }}
            required
            maxLength={100}
            placeholder="Research"
            autoComplete="off"
            autoFocus
          />
        )}
      </Field>
      <Field label="Slug" hint={fixedSlug ? "The default workspace keeps the slug default." : "Lowercase letters, digits and hyphens; unique in the organization."}>
        {(id, hint) => (
          <Input
            id={id}
            aria-describedby={hint}
            value={slug}
            disabled={fixedSlug}
            onChange={(e) => {
              setSlugTouched(true);
              setSlug(e.target.value);
            }}
            required
            pattern="[a-z0-9][a-z0-9\-]{0,62}"
            className="font-mono"
            placeholder="research"
            autoComplete="off"
            spellCheck={false}
          />
        )}
      </Field>
    </FormDialog>
  );
}

function viaText(m: WorkspaceMember, isDefault: boolean) {
  if (m.via === "idp") return "IdP group";
  if (m.via === "member") return "Added";
  return isDefault && m.role !== "admin" ? "Organization role" : "Org admin";
}

/** Members of one workspace (workspace admins): add an org member, change their role, remove them. */
function WorkspaceMembers({
  base, orgBasePath, workspaces, selected, onSelect,
}: { base: string; orgBasePath: string; workspaces: Workspace[]; selected: string; onSelect: (id: string) => void }) {
  const { user } = useSession();
  const w = workspaces.find((x) => x.id === selected)!;
  const path = `${base}/${encodeURIComponent(w.id)}/members`;
  const members = useAsync(() => get<WorkspaceMember[]>(path), [path]);
  const orgMembers = useAsync(() => (w.is_default ? Promise.resolve([] as Member[]) : get<Member[]>(`${orgBasePath}/members`)), [orgBasePath, w.is_default]);
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<WorkspaceMember | null>(null);

  const setRole = async (m: WorkspaceMember, role: Role) => {
    try {
      await api("PUT", `${path}/${encodeURIComponent(m.user_id)}`, { role });
      toast(`${m.email} is now ${roleLabel[role].toLowerCase()} in ${w.name}`);
      members.reload();
    } catch (e) {
      toast((e as Error).message, { error: true });
    }
  };
  const candidates = (orgMembers.data ?? []).filter((m) => !members.data?.some((x) => x.user_id === m.user_id));

  return (
    <>
      <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div className="grid gap-1.5">
          <label htmlFor="ws-members-of" className="text-sm font-medium">
            Workspace
          </label>
          <Select value={w.id} onValueChange={onSelect}>
            <SelectTrigger id="ws-members-of" className="w-56">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {workspaces.map((x) => (
                <SelectItem key={x.id} value={x.id}>
                  {x.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {!w.is_default && (
          <Button size="sm" onClick={() => setAdding(true)}>
            <UserPlus aria-hidden />
            Add member
          </Button>
        )}
      </div>
      {w.is_default && (
        <p className="mb-3 text-sm text-muted-foreground">
          Every organization member is in the default workspace with their organization role. Change roles on the Members page.
        </p>
      )}
      <DataState state={members} rows={3} empty={<EmptyState title="No members yet">Add organization members to give them access to this workspace.</EmptyState>}>
        {(ms) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Member</TableHead>
                  <TableHead className="w-40">Role</TableHead>
                  <TableHead>Access</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {ms.map((m) => {
                  const editable = m.via === "member" && !w.is_default;
                  return (
                    <TableRow key={m.user_id}>
                      <TableCell>
                        <span className="block font-medium">{m.name || m.email}</span>
                        {m.name && <span className="block text-xs text-muted-foreground">{m.email}</span>}
                      </TableCell>
                      <TableCell>
                        {editable ? (
                          <Select value={m.role} onValueChange={(r) => setRole(m, r as Role)}>
                            <SelectTrigger size="sm" className="w-32" aria-label={`Role of ${m.email}`}>
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {WS_ROLES.map((r) => (
                                <SelectItem key={r} value={r}>
                                  {roleLabel[r]}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        ) : (
                          roleLabel[m.role] ?? m.role
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {viaText(m, w.is_default)}
                        {m.user_id === user?.id && " · you"}
                      </TableCell>
                      <TableCell className="text-right">
                        {(m.via === "member" || m.via === "idp") && !w.is_default && (
                          <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRemoving(m)} aria-label={`Remove ${m.email} from ${w.name}`}>
                            Remove
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

      {adding && (
        <AddMemberDialog
          path={path}
          workspace={w}
          candidates={candidates}
          loading={orgMembers.loading}
          onClose={() => setAdding(false)}
          onAdded={members.reload}
        />
      )}
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={`Remove ${removing?.email ?? ""} from ${w.name}?`}
        description={
          removing?.via === "idp"
            ? "They lose access now; their IdP group adds them again on the next sign-in or SCIM sync unless the mapping changes."
            : "They lose access to this workspace's keys and resources. Keys they created stay."
        }
        confirmLabel="Remove"
        success="Member removed"
        onConfirm={async () => {
          await api("DELETE", `${path}/${encodeURIComponent(removing!.user_id)}`);
          members.reload();
        }}
      />
    </>
  );
}

function AddMemberDialog({
  path, workspace, candidates, loading, onClose, onAdded,
}: { path: string; workspace: Workspace; candidates: Member[]; loading: boolean; onClose: () => void; onAdded: () => void }) {
  const [open, setOpen] = useState(true);
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState<Role>("member");
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title={`Add a member to ${workspace.name}`}
      description="Only organization members can be added. Invite people on the Members page first."
      submitLabel="Add member"
      success="Member added"
      onSubmit={async () => {
        if (!userId) throw new Error("Choose a member.");
        await api("PUT", `${path}/${encodeURIComponent(userId)}`, { role });
        onAdded();
      }}
    >
      <Field label="Organization member" hint={!loading && candidates.length === 0 ? "Everyone in the organization is already in this workspace." : undefined}>
        {(id, hint) => (
          <Select value={userId} onValueChange={setUserId} disabled={loading || candidates.length === 0}>
            <SelectTrigger id={id} aria-describedby={hint} className="w-full">
              <SelectValue placeholder={loading ? "Loading…" : "Choose a member"} />
            </SelectTrigger>
            <SelectContent>
              {candidates.map((m) => (
                <SelectItem key={m.user_id} value={m.user_id}>
                  {m.email}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field label="Role in this workspace" hint="Admins manage its keys, providers, models, policies, presets and members. Members create keys; viewers read.">
        {(id, hint) => (
          <Select value={role} onValueChange={(v) => setRole(v as Role)}>
            <SelectTrigger id={id} aria-describedby={hint} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {WS_ROLES.map((r) => (
                <SelectItem key={r} value={r}>
                  {roleLabel[r]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>
    </FormDialog>
  );
}
