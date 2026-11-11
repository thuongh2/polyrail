import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { Link } from "wouter";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { ConfirmDialog, EnabledText, Field, FormDialog, NoAccess, splitList } from "@/components/platform/kit";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, type MCPClient, type MCPServer } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

type MCPList = { servers: MCPServer[]; connected: MCPClient[] | null };
const BASE = "/api/admin/mcp";

export default function MCP() {
  const { user } = useSession();
  const state = useAsync(
    () => (user?.operator ? get<MCPList>(BASE) : Promise.resolve({ servers: [], connected: [] } as MCPList)),
    [user?.operator],
  );
  const [editing, setEditing] = useState<MCPServer | "new" | null>(null);
  const [deleting, setDeleting] = useState<MCPServer | null>(null);

  if (!user?.operator) {
    return (
      <>
        <PageHeader title="MCP servers" />
        <NoAccess>Only operators manage MCP servers.</NoAccess>
      </>
    );
  }

  const add = (
    <Button onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add MCP server
    </Button>
  );

  return (
    <>
      <PageHeader
        title="MCP servers"
        description="Tool servers the gateway connects to. Their tools can be offered to models on chat requests from organizations you grant access."
        actions={add}
      />
      <DataState
        state={state}
        isEmpty={(d) => d.servers.length === 0}
        empty={
          <EmptyState title="No MCP servers yet" action={add}>
            Register an HTTP or SSE MCP server; the gateway connects to it and discovers its tools.
          </EmptyState>
        }
      >
        {({ servers, connected }) => (
          <div className="rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Transport</TableHead>
                  <TableHead>URL</TableHead>
                  <TableHead>Allowed tools</TableHead>
                  <TableHead>Connection</TableHead>
                  <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {[...servers].sort((a, b) => a.name.localeCompare(b.name)).map((s) => {
                  const c = connected?.find((x) => x.config.id === s.id);
                  return (
                    <TableRow key={s.id}>
                      <TableCell className="font-mono font-medium">{s.name}</TableCell>
                      <TableCell>{s.type === "sse" ? "SSE" : "HTTP"}</TableCell>
                      <TableCell className="max-w-64 truncate font-mono text-xs text-muted-foreground" title={s.url}>{s.url}</TableCell>
                      <TableCell className="font-mono text-xs">{!s.tools?.length || s.tools.includes("*") ? "All" : s.tools.join(", ")}</TableCell>
                      <TableCell className="max-w-80 text-xs">
                        <Connection server={s} client={c} />
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <Button size="icon" variant="ghost" aria-label={`Edit ${s.name}`} onClick={() => setEditing(s)}>
                            <Pencil aria-hidden />
                          </Button>
                          <Button size="icon" variant="ghost" aria-label={`Delete ${s.name}`} onClick={() => setDeleting(s)}>
                            <Trash2 aria-hidden />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </DataState>

      <section className="mt-8 max-w-3xl space-y-3 text-sm" aria-labelledby="mcp-usage">
        <h2 id="mcp-usage" className="text-base font-semibold">Using MCP tools</h2>
        <ol className="list-decimal space-y-2 pl-5 text-muted-foreground">
          <li>
            Grant an organization access by server name on the{" "}
            <Link href="/platform/orgs" className="text-foreground underline underline-offset-4">Organizations</Link> page. Without a grant its keys see no tools.
          </li>
          <li>
            On a chat request, opt in with the header{" "}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs text-foreground">x-gateway-mcp-clients: name1,name2</code> (or{" "}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs text-foreground">*</code> for every granted server). The servers' tools are added to the request.
          </li>
          <li>
            When the model returns a tool call, send that one <code className="font-mono text-xs text-foreground">tool_calls[]</code> item to{" "}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs text-foreground">POST /v1/mcp/tool/execute</code> with the same API key, then append the returned tool message and continue the chat. Tool calls are rate limited and logged as <span className="font-mono text-xs text-foreground">mcp/&lt;server&gt;</span>.
          </li>
        </ol>
      </section>

      {editing && (
        <MCPDialog
          key={editing === "new" ? "new" : editing.id}
          server={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={state.reload}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "MCP server"}?`}
        description="The gateway disconnects from it and its tools stop being offered. Organization grants that name it no longer match anything."
        confirmLabel="Delete server"
        success="MCP server deleted"
        onConfirm={async () => {
          await api("DELETE", `${BASE}/${encodeURIComponent(deleting!.id)}`);
          state.reload();
        }}
      />
    </>
  );
}

function Connection({ server, client }: { server: MCPServer; client?: MCPClient }) {
  if (!server.enabled) return <EnabledText on={false} />;
  if (!client) return <span className="text-warning">Not connected</span>;
  const ok = client.state === "connected";
  return (
    <div>
      <span className={ok ? "text-success" : client.state === "error" ? "text-destructive" : "text-warning"}>{ok ? "Connected" : client.state === "error" ? "Error" : client.state === "disconnected" ? "Disconnected" : client.state || "Unknown"}</span>
      {client.tools.length > 0 ? (
        <p className="mt-0.5 font-mono text-muted-foreground" title={client.tools.map((t) => t.name).join(", ")}>
          {client.tools.length} tool{client.tools.length === 1 ? "" : "s"}: {client.tools.map((t) => t.name).join(", ")}
        </p>
      ) : (
        <p className="mt-0.5 text-muted-foreground">No tools discovered</p>
      )}
    </div>
  );
}

function MCPDialog({ server, onClose, onSaved }: { server?: MCPServer; onClose: () => void; onSaved: () => void }) {
  const [open, setOpen] = useState(true);
  const [name, setName] = useState(server?.name ?? "");
  const [type, setType] = useState<MCPServer["type"]>(server?.type ?? "http");
  const [url, setUrl] = useState(server?.url ?? "");
  const [tools, setTools] = useState((server?.tools?.length ? server.tools : ["*"]).join(", "));
  const [enabled, setEnabled] = useState(server?.enabled ?? true);
  return (
    <FormDialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title={server ? `Edit ${server.name}` : "Add MCP server"}
      description="The gateway connects when the server is saved and enabled."
      submitLabel={server ? "Save changes" : "Add server"}
      success={server ? "MCP server saved" : "MCP server added"}
      onSubmit={async () => {
        if (!/^[A-Za-z0-9_-]+$/.test(name)) throw new Error("Name may use letters, digits, - and _ only.");
        const list = splitList(tools);
        const body = { name, type, url: url.trim(), tools: list.length ? list : ["*"], enabled };
        if (server) await api("PUT", `${BASE}/${encodeURIComponent(server.id)}`, body);
        else await api("POST", BASE, body);
        onSaved();
      }}
    >
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,9rem)]">
        <Field label="Name" hint="Letters, digits, - and _. Organizations are granted access by this name.">
          {(id, hint) => (
            <Input
              id={id}
              aria-describedby={hint}
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              pattern="[A-Za-z0-9_\-]+"
              placeholder="search"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          )}
        </Field>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="mcp-type">Transport</Label>
          <Select value={type} onValueChange={(v) => setType(v as MCPServer["type"])}>
            <SelectTrigger id="mcp-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="http">HTTP</SelectItem>
              <SelectItem value="sse">SSE</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <Field label="URL">
        {(id) => <Input id={id} type="url" value={url} onChange={(e) => setUrl(e.target.value)} required placeholder="https://mcp.example.com/mcp" autoComplete="off" />}
      </Field>
      <Field label="Allowed tools" hint={<>Comma-separated tool names, or <span className="font-mono">*</span> for every tool the server offers.</>}>
        {(id, hint) => <Input id={id} aria-describedby={hint} value={tools} onChange={(e) => setTools(e.target.value)} className="font-mono" spellCheck={false} />}
      </Field>
      <div className="flex items-center gap-3">
        <Switch id="mcp-enabled" checked={enabled} onCheckedChange={setEnabled} />
        <Label htmlFor="mcp-enabled">Enabled</Label>
      </div>
    </FormDialog>
  );
}
