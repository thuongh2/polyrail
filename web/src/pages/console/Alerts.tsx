// Alert channels (docs/KEYS.md "Alert channels"): one shoutrrr URL per channel, org or platform scope.
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Bell, ExternalLink, Globe, Mail, MessageSquare, Pencil, Plus, Send, Trash2 } from "lucide-react";
import type { Scope } from "@/App";
import { TableFrame } from "@/components/console/bits";
import { ConfirmDialog, Field, FormError, NoAccess, splitList } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { alertsApi, orgBase, PLATFORM, when, type AlertChannel, type AlertEvent, type AlertInput } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

/** Auto-disable threshold (docs/KEYS.md). */
const MAX_FAILURES = 10;
const DOCS_URL = "https://shoutrrr.nickfedor.com/";
const README_URL = "https://github.com/nicholas-fedor/shoutrrr#readme";

const alertEvents: [AlertEvent, string][] = [
  ["key.quarantined", "A provider key was quarantined after 3 consecutive auth failures."],
  ["key.recovered", "A quarantined key passed a test or was re-enabled."],
  ["provider.unavailable", "A provider has no available key."],
  ["budget.exceeded", "A request was refused for credits or a key's monthly budget."],
  ["request.failed", "A request failed on every route."],
];

/** Service names as the API reports them (the URL scheme) → label and icon. */
const services: Record<string, [string, typeof Bell]> = {
  telegram: ["Telegram", Send],
  discord: ["Discord", MessageSquare],
  slack: ["Slack", MessageSquare],
  teams: ["Microsoft Teams", MessageSquare],
  googlechat: ["Google Chat", MessageSquare],
  matrix: ["Matrix", MessageSquare],
  mattermost: ["Mattermost", MessageSquare],
  smtp: ["Email (SMTP)", Mail],
  ntfy: ["ntfy", Bell],
  gotify: ["Gotify", Bell],
  pushover: ["Pushover", Bell],
  generic: ["Generic webhook", Globe],
};
const serviceOf = (s?: string) => services[(s ?? "").toLowerCase()] ?? [s || "Unknown", Globe];

export default function Alerts({ scope = "org" }: { scope?: Scope }) {
  const { org, can, user } = useSession();
  const allowed = scope === "platform" ? !!user?.operator : can("admin");
  const base = scope === "platform" ? PLATFORM : org ? orgBase(org.id) : "";
  const a = useMemo(() => alertsApi(base), [base]);
  const list = useAsync(() => (allowed && base ? a.list() : Promise.resolve([] as AlertChannel[])), [a, allowed]);
  const [editing, setEditing] = useState<AlertChannel | "new" | null>(null);
  const [deleting, setDeleting] = useState<AlertChannel | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const title = "Alerts";
  const description =
    scope === "platform"
      ? "Send platform events, such as quarantined keys of public providers, to chat, email or push services."
      : "Send this organization's events, such as quarantined provider keys or exceeded budgets, to chat or push services.";
  if (scope === "org" && !org) return <EmptyState title="You are not in an organization yet" />;
  if (!allowed)
    return (
      <>
        <PageHeader title={title} description={description} />
        <NoAccess>{scope === "platform" ? "Only operators manage platform alerts." : "Only organization admins manage alerts."}</NoAccess>
      </>
    );

  async function run(c: AlertChannel, fn: () => Promise<void>) {
    setBusy(c.id);
    try {
      await fn();
    } catch (e) {
      toast((e as Error).message, { error: true });
    } finally {
      setBusy(null);
    }
  }
  const setEnabled = (c: AlertChannel, enabled: boolean) =>
    run(c, async () => {
      await a.update(c.id, { enabled });
      toast(enabled ? "Channel enabled" : "Channel disabled");
      list.reload();
    });
  const test = (c: AlertChannel) =>
    run(c, async () => {
      const r = await a.test(c.id);
      if (r?.ok === false) toast(r.error || "The test message was not delivered", { error: true });
      else toast(`Test message sent to ${c.name}`);
      list.reload();
    });

  const add = (
    <Button onClick={() => setEditing("new")}>
      <Plus aria-hidden /> Add channel
    </Button>
  );

  return (
    <>
      <PageHeader title={title} description={description} actions={add} />
      <DataState
        state={list}
        rows={3}
        empty={
          <EmptyState title="No alert channels yet" action={add}>
            Add Telegram, Slack, Discord, Teams, email or any other service to hear about failing keys and providers.
          </EmptyState>
        }
      >
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Service</TableHead>
                  <TableHead>Events</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Failures</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((c) => {
                  const [label, Icon] = serviceOf(c.service);
                  const autoOff = !c.enabled && c.failures >= MAX_FAILURES;
                  return (
                    <TableRow key={c.id}>
                      <TableCell>
                        <span className="font-medium">{c.name}</span>
                        <span className="block text-xs text-muted-foreground">Added {when(c.created_at)}</span>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <span className="inline-flex items-center gap-2 text-sm">
                          <Icon className="size-4 text-muted-foreground" aria-hidden />
                          {label}
                        </span>
                      </TableCell>
                      <TableCell>
                        <div className="flex max-w-64 flex-wrap gap-1">
                          {(c.events ?? []).map((e) => (
                            <Badge key={e} variant="outline" className="font-mono text-xs font-normal">
                              {e === "*" ? "All events" : e}
                            </Badge>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <Badge
                          variant="outline"
                          className={!c.enabled ? (autoOff ? "text-danger" : "text-muted-foreground") : c.failures ? "border-warning/60 text-foreground" : "text-success"}
                        >
                          {!c.enabled ? (autoOff ? "Disabled after failures" : "Disabled") : c.failures ? "Failing" : "Active"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right tabular-nums">{c.failures}</TableCell>
                      <TableCell>
                        <Switch
                          checked={c.enabled}
                          disabled={busy === c.id}
                          onCheckedChange={(on) => setEnabled(c, on)}
                          aria-label={`${c.enabled ? "Disable" : "Enable"} channel ${c.name}`}
                        />
                      </TableCell>
                      <TableCell>
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="sm" disabled={busy === c.id} onClick={() => test(c)} aria-label={`Send a test message to ${c.name}`}>
                            <Send aria-hidden /> Test
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setEditing(c)} aria-label={`Edit channel ${c.name}`}>
                            <Pencil aria-hidden />
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setDeleting(c)} aria-label={`Delete channel ${c.name}`}>
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
        Each event is sent at most once per subject (provider, key, model or budget) every 10 minutes. A channel is disabled after {MAX_FAILURES} failed deliveries in a
        row. Messages are sent with{" "}
        <a className="underline underline-offset-4" href={README_URL} target="_blank" rel="noreferrer">shoutrrr</a>.
      </p>

      {editing && (
        <ChannelDialog
          key={editing === "new" ? "new" : editing.id}
          scope={scope}
          channel={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          save={(body) => (editing === "new" ? a.create(body) : a.update(editing.id, body))}
          onSaved={list.reload}
        />
      )}
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "channel"}?`}
        description="The channel stops receiving alerts immediately. Its stored URL can't be recovered."
        confirmLabel="Delete channel"
        success="Channel deleted"
        onConfirm={async () => {
          await a.remove(deleting!.id);
          list.reload();
        }}
      />
    </>
  );
}

// ---- URL builder ----

type Kind = "telegram" | "discord" | "slack" | "teams" | "smtp" | "ntfy" | "custom";
const kinds: [Kind, string][] = [
  ["telegram", "Telegram"],
  ["discord", "Discord"],
  ["slack", "Slack"],
  ["teams", "Microsoft Teams"],
  ["smtp", "Email (SMTP)"],
  ["ntfy", "ntfy"],
  ["custom", "Custom URL"],
];
type Form = Record<string, string>;
const enc = encodeURIComponent;

/**
 * Builds a shoutrrr URL from friendly fields; throws with a message the user can act on.
 * Formats follow the shoutrrr service docs; the server parses and validates the URL again.
 */
function buildURL(kind: Kind, f: Form): string {
  const v = (k: string) => (f[k] ?? "").trim();
  switch (kind) {
    case "telegram": {
      const token = v("token");
      if (!/^\d+:[\w-]+$/.test(token)) throw new Error("Bot token looks like 123456789:AAH… (from @BotFather).");
      const chats = splitList(v("chats"));
      if (!chats.length) throw new Error("Enter a chat ID or @channel.");
      const bad = chats.find((c) => !/^(@\w+|-?\d+)$/.test(c));
      if (bad) throw new Error(`"${bad}" is not a chat ID (such as -1001234567890) or @channel name.`);
      return `telegram://${token}@telegram?chats=${chats.join(",")}`;
    }
    case "discord": {
      const m = v("webhook").match(/^https:\/\/(?:(?:canary|ptb)\.)?discord(?:app)?\.com\/api\/webhooks\/(\d+)\/([\w-]+)\/?$/);
      if (!m) throw new Error("Paste the webhook URL from Server settings → Integrations → Webhooks (https://discord.com/api/webhooks/…).");
      return `discord://${m[2]}@${m[1]}`;
    }
    case "slack": {
      if (v("mode") === "bot") {
        const t = v("token").match(/^xoxb-([\w-]+)$/);
        if (!t) throw new Error("A bot token starts with xoxb-.");
        const ch = v("channel");
        if (!/^[A-Z0-9]+$/i.test(ch)) throw new Error("Enter the channel ID, such as C0123456789 (not #name).");
        return `slack://xoxb:${t[1]}@${ch}`;
      }
      const m = v("webhook").match(/^https:\/\/hooks\.slack\.com\/services\/([A-Z0-9]+)\/([A-Z0-9]+)\/([A-Za-z0-9]+)\/?$/);
      if (!m) throw new Error("Paste the incoming webhook URL (https://hooks.slack.com/services/T…/B…/…).");
      return `slack://hook:${m[1]}-${m[2]}-${m[3]}@webhook`;
    }
    case "teams": {
      // Power Automate workflow webhooks only (Office 365 connectors are retired); same check as
      // shoutrrr's teams.ValidateWebhookURL.
      const u = v("webhook");
      const ok =
        /^https:\/\/[a-zA-Z0-9][a-zA-Z0-9.-]*\.logic\.azure(?:\.[a-z]{2,})?(:\d+)?\/(?:powerautomate\/automations\/direct\/(?:[A-Za-z0-9-]+\/)*)?workflows\//.test(u) ||
        /^https:\/\/[a-zA-Z0-9][a-zA-Z0-9.-]*\.environment\.api\.powerplatform\.com(:\d+)?\/powerautomate\/automations\/direct\/(?:[A-Za-z0-9-]+\/)*workflows\//.test(u);
      if (!ok) throw new Error("Paste the workflow URL from Teams → Workflows → \"Post to a channel when a webhook request is received\" (https://….logic.azure.com/workflows/… or ….powerplatform.com/…).");
      return `teams://?host=${enc(u)}`;
    }
    case "smtp": {
      const host = v("host");
      if (!/^[\w.-]+$/.test(host)) throw new Error("Enter the SMTP host name, such as smtp.example.com.");
      const port = v("port") || "587";
      if (!/^\d{1,5}$/.test(port)) throw new Error("Port is a number, such as 587.");
      const from = v("from");
      const to = splitList(v("to"));
      if (!from.includes("@")) throw new Error("Enter the sender address.");
      if (!to.length || to.some((x) => !x.includes("@"))) throw new Error("Enter one or more recipient addresses.");
      const auth = v("user") ? `${enc(v("user"))}:${enc(f.password ?? "")}@` : "";
      return `smtp://${auth}${host}:${port}/?fromaddress=${enc(from)}&toaddresses=${to.map(enc).join(",")}`;
    }
    case "ntfy": {
      const topic = v("topic");
      if (!/^[\w-]{1,64}$/.test(topic)) throw new Error("A topic is letters, digits, - and _.");
      const server = v("server").replace(/^https:\/\//, "").replace(/\/+$/, "") || "ntfy.sh";
      if (!/^[\w.-]+(:\d+)?$/.test(server)) throw new Error("Server is a host name such as ntfy.example.com (https only).");
      return `ntfy://${server}/${topic}`;
    }
    case "custom": {
      const u = v("url");
      if (!/^[a-z][a-z0-9+.-]*:\/\/\S+$/i.test(u)) throw new Error("Enter a shoutrrr URL, such as gotify://host/token.");
      return u;
    }
  }
}

function ChannelDialog({
  scope, channel, onClose, save, onSaved,
}: {
  scope: Scope;
  channel?: AlertChannel;
  onClose: () => void;
  save: (body: AlertInput) => Promise<unknown>;
  onSaved: () => void;
}) {
  const [name, setName] = useState(channel?.name ?? "");
  const [replace, setReplace] = useState(!channel);
  const { config } = useSession();
  // Services this server offers for the scope (GET /api/auth/config alert_services); older servers
  // do not list them, then every builder is offered and the server validates.
  const offered = config?.alert_services;
  const unavailable = (k: Kind) =>
    k === "custom" || !offered
      ? ""
      : !offered.platform.includes(k)
        ? "needs a build with -tags alertsfull"
        : scope === "org" && !offered.org.includes(k)
          ? "platform channels only"
          : "";
  const [kind, setKind] = useState<Kind>("telegram");
  const [form, setForm] = useState<Form>({ mode: "webhook" });
  const [all, setAll] = useState(channel?.events?.includes("*") ?? false);
  const [events, setEvents] = useState<string[]>(channel ? channel.events.filter((e) => e !== "*") : ["key.quarantined", "provider.unavailable"]);
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const set = (k: string) => (e: { target: { value: string } }) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const preview = useMemo(() => {
    try {
      return buildURL(kind, form);
    } catch {
      return "";
    }
  }, [kind, form]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const picked = all ? ["*"] : events;
    if (!picked.length) return setErr("Choose at least one event.");
    let url: string | undefined;
    try {
      if (replace) url = buildURL(kind, form);
    } catch (e) {
      return setErr((e as Error).message);
    }
    setBusy(true);
    setErr("");
    try {
      await save({ name: name.trim(), events: picked, enabled, ...(url ? { url } : {}) });
      toast(channel ? "Channel saved" : "Channel added");
      onSaved();
      onClose();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const text = (k: string, label: string, opts: { hint?: ReactNode; placeholder?: string; type?: string; required?: boolean } = {}) => (
    <Field label={label} hint={opts.hint}>
      {(id, hint) => (
        <Input
          id={id}
          aria-describedby={hint}
          type={opts.type}
          value={form[k] ?? ""}
          onChange={set(k)}
          placeholder={opts.placeholder}
          required={opts.required}
          autoComplete={opts.type === "password" ? "new-password" : "off"}
          spellCheck={false}
          className="font-mono text-xs"
        />
      )}
    </Field>
  );
  const customHost = scope === "org" && (kind === "smtp" || (kind === "ntfy" && !!form.server?.trim() && form.server.trim() !== "ntfy.sh"));

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto scrollbar-thin sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{channel ? `Edit ${channel.name}` : "Add alert channel"}</DialogTitle>
          <DialogDescription>The channel URL holds its credentials: it is encrypted and never shown again.</DialogDescription>
        </DialogHeader>
        <form id="alert-channel" onSubmit={submit} className="grid gap-4">
          <Field label="Name">
            {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} placeholder="Ops on Telegram" autoComplete="off" autoFocus />}
          </Field>

          {!replace ? (
            <div className="flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm">
              <span>
                {serviceOf(channel?.service)[0]} URL <span className="text-muted-foreground">saved, hidden</span>
              </span>
              <Button type="button" size="sm" variant="outline" onClick={() => setReplace(true)}>Replace URL</Button>
            </div>
          ) : (
            <div className="grid gap-4 rounded-md border p-3">
              <div className="grid gap-1.5">
                <Label htmlFor="alert-service">Service</Label>
                <Select value={kind} onValueChange={(k) => (setKind(k as Kind), setErr(""))}>
                  <SelectTrigger id="alert-service" className="w-full"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {kinds.map(([k, label]) => {
                      const why = unavailable(k);
                      return (
                        <SelectItem key={k} value={k} disabled={!!why}>
                          {label}
                          {why && <span className="text-muted-foreground">({why})</span>}
                        </SelectItem>
                      );
                    })}
                  </SelectContent>
                </Select>
              </div>
              {kind === "telegram" && (
                <>
                  {text("token", "Bot token", { type: "password", placeholder: "123456789:AAH…", hint: "From @BotFather. Add the bot to the chat or channel first." })}
                  {text("chats", "Chat", { placeholder: "@mychannel or -1001234567890", hint: "A chat ID or @channel; separate several with commas." })}
                </>
              )}
              {kind === "discord" && text("webhook", "Webhook URL", { type: "password", placeholder: "https://discord.com/api/webhooks/…" })}
              {kind === "slack" && (
                <>
                  <div className="grid gap-1.5">
                    <Label htmlFor="alert-slack-mode">Connect with</Label>
                    <Select value={form.mode || "webhook"} onValueChange={(m) => setForm((f) => ({ ...f, mode: m }))}>
                      <SelectTrigger id="alert-slack-mode" className="w-full"><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="webhook">Incoming webhook URL</SelectItem>
                        <SelectItem value="bot">Bot token and channel</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  {form.mode === "bot" ? (
                    <>
                      {text("token", "Bot token", { type: "password", placeholder: "xoxb-…" })}
                      {text("channel", "Channel ID", { placeholder: "C0123456789", hint: "In Slack: channel details → the ID at the bottom. Invite the bot to the channel." })}
                    </>
                  ) : (
                    text("webhook", "Webhook URL", { type: "password", placeholder: "https://hooks.slack.com/services/T…/B…/…" })
                  )}
                </>
              )}
              {kind === "teams" &&
                text("webhook", "Incoming webhook URL", {
                  type: "password",
                  placeholder: "https://prod-00.westus.logic.azure.com:443/workflows/…",
                  hint: "A Teams Workflows (Power Automate) webhook URL. Office 365 connector URLs are retired and not accepted.",
                })}
              {kind === "smtp" && (
                <>
                  <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_6rem]">
                    {text("host", "SMTP host", { placeholder: "smtp.example.com" })}
                    {text("port", "Port", { placeholder: "587" })}
                  </div>
                  <div className="grid gap-4 sm:grid-cols-2">
                    {text("user", "Username")}
                    {text("password", "Password", { type: "password" })}
                  </div>
                  {text("from", "From", { placeholder: "alerts@example.com" })}
                  {text("to", "To", { placeholder: "ops@example.com, oncall@example.com", hint: "Separate several addresses with commas." })}
                </>
              )}
              {kind === "ntfy" && (
                <>
                  {text("topic", "Topic", { placeholder: "polyrail-alerts", hint: "Anyone who knows a topic on a public server can read it: pick a hard-to-guess name." })}
                  {text("server", "Server", { placeholder: "ntfy.sh", hint: "Empty uses ntfy.sh. A self-hosted server must use HTTPS." })}
                </>
              )}
              {kind === "custom" && (
                <>
                  {text("url", "Shoutrrr URL", { type: "password", placeholder: "gotify://gotify.example.com/token" })}
                  <p className="text-xs text-muted-foreground">
                    {offered
                      ? `Services on this server: ${(scope === "org" ? offered.org : offered.platform).map((x) => serviceOf(x)[0]).join(", ")}.`
                      : "A shoutrrr service URL, such as Gotify, Pushover, Matrix or Google Chat."}{" "}
                    URL formats:{" "}
                    <a className="inline-flex items-center gap-0.5 underline underline-offset-4" href={DOCS_URL} target="_blank" rel="noreferrer">
                      shoutrrr services <ExternalLink className="size-3" aria-hidden />
                    </a>
                    .
                  </p>
                </>
              )}
              {customHost && (
                <p className="text-xs text-muted-foreground">
                  Services on a custom host are refused for organizations unless the operator allows private provider URLs.
                </p>
              )}
              {kind !== "custom" && preview && (
                <details className="text-xs">
                  <summary className="cursor-pointer rounded-sm text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">Show the generated URL</summary>
                  <code className="mt-1 block break-all rounded bg-muted px-2 py-1 font-mono">{preview}</code>
                </details>
              )}
            </div>
          )}

          <fieldset className="grid gap-2">
            <legend className="mb-1.5 text-sm font-medium">Events</legend>
            <label className="flex items-start gap-2 text-sm">
              <Checkbox className="mt-0.5" checked={all} onCheckedChange={(c) => setAll(c === true)} />
              <span>
                All events
                <span className="block text-xs text-muted-foreground">Includes event types added in future versions.</span>
              </span>
            </label>
            {alertEvents.map(([ev, help]) => (
              <label key={ev} className="flex items-start gap-2 text-sm">
                <Checkbox
                  className="mt-0.5"
                  disabled={all}
                  checked={all || events.includes(ev)}
                  onCheckedChange={(c) => setEvents((es) => (c ? [...es, ev] : es.filter((x) => x !== ev)))}
                />
                <span>
                  <span className="font-mono text-xs">{ev}</span>
                  <span className="block text-xs text-muted-foreground">{help}</span>
                </span>
              </label>
            ))}
          </fieldset>
          {channel && (
            <div className="flex items-center gap-3">
              <Switch id="alert-enabled" checked={enabled} onCheckedChange={setEnabled} />
              <Label htmlFor="alert-enabled">Enabled</Label>
            </div>
          )}
          <FormError message={err} />
        </form>
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>Cancel</Button>
          <Button type="submit" form="alert-channel" disabled={busy}>
            {busy ? "Saving…" : channel ? "Save changes" : "Add channel"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
