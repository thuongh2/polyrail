import { useState } from "react";
import { MoreHorizontal } from "lucide-react";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { ConfirmDialog, NoAccess } from "@/components/platform/kit";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, type User } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

type Change = { user: User; patch: { operator?: boolean; disabled?: boolean } } | null;

export default function Users() {
  const { user: me } = useSession();
  const state = useAsync(() => (me?.operator ? get<User[]>("/api/admin/users") : Promise.resolve([] as User[])), [me?.operator]);
  const [change, setChange] = useState<Change>(null);
  const [q, setQ] = useQueryParam("q");

  if (!me?.operator) {
    return (
      <>
        <PageHeader title="Users" />
        <NoAccess>Only operators manage users.</NoAccess>
      </>
    );
  }

  const p = change?.patch;
  const u = change?.user;
  const copy =
    p?.operator === true
      ? { title: `Make ${u?.email} an operator?`, body: "Operators manage every organization, public provider, model, user and MCP server on this gateway.", label: "Make operator", done: "Operator added" }
      : p?.operator === false
        ? { title: `Remove operator from ${u?.email}?`, body: "They keep their organization memberships but lose platform administration.", label: "Remove operator", done: "Operator removed" }
        : p?.disabled === true
          ? { title: `Disable ${u?.email}?`, body: "They can no longer sign in. Their API keys keep working until revoked in each organization.", label: "Disable user", done: "User disabled" }
          : { title: `Enable ${u?.email}?`, body: "They can sign in again.", label: "Enable user", done: "User enabled" };

  return (
    <>
      <PageHeader title="Users" description="Everyone with an account on this gateway. Organization roles are managed on each organization's Members page." />
      <Input
        type="search"
        aria-label="Filter users"
        placeholder="Filter by email or name"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        className="mb-4 w-full sm:w-72"
      />
      <DataState state={state} empty={<EmptyState title="No users yet">Users appear here when they sign up or sign in with SSO.</EmptyState>}>
        {(users) => {
          const needle = q.trim().toLowerCase();
          const rows = users.filter((x) => !needle || x.email.toLowerCase().includes(needle) || x.name?.toLowerCase().includes(needle));
          if (rows.length === 0)
            return <EmptyState title="No users match" action={<Button variant="outline" onClick={() => setQ("")}>Clear filter</Button>} />;
          return (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Email</TableHead>
                    <TableHead>Name</TableHead>
                    <TableHead>Access</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((x) => (
                    <TableRow key={x.id}>
                      <TableCell className="font-medium">
                        {x.email}
                        {x.id === me.id && <span className="ml-2 text-xs font-normal text-muted-foreground">(you)</span>}
                      </TableCell>
                      <TableCell>{x.name || <span className="text-muted-foreground">–</span>}</TableCell>
                      <TableCell>
                        <div className="flex gap-1.5">
                          {x.operator && <Badge variant="secondary">Operator</Badge>}
                          {x.disabled && <Badge variant="destructive">Disabled</Badge>}
                          {!x.operator && !x.disabled && <span className="text-muted-foreground">User</span>}
                        </div>
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-muted-foreground">{new Date(x.created_at).toLocaleDateString()}</TableCell>
                      <TableCell>
                        {x.id !== me.id && (
                          <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                              <Button size="icon" variant="ghost" aria-label={`Actions for ${x.email}`}>
                                <MoreHorizontal aria-hidden />
                              </Button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end">
                              <DropdownMenuItem onSelect={() => setChange({ user: x, patch: { operator: !x.operator } })}>
                                {x.operator ? "Remove operator" : "Make operator"}
                              </DropdownMenuItem>
                              <DropdownMenuItem
                                variant={x.disabled ? "default" : "destructive"}
                                onSelect={() => setChange({ user: x, patch: { disabled: !x.disabled } })}
                              >
                                {x.disabled ? "Enable user" : "Disable user"}
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          );
        }}
      </DataState>
      <ConfirmDialog
        open={!!change}
        onOpenChange={(o) => !o && setChange(null)}
        title={copy.title}
        description={copy.body}
        confirmLabel={copy.label}
        success={copy.done}
        destructive={p?.operator === true || p?.disabled === true}
        onConfirm={async () => {
          await api("PATCH", `/api/admin/users/${encodeURIComponent(change!.user.id)}`, change!.patch);
          state.reload();
        }}
      />
    </>
  );
}
