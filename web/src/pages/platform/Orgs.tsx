import { useState } from "react";
import { MoreHorizontal, Plus } from "lucide-react";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { Field, FormDialog, NoAccess, splitList } from "@/components/platform/kit";
import { toast } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, orgBase, usd, type Org } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";

type Action = { kind: "credits" | "mcp"; org: Org } | { kind: "create" } | null;

export default function Orgs() {
  const { user, refresh } = useSession();
  const state = useAsync(() => (user?.operator ? get<Org[]>("/api/orgs?limit=500") : Promise.resolve([] as Org[])), [user?.operator]);
  const [action, setAction] = useState<Action>(null);
  const [q, setQ] = useQueryParam("q");
  const close = () => setAction(null);

  if (!user?.operator) {
    return (
      <>
        <PageHeader title="Organizations" />
        <NoAccess>Only operators manage organizations.</NoAccess>
      </>
    );
  }

  const togglePrepaid = async (o: Org) => {
    try {
      await api("PATCH", orgBase(o.id), { prepaid: !o.prepaid });
      toast(o.prepaid ? `${o.name} is now unlimited` : `${o.name} is now prepaid`);
      state.reload();
    } catch (e) {
      toast((e as Error).message, { error: true });
    }
  };

  const create = (
    <Button onClick={() => setAction({ kind: "create" })}>
      <Plus aria-hidden /> Create organization
    </Button>
  );

  return (
    <>
      <PageHeader
        title="Organizations"
        description="Every tenant on this gateway. Prepaid organizations stop at zero balance; unlimited ones are billed outside the gateway."
        actions={create}
      />
      <Input
        type="search"
        aria-label="Filter organizations"
        placeholder="Filter by name or id"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        className="mb-4 w-full sm:w-72"
      />
      <DataState state={state} empty={<EmptyState title="No organizations yet" action={create} />}>
        {(orgs) => {
          const needle = q.trim().toLowerCase();
          const rows = orgs.filter((o) => !needle || o.name.toLowerCase().includes(needle) || o.id.includes(needle));
          if (rows.length === 0)
            return <EmptyState title="No organizations match" action={<Button variant="outline" onClick={() => setQ("")}>Clear filter</Button>} />;
          return (
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Organization</TableHead>
                    <TableHead>Billing</TableHead>
                    <TableHead className="text-right">Credits granted</TableHead>
                    <TableHead>MCP access</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((o) => (
                    <TableRow key={o.id}>
                      <TableCell>
                        <div className="font-medium">{o.name}</div>
                        <div className="font-mono text-xs text-muted-foreground">{o.id}</div>
                      </TableCell>
                      <TableCell>{o.prepaid ? "Prepaid" : "Unlimited"}</TableCell>
                      <TableCell className="text-right tabular-nums">{usd(o.credits_micros)}</TableCell>
                      <TableCell className="max-w-56 truncate text-xs">
                        {!o.mcp_servers?.length ? (
                          <span className="text-muted-foreground">None</span>
                        ) : o.mcp_servers.includes("*") ? (
                          "All servers"
                        ) : (
                          <span className="font-mono">{o.mcp_servers.join(", ")}</span>
                        )}
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-muted-foreground">{new Date(o.created_at).toLocaleDateString()}</TableCell>
                      <TableCell>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button size="icon" variant="ghost" aria-label={`Actions for ${o.name}`}>
                              <MoreHorizontal aria-hidden />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem onSelect={() => setAction({ kind: "credits", org: o })}>Add credits</DropdownMenuItem>
                            <DropdownMenuItem onSelect={() => togglePrepaid(o)}>
                              {o.prepaid ? "Switch to unlimited" : "Switch to prepaid"}
                            </DropdownMenuItem>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem onSelect={() => setAction({ kind: "mcp", org: o })}>MCP access</DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          );
        }}
      </DataState>

      {action?.kind === "credits" && <CreditsDialog key={action.org.id} org={action.org} onClose={close} onSaved={state.reload} />}
      {action?.kind === "mcp" && <MCPAccessDialog key={action.org.id} org={action.org} onClose={close} onSaved={state.reload} />}
      {action?.kind === "create" && (
        <CreateDialog
          onClose={close}
          onSaved={() => {
            state.reload();
            refresh().catch(() => {}); // the operator may have made themselves owner
          }}
        />
      )}
    </>
  );
}

function useOpen(onClose: () => void) {
  const [open, setOpen] = useState(true);
  return {
    open,
    onOpenChange: (o: boolean) => {
      setOpen(o);
      if (!o) onClose();
    },
  };
}

function CreditsDialog({ org, onClose, onSaved }: { org: Org; onClose: () => void; onSaved: () => void }) {
  const dlg = useOpen(onClose);
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");
  return (
    <FormDialog
      {...dlg}
      title={`Add credits to ${org.name}`}
      description={`Granted so far: ${usd(org.credits_micros)}. A negative amount removes credits.${org.prepaid ? "" : " This organization is unlimited, so credits only matter once it is switched to prepaid."}`}
      submitLabel="Add credits"
      success="Credits updated"
      onSubmit={async () => {
        const v = Number(amount);
        if (!amount.trim() || !Number.isFinite(v) || v === 0) throw new Error("Enter a non-zero amount in USD.");
        await api("POST", `${orgBase(org.id)}/credits`, { usd: v, note: note.trim() });
        onSaved();
      }}
    >
      <Field label="Amount (USD)" hint="For example 50 or -10.">
        {(id, hint) => (
          <Input id={id} aria-describedby={hint} type="number" step="0.01" value={amount} onChange={(e) => setAmount(e.target.value)} required autoFocus />
        )}
      </Field>
      <Field label="Note" hint="Optional. Recorded in the audit log.">
        {(id, hint) => <Input id={id} aria-describedby={hint} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Invoice 2026-114" />}
      </Field>
    </FormDialog>
  );
}

function MCPAccessDialog({ org, onClose, onSaved }: { org: Org; onClose: () => void; onSaved: () => void }) {
  const dlg = useOpen(onClose);
  const [value, setValue] = useState((org.mcp_servers ?? []).join(", "));
  return (
    <FormDialog
      {...dlg}
      title={`MCP access for ${org.name}`}
      description="The MCP servers this organization's API keys may use. Empty means none."
      submitLabel="Save access"
      success="MCP access saved"
      onSubmit={async () => {
        await api("PATCH", orgBase(org.id), { mcp_servers: splitList(value) });
        onSaved();
      }}
    >
      <Field label="Server names" hint={<>Comma-separated names from the MCP servers page, or <span className="font-mono">*</span> for all.</>}>
        {(id, hint) => (
          <Input id={id} aria-describedby={hint} value={value} onChange={(e) => setValue(e.target.value)} placeholder="search, files" className="font-mono" autoFocus spellCheck={false} />
        )}
      </Field>
    </FormDialog>
  );
}

function CreateDialog({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const dlg = useOpen(onClose);
  const [name, setName] = useState("");
  const [owner, setOwner] = useState("");
  const [prepaid, setPrepaid] = useState(true);
  return (
    <FormDialog
      {...dlg}
      title="Create organization"
      submitLabel="Create organization"
      success="Organization created"
      onSubmit={async () => {
        if (!name.trim()) throw new Error("Enter a name.");
        await api("POST", "/api/orgs", { name: name.trim(), owner_email: owner.trim(), prepaid });
        onSaved();
      }}
    >
      <Field label="Name">
        {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} required autoFocus />}
      </Field>
      <Field label="Owner email" hint="Optional. Must belong to an existing user, who becomes the owner.">
        {(id, hint) => <Input id={id} aria-describedby={hint} type="email" value={owner} onChange={(e) => setOwner(e.target.value)} autoComplete="off" />}
      </Field>
      <div className="flex items-start gap-3">
        <Switch id="org-prepaid" checked={prepaid} onCheckedChange={setPrepaid} className="mt-0.5" />
        <div>
          <Label htmlFor="org-prepaid">Prepaid</Label>
          <p className="text-xs text-muted-foreground">Requests stop when credits run out. Turn off for unlimited billing.</p>
        </div>
      </div>
    </FormDialog>
  );
}
