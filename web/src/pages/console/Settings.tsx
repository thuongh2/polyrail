import { useEffect, useState, type FormEvent } from "react";
import { useLocation } from "wouter";
import { Field, Section } from "@/components/console/bits";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import {
  AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api, get, orgBase, type Org } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

/** Returns the first invalid pattern's message, or "" when every line compiles. */
function badPattern(lines: string[]) {
  for (const p of lines) {
    try {
      new RegExp(p);
    } catch (e) {
      return `Line “${p}”: ${(e as Error).message}`;
    }
  }
  return "";
}

export default function Settings() {
  const { org, can } = useSession();
  const base = org ? orgBase(org.id) : "";
  const info = useAsync(() => get<Org>(base), [base]);
  if (!org) return <EmptyState title="You are not in an organization yet" />;
  const admin = can("admin");

  return (
    <>
      <PageHeader title="Settings" description={admin ? "Organization name, output limits and guardrails." : "Only admins can change these settings."} />
      <DataState state={info} rows={5}>
        {(o) => (
          <>
            <SettingsForm org={o} base={base} readOnly={!admin} onSaved={info.reload} />
            {can("owner") && <DangerZone org={o} base={base} />}
          </>
        )}
      </DataState>
    </>
  );
}

function SettingsForm({ org, base, readOnly, onSaved }: { org: Org; base: string; readOnly: boolean; onSaved: () => void }) {
  const { refresh } = useSession();
  const init = () => ({
    name: org.name,
    cap: org.max_tokens_cap ? String(org.max_tokens_cap) : "",
    maxChars: org.guardrails.max_input_chars ? String(org.guardrails.max_input_chars) : "",
    patterns: (org.guardrails.blocked_patterns ?? []).join("\n"),
    pii: !!org.guardrails.mask_pii,
  });
  const [v, setV] = useState(init);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => {
    setV(init());
  }, [org]);

  const patterns = v.patterns.split("\n").map((s) => s.trim()).filter(Boolean);
  const patternErr = badPattern(patterns);
  const dirty = JSON.stringify(v) !== JSON.stringify(init());

  async function save(e: FormEvent) {
    e.preventDefault();
    if (patternErr) return;
    const cap = Number(v.cap || 0);
    const maxChars = Number(v.maxChars || 0);
    if (!Number.isInteger(cap) || cap < 0 || !Number.isInteger(maxChars) || maxChars < 0) {
      setErr("Limits must be whole numbers, or empty for none.");
      return;
    }
    if (!v.name.trim()) {
      setErr("The organization needs a name.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await api("PATCH", base, {
        name: v.name.trim(),
        max_tokens_cap: cap,
        guardrails: { max_input_chars: maxChars || undefined, blocked_patterns: patterns.length ? patterns : undefined, mask_pii: v.pii || undefined },
      });
      toast("Settings saved");
      onSaved();
      refresh().catch(() => {}); // the org switcher shows the name
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="max-w-2xl">
      <fieldset disabled={readOnly} className="min-w-0">
        <Section title="General">
          <div className="grid gap-4">
            <Field id="s-name" label="Organization name">
              <Input id="s-name" value={v.name} onChange={(e) => setV({ ...v, name: e.target.value })} />
            </Field>
            <Field id="s-cap" label="Max output tokens per request" help="Requests asking for more are capped to this. Empty means the model's own limit.">
              <Input id="s-cap" type="number" min={0} inputMode="numeric" placeholder="No cap" className="max-w-48" value={v.cap} onChange={(e) => setV({ ...v, cap: e.target.value })} aria-describedby="s-cap-help" />
            </Field>
          </div>
        </Section>
        <Section title="Guardrails" description="Checked by the gateway before a request reaches any provider.">
          <div className="grid gap-4">
            <Field id="s-chars" label="Max input characters" help="Requests whose messages are longer are refused. Empty means no limit.">
              <Input id="s-chars" type="number" min={0} inputMode="numeric" placeholder="No limit" className="max-w-48" value={v.maxChars} onChange={(e) => setV({ ...v, maxChars: e.target.value })} aria-describedby="s-chars-help" />
            </Field>
            <Field id="s-patterns" label="Blocked patterns" help={patternErr ? <span className="text-destructive">{patternErr}</span> : "One regular expression per line (Go RE2 syntax). A request matching any of them is refused."}>
              <Textarea
                id="s-patterns"
                rows={5}
                spellCheck={false}
                className="font-mono text-xs"
                placeholder={"(?i)internal-only\n\\bproject-x\\b"}
                value={v.patterns}
                onChange={(e) => setV({ ...v, patterns: e.target.value })}
                aria-invalid={!!patternErr}
                aria-describedby="s-patterns-help"
              />
            </Field>
            <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
              <div>
                <label htmlFor="s-pii" className="text-sm font-medium">
                  Mask personal data
                </label>
                <p className="text-xs text-muted-foreground">Replaces email addresses and phone numbers in prompts with placeholders before they reach a provider.</p>
              </div>
              <Switch id="s-pii" checked={v.pii} onCheckedChange={(c) => setV({ ...v, pii: c })} />
            </div>
          </div>
        </Section>
      </fieldset>
      {err && (
        <p role="alert" className="mt-4 text-sm text-destructive">
          {err}
        </p>
      )}
      {!readOnly && (
        <div className="mt-6 flex gap-2">
          <Button type="submit" disabled={busy || !dirty || !!patternErr}>
            {busy ? "Saving…" : "Save changes"}
          </Button>
          {dirty && (
            <Button type="button" variant="ghost" onClick={() => setV(init())}>
              Discard
            </Button>
          )}
        </div>
      )}
    </form>
  );
}

function DangerZone({ org, base }: { org: Org; base: string }) {
  const { refresh, setOrg } = useSession();
  const [, navigate] = useLocation();
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);

  async function del() {
    setBusy(true);
    try {
      await api("DELETE", base);
      toast(`Deleted ${org.name}`);
      setOrg("");
      setOpen(false);
      await refresh();
      navigate("/activity");
    } catch (e) {
      toast((e as Error).message, { error: true });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="mt-12 max-w-2xl rounded-lg border border-destructive/40 p-4">
      <h2 className="text-base font-semibold">Danger zone</h2>
      <div className="mt-2 flex flex-wrap items-center justify-between gap-4">
        <p className="max-w-md text-sm text-muted-foreground">
          Deleting the organization removes its members, API keys, private providers and models, usage and audit log. This can't be undone.
        </p>
        <Button variant="outline" className="border-destructive/50 text-destructive hover:text-destructive" onClick={() => setOpen(true)}>
          Delete organization
        </Button>
      </div>
      <AlertDialog open={open} onOpenChange={(o) => (setOpen(o), setTyped(""))}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {org.name}?</AlertDialogTitle>
            <AlertDialogDescription>Every API key stops working immediately. Type the organization's name to confirm.</AlertDialogDescription>
          </AlertDialogHeader>
          <Field id="del-name" label={`Type “${org.name}”`}>
            <Input id="del-name" autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />
          </Field>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button variant="destructive" disabled={typed !== org.name || busy} onClick={del}>
              {busy ? "Deleting…" : "Delete organization"}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}
