import { useState, type FormEvent } from "react";
import { Pencil, Plus, Trash2, X } from "lucide-react";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { ConfirmDialog, Field, FormDialog, NoAccess } from "@/components/platform/kit";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, type ProxyHealth, type ProxyMember, type ProxyPool, type ProxyStrategy, type ProxyTestResult } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const BASE = "/api/admin/proxy-pools";

const strategies: { value: ProxyStrategy; label: string; hint: string }[] = [
  { value: "round-robin", label: "Round robin", hint: "Each new connection takes the next member." },
  { value: "random", label: "Random", hint: "Each new connection takes a random member." },
  { value: "sticky", label: "Sticky", hint: "Keep one member for a time window, then move on." },
  { value: "latency", label: "Lowest latency", hint: "Prefer the member with the fastest measured connect time." },
];
const strategyLabel = (s: ProxyStrategy) => strategies.find((x) => x.value === s)?.label ?? s;

export default function Proxies() {
  const { user } = useSession();
  const state = useAsync(() => (user?.operator ? get<ProxyPool[]>(BASE) : Promise.resolve([] as ProxyPool[])), [user?.operator]);
  const [editing, setEditing] = useState<ProxyPool | "new" | null>(null);
  const [deleting, setDeleting] = useState<ProxyPool | null>(null);
  const [testing, setTesting] = useState<ProxyPool | null>(null);

  if (!user?.operator) {
    return (
      <>
        <PageHeader title="Proxies" />
        <NoAccess>Only operators manage proxy pools.</NoAccess>
      </>
    );
  }

  const add = (
    <Button onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add proxy pool
    </Button>
  );

  return (
    <>
      <PageHeader
        title="Proxies"
        description="Pools of outbound proxies for upstream model calls. Assign a pool to a platform provider, or make one the default for every provider with UPSTREAM_PROXY_POOL."
        actions={add}
      />
      <DataState
        state={state}
        isEmpty={(d) => d.length === 0}
        empty={
          <EmptyState title="No proxy pools yet" action={add}>
            A pool rotates upstream connections across its proxies and sets aside the ones that stop answering.
          </EmptyState>
        }
      >
        {(pools) => (
          <div className="rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Strategy</TableHead>
                  <TableHead>When all fail</TableHead>
                  <TableHead>Members</TableHead>
                  <TableHead className="w-0"><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {[...pools].sort((a, b) => a.name.localeCompare(b.name)).map((p) => (
                  <TableRow key={p.id}>
                    <TableCell className="align-top">
                      <span className="font-medium">{p.name}</span>
                      <p className="font-mono text-xs text-muted-foreground">{p.id}</p>
                    </TableCell>
                    <TableCell className="align-top text-sm">
                      {strategyLabel(p.strategy)}
                      {p.strategy === "sticky" && <span className="text-muted-foreground">, {p.sticky_minutes} min</span>}
                    </TableCell>
                    <TableCell className="align-top text-sm">{p.fail_closed ? "Refuse the call" : "Connect directly"}</TableCell>
                    <TableCell className="align-top">
                      <ul className="grid gap-1 text-xs">
                        {p.members.map((m) => (
                          <li key={m.id} className="flex flex-wrap items-baseline gap-x-2">
                            <span className="font-mono" title={m.url}>{m.label || hostOf(m.url)}</span>
                            {m.enabled ? <HealthText h={m.health} /> : <span className="text-muted-foreground">Disabled</span>}
                          </li>
                        ))}
                      </ul>
                    </TableCell>
                    <TableCell className="align-top">
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="outline" onClick={() => setTesting(p)}>Test</Button>
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
      <p className="mt-3 max-w-3xl text-xs text-muted-foreground">
        Health is this server's view: a member that fails a connection is set aside for 60 seconds, doubling up to 10 minutes;
        three failures in a row mark it down until a health probe reaches it. Provider errors such as rate limits never change the selection.
      </p>

      {editing && (
        <PoolDialog
          key={editing === "new" ? "new" : editing.id}
          pool={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={state.reload}
        />
      )}
      {testing && <TestDialog pool={testing} onClose={() => { setTesting(null); state.reload(); }} />}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "proxy pool"}?`}
        description="A pool that a provider uses cannot be deleted; move those providers off it first."
        confirmLabel="Delete pool"
        success="Proxy pool deleted"
        onConfirm={async () => {
          await api("DELETE", `${BASE}/${encodeURIComponent(deleting!.id)}`);
          state.reload();
        }}
      />
    </>
  );
}

function hostOf(url: string) {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

function HealthText({ h }: { h?: ProxyHealth }) {
  const lat = h?.latency_ms ? ` · ${h.latency_ms} ms` : "";
  switch (h?.state) {
    case "up":
      return <span className="text-success">Up{lat}</span>;
    case "down":
      return <span className="text-destructive">Down</span>;
    case "set_aside":
      return <span>Set aside{h.set_aside_until ? ` until ${new Date(h.set_aside_until).toLocaleTimeString()}` : ""}</span>;
    default:
      return <span className="text-muted-foreground">Not checked yet</span>;
  }
}

type MemberForm = { id?: string; url: string; username: string; password: string; ca: string; label: string; enabled: boolean; stored?: ProxyMember };

const toForm = (m: ProxyMember): MemberForm => ({
  id: m.id, url: m.url, username: m.username ?? "", password: "", ca: m.ca_cert_pem ?? "", label: m.label ?? "", enabled: m.enabled, stored: m,
});
const blank = (): MemberForm => ({ url: "", username: "", password: "", ca: "", label: "", enabled: true });

function PoolDialog({ pool, onClose, onSaved }: { pool?: ProxyPool; onClose: () => void; onSaved: () => void }) {
  const [open, setOpen] = useState(true);
  const [name, setName] = useState(pool?.name ?? "");
  const [strategy, setStrategy] = useState<ProxyStrategy>(pool?.strategy ?? "round-robin");
  const [sticky, setSticky] = useState(String(pool?.sticky_minutes ?? 30));
  const [failClosed, setFailClosed] = useState(pool?.fail_closed ?? true);
  const [members, setMembers] = useState<MemberForm[]>(pool ? pool.members.map(toForm) : [blank()]);
  const update = (i: number, patch: Partial<MemberForm>) => setMembers((ms) => ms.map((m, j) => (j === i ? { ...m, ...patch } : m)));

  return (
    <FormDialog
      wide
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) onClose();
      }}
      title={pool ? `Edit ${pool.name}` : "Add proxy pool"}
      description="Passwords are encrypted and never shown again. Changes apply to new connections within seconds."
      submitLabel={pool ? "Save changes" : "Add pool"}
      success={pool ? "Proxy pool saved" : "Proxy pool added"}
      onSubmit={async () => {
        if (!members.length) throw new Error("Add at least one member.");
        const body = {
          name: name.trim(),
          strategy,
          sticky_minutes: Number(sticky) || 30,
          fail_closed: failClosed,
          members: members.map((m) => ({
            id: m.id,
            url: m.url.trim(),
            username: m.username.trim() || undefined,
            password: m.password || undefined,
            ca_cert_pem: m.ca.trim() || undefined,
            label: m.label.trim() || undefined,
            enabled: m.enabled,
          })),
        };
        if (pool) await api("PUT", `${BASE}/${encodeURIComponent(pool.id)}`, body);
        else await api("POST", BASE, body);
        onSaved();
      }}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name" hint="UPSTREAM_PROXY_POOL may name the pool by this name or its id.">
          {(id, hint) => <Input id={id} aria-describedby={hint} value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} placeholder="egress-eu" autoComplete="off" />}
        </Field>
        <div className="grid content-start gap-1.5">
          <Label htmlFor="pool-strategy">Strategy</Label>
          <Select value={strategy} onValueChange={(v) => setStrategy(v as ProxyStrategy)}>
            <SelectTrigger id="pool-strategy" className="w-full" aria-describedby="pool-strategy-hint">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {strategies.map((s) => (
                <SelectItem key={s.value} value={s.value}>{s.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p id="pool-strategy-hint" className="text-xs text-muted-foreground">{strategies.find((s) => s.value === strategy)?.hint}</p>
        </div>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        {strategy === "sticky" && (
          <Field label="Sticky window (minutes)" hint="1 to 1440.">
            {(id, hint) => <Input id={id} aria-describedby={hint} type="number" min={1} max={1440} value={sticky} onChange={(e) => setSticky(e.target.value)} required />}
          </Field>
        )}
        <div className="grid content-start gap-1.5">
          <div className="flex items-center gap-3 sm:pt-6">
            <Switch id="pool-fail-closed" checked={failClosed} onCheckedChange={setFailClosed} aria-describedby="pool-fail-closed-hint" />
            <Label htmlFor="pool-fail-closed">Refuse calls when every member is unusable</Label>
          </div>
          <p id="pool-fail-closed-hint" className="text-xs text-muted-foreground">
            {failClosed ? "No traffic leaves without a proxy; routing fails over to the model's next target." : "Calls connect directly while no member is usable."}
          </p>
        </div>
      </div>

      <fieldset className="grid gap-3">
        <legend className="mb-2 text-sm font-medium">Members</legend>
        {members.map((m, i) => {
          const keeps = !!m.stored?.username && m.stored.url === m.url.trim() && m.stored.username === m.username.trim();
          return (
            <div key={m.id ?? `new-${i}`} className="grid gap-3 rounded-md border p-3">
              <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,10rem)_auto]">
                <Field label={`Proxy URL ${i + 1}`} hint="http://, https:// or socks5://host:port">
                  {(id, hint) => (
                    <Input id={id} aria-describedby={hint} value={m.url} onChange={(e) => update(i, { url: e.target.value })} required placeholder="http://proxy-1.internal:3128" inputMode="url" autoComplete="off" spellCheck={false} className="font-mono text-xs" />
                  )}
                </Field>
                <Field label="Label">
                  {(id) => <Input id={id} value={m.label} onChange={(e) => update(i, { label: e.target.value })} maxLength={100} placeholder="dc-1" autoComplete="off" />}
                </Field>
                <div className="flex items-end justify-end pb-0.5">
                  <Button type="button" size="icon" variant="ghost" aria-label={`Remove member ${i + 1}`} onClick={() => setMembers((ms) => ms.filter((_, j) => j !== i))} disabled={members.length === 1}>
                    <X aria-hidden />
                  </Button>
                </div>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="Username">
                  {(id) => <Input id={id} value={m.username} onChange={(e) => update(i, { username: e.target.value })} autoComplete="off" spellCheck={false} />}
                </Field>
                <Field label="Password" hint={keeps ? "Leave blank to keep the stored password." : undefined}>
                  {(id, hint) => (
                    <Input
                      id={id}
                      aria-describedby={hint}
                      type="password"
                      value={m.password}
                      onChange={(e) => update(i, { password: e.target.value })}
                      required={!!m.username.trim() && !keeps}
                      placeholder={keeps ? "Leave blank to keep" : undefined}
                      autoComplete="new-password"
                      spellCheck={false}
                    />
                  )}
                </Field>
              </div>
              <details className="rounded-md border px-3 py-2" open={!!m.ca}>
                <summary className="cursor-pointer rounded-sm text-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none">CA certificate</summary>
                <Field label="PEM" className="mt-2" hint="For an https:// proxy signed by a private CA.">
                  {(id, hint) => (
                    <Textarea id={id} aria-describedby={hint} value={m.ca} onChange={(e) => update(i, { ca: e.target.value })} rows={3} className="font-mono text-xs" placeholder="-----BEGIN CERTIFICATE-----" autoComplete="off" spellCheck={false} />
                  )}
                </Field>
              </details>
              <div className="flex items-center gap-3">
                <Switch id={`member-enabled-${i}`} checked={m.enabled} onCheckedChange={(v) => update(i, { enabled: v })} />
                <Label htmlFor={`member-enabled-${i}`}>Enabled</Label>
              </div>
            </div>
          );
        })}
        <div>
          <Button type="button" variant="outline" size="sm" onClick={() => setMembers((ms) => [...ms, blank()])} disabled={members.length >= 100}>
            <Plus aria-hidden /> Add member
          </Button>
        </div>
      </fieldset>
    </FormDialog>
  );
}

function TestDialog({ pool, onClose }: { pool: ProxyPool; onClose: () => void }) {
  const [target, setTarget] = useState("api.openai.com:443");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [results, setResults] = useState<ProxyTestResult[]>();
  const run = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const r = await api<{ results: ProxyTestResult[] }>("POST", `${BASE}/${encodeURIComponent(pool.id)}/test`, { target: target.trim() });
      setResults(r.results);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const name = (id: string) => {
    const m = pool.members.find((x) => x.id === id);
    return m?.label || (m ? hostOf(m.url) : id);
  };
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto scrollbar-thin sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Test {pool.name}</DialogTitle>
          <DialogDescription>Opens a tunnel to the target through each member and closes it. Nothing is sent through the tunnel.</DialogDescription>
        </DialogHeader>
        <form onSubmit={run} className="flex flex-wrap items-end gap-3">
          <Field label="Target" className="min-w-0 flex-1" hint="host:port">
            {(id, hint) => <Input id={id} aria-describedby={hint} value={target} onChange={(e) => setTarget(e.target.value)} required className="font-mono text-xs" autoComplete="off" spellCheck={false} />}
          </Field>
          <Button type="submit" disabled={busy} className="mb-5">{busy ? "Testing…" : "Run test"}</Button>
        </form>
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        {results && (
          <div className="rounded-lg border" aria-live="polite">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Member</TableHead>
                  <TableHead>Result</TableHead>
                  <TableHead className="text-right">Connect</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {results.map((r) => (
                  <TableRow key={r.member_id}>
                    <TableCell className="font-mono text-xs">{name(r.member_id)}</TableCell>
                    <TableCell className="max-w-80 text-xs">
                      {r.ok ? <Badge variant="outline" className="text-success">Reachable</Badge> : <span className="text-destructive">{r.error || "Failed"}</span>}
                    </TableCell>
                    <TableCell className="text-right tabular-nums text-xs">{r.latency_ms} ms</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
