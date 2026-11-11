import { useState, type FormEvent } from "react";
import { Plus, Send, Trash2, TriangleAlert } from "lucide-react";
import { CopyButton, Field, TableFrame } from "@/components/console/bits";
import { ConfirmDialog, NoAccess } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api, get, orgBase, when, type Webhook, type WebhookEvent, type WebhookTest } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const webhookEvents: WebhookEvent[] = ["request.failed", "budget.exceeded", "key.created", "key.revoked"];

const eventHelp: Record<WebhookEvent, string> = {
  "request.failed": "A request failed on every route (at most once per model per minute).",
  "budget.exceeded": "A request was refused for org credits or a key's monthly budget.",
  "key.created": "An API key was created.",
  "key.revoked": "An API key was revoked.",
};

/** Auto-disable threshold (docs/API.md "Webhooks"). */
const MAX_FAILURES = 10;

export default function Webhooks() {
  const { org, can } = useSession();
  const admin = can("admin");
  const base = org ? orgBase(org.id) + "/webhooks" : "";
  const hooks = useAsync(() => (admin && base ? get<Webhook[]>(base) : Promise.resolve([] as Webhook[])), [base, admin]);
  const [creating, setCreating] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<Webhook | null>(null);
  const [busy, setBusy] = useState<string | null>(null); // webhook id with a request in flight

  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const description = "Signed HTTPS callbacks for gateway events. Payloads never contain prompt or completion text.";
  if (!admin)
    return (
      <>
        <PageHeader title="Webhooks" description={description} />
        <NoAccess>Only organization admins can view and manage webhooks.</NoAccess>
      </>
    );

  async function run(h: Webhook, fn: () => Promise<void>) {
    setBusy(h.id);
    try {
      await fn();
    } catch (e) {
      toast((e as Error).message, { error: true });
    } finally {
      setBusy(null);
    }
  }

  const setEnabled = (h: Webhook, enabled: boolean) =>
    run(h, async () => {
      await api("PATCH", `${base}/${h.id}`, { enabled });
      toast(enabled ? "Webhook enabled" : "Webhook disabled");
      hooks.reload();
    });

  const test = (h: Webhook) =>
    run(h, async () => {
      const r = await api<WebhookTest>("POST", `${base}/${h.id}/test`);
      if (r.ok) toast(`Test delivered (HTTP ${r.status})`);
      else toast(r.error ?? `Receiver answered HTTP ${r.status}`, { error: true });
    });

  const add = (
    <Button size="sm" onClick={() => setCreating(true)}>
      <Plus aria-hidden /> Add webhook
    </Button>
  );

  return (
    <>
      <PageHeader title="Webhooks" description={description} actions={add} />
      <DataState
        state={hooks}
        rows={3}
        empty={
          <EmptyState title="No webhooks yet" action={add}>
            Add an endpoint to be notified when requests fail, budgets run out or API keys change.
          </EmptyState>
        }
      >
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Events</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Failures</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((h) => {
                  const autoOff = !h.enabled && h.failures >= MAX_FAILURES;
                  return (
                    <TableRow key={h.id}>
                      <TableCell className="max-w-72">
                        <span className="block truncate font-mono text-xs" title={h.url}>
                          {h.url}
                        </span>
                        <span className="text-xs text-muted-foreground">Added {when(h.created_at)}</span>
                      </TableCell>
                      <TableCell>
                        <div className="flex max-w-64 flex-wrap gap-1">
                          {h.events.map((e) => (
                            <Badge key={e} variant="outline" className="font-mono text-xs font-normal">
                              {e === "*" ? "All events" : e}
                            </Badge>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <Badge
                          variant="outline"
                          className={!h.enabled ? (autoOff ? "text-danger" : "text-muted-foreground") : h.failures ? "border-warning/60 text-foreground" : "text-success"}
                        >
                          {!h.enabled ? (autoOff ? "Disabled after failures" : "Disabled") : h.failures ? "Failing" : "Active"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{h.failures}</TableCell>
                      <TableCell>
                        <Switch
                          checked={h.enabled}
                          disabled={busy === h.id}
                          onCheckedChange={(c) => setEnabled(h, c)}
                          aria-label={`${h.enabled ? "Disable" : "Enable"} webhook ${h.url}`}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="sm" disabled={busy === h.id} onClick={() => test(h)} aria-label={`Send test event to ${h.url}`}>
                            <Send aria-hidden /> Send test
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setDeleting(h)} aria-label={`Delete webhook ${h.url}`}>
                            <Trash2 aria-hidden />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
      <p className="mt-3 text-xs text-muted-foreground">
        Deliveries are signed with X-GW-Signature (HMAC-SHA256 of timestamp and body), retried 3 times on network errors and 5xx, and the
        webhook is disabled after {MAX_FAILURES} failures in a row. Re-enabling resets the count.
      </p>

      <CreateWebhookDialog
        open={creating}
        onOpenChange={setCreating}
        base={base}
        onCreated={(s) => {
          setCreating(false);
          setSecret(s);
          hooks.reload();
        }}
      />

      <Dialog open={!!secret} onOpenChange={(o) => !o && setSecret(null)}>
        <DialogContent onInteractOutside={(e) => e.preventDefault()}>
          <DialogHeader>
            <DialogTitle>Copy the signing secret</DialogTitle>
            <DialogDescription>Use it to verify the X-GW-Signature header of every delivery.</DialogDescription>
          </DialogHeader>
          <div className="flex items-start gap-2 rounded-md border border-warning/50 bg-warning/10 p-3 text-sm">
            <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden />
            <p>This is the only time the secret is shown. If you lose it, delete the webhook and add it again.</p>
          </div>
          <div className="flex items-center gap-2">
            <Label htmlFor="wh-secret" className="sr-only">
              Signing secret
            </Label>
            <Input id="wh-secret" readOnly value={secret ?? ""} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
            <CopyButton text={secret ?? ""} />
          </div>
          <DialogFooter>
            <Button onClick={() => setSecret(null)}>I have saved it</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete this webhook?"
        description={
          <>
            <span className="font-mono break-all">{deleting?.url}</span> stops receiving events immediately. Its signing secret can't be recovered.
          </>
        }
        confirmLabel="Delete webhook"
        success="Webhook deleted"
        onConfirm={async () => {
          await api("DELETE", `${base}/${deleting!.id}`);
          hooks.reload();
        }}
      />
    </>
  );
}

function CreateWebhookDialog({
  open, onOpenChange, base, onCreated,
}: { open: boolean; onOpenChange: (o: boolean) => void; base: string; onCreated: (secret: string) => void }) {
  const [url, setUrl] = useState("");
  const [all, setAll] = useState(false);
  const [events, setEvents] = useState<string[]>(["request.failed", "budget.exceeded"]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const reset = () => (setUrl(""), setAll(false), setEvents(["request.failed", "budget.exceeded"]), setErr(""));

  async function submit(e: FormEvent) {
    e.preventDefault();
    const picked = all ? ["*"] : events;
    if (picked.length === 0) return setErr("Choose at least one event.");
    setBusy(true);
    setErr("");
    try {
      const r = await api<{ webhook: Webhook; secret: string }>("POST", base, { url: url.trim(), events: picked });
      reset();
      onCreated(r.secret);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => (o || reset(), onOpenChange(o))}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto scrollbar-thin sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add webhook</DialogTitle>
          <DialogDescription>The endpoint must use HTTPS on a public address. At most 20 webhooks per organization.</DialogDescription>
        </DialogHeader>
        <form id="create-webhook" onSubmit={submit} className="grid gap-4">
          <Field id="wh-url" label="Endpoint URL">
            <Input
              id="wh-url"
              type="url"
              required
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="https://example.com/hooks/gateway"
              className="font-mono text-sm"
              spellCheck={false}
              autoComplete="off"
              autoFocus
            />
          </Field>
          <fieldset className="grid gap-2">
            <legend className="mb-1.5 text-sm font-medium">Events</legend>
            <label className="flex items-start gap-2 text-sm">
              <Checkbox className="mt-0.5" checked={all} onCheckedChange={(c) => setAll(c === true)} />
              <span>
                All events
                <span className="block text-xs text-muted-foreground">Includes event types added in future versions.</span>
              </span>
            </label>
            {webhookEvents.map((ev) => (
              <label key={ev} className="flex items-start gap-2 text-sm">
                <Checkbox
                  className="mt-0.5"
                  disabled={all}
                  checked={all || events.includes(ev)}
                  onCheckedChange={(c) => setEvents((es) => (c ? [...es, ev] : es.filter((x) => x !== ev)))}
                />
                <span>
                  <span className="font-mono text-xs">{ev}</span>
                  <span className="block text-xs text-muted-foreground">{eventHelp[ev]}</span>
                </span>
              </label>
            ))}
          </fieldset>
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
          <Button type="submit" form="create-webhook" disabled={busy}>
            {busy ? "Adding…" : "Add webhook"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
