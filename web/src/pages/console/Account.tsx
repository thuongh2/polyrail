import { useState, type FormEvent } from "react";
import { Field, Section } from "@/components/console/bits";
import { PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authApi } from "@/lib/api";
import { useSession } from "@/lib/session";

export default function Account() {
  const { user, orgs } = useSession();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  if (!user) return null;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (next.length < 10) return setErr("The new password needs at least 10 characters.");
    if (next !== again) return setErr("The two new passwords don't match.");
    setBusy(true);
    setErr("");
    try {
      await authApi.password(cur, next);
      setCur(""), setNext(""), setAgain("");
      toast("Password changed. Other sessions were signed out.");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader title="Account" description="Your sign-in details." />
      <dl className="grid max-w-2xl grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-4 gap-y-3 rounded-lg border p-4 text-sm">
        <dt className="text-muted-foreground">Email</dt>
        <dd className="break-all">{user.email}</dd>
        <dt className="text-muted-foreground">Name</dt>
        <dd>{user.name || "–"}</dd>
        <dt className="text-muted-foreground">Platform role</dt>
        <dd>{user.operator ? <Badge variant="outline" className="text-signal">Operator</Badge> : "User"}</dd>
        <dt className="text-muted-foreground">Organizations</dt>
        <dd>{orgs.length ? orgs.map((o) => `${o.name} (${o.role})`).join(", ") : "None"}</dd>
        <dt className="text-muted-foreground">Member since</dt>
        <dd>{new Date(user.created_at).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" })}</dd>
      </dl>

      <Section title="Change password" description="Changing it signs out every other session.">
        <form onSubmit={submit} className="grid max-w-sm gap-4">
          {/* hidden username helps password managers pair the new password with this account */}
          <input type="email" name="username" autoComplete="username" value={user.email} readOnly hidden />
          <Field id="pw-cur" label="Current password" help="Leave empty if you have only signed in with SSO.">
            <Input id="pw-cur" type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} aria-describedby="pw-cur-help" />
          </Field>
          <Field id="pw-new" label="New password" help="At least 10 characters.">
            <Input id="pw-new" type="password" autoComplete="new-password" required value={next} onChange={(e) => setNext(e.target.value)} aria-describedby="pw-new-help" />
          </Field>
          <Field id="pw-again" label="Repeat new password">
            <Input id="pw-again" type="password" autoComplete="new-password" required value={again} onChange={(e) => setAgain(e.target.value)} />
          </Field>
          {err && (
            <p role="alert" className="text-sm text-destructive">
              {err}
            </p>
          )}
          <div>
            <Button type="submit" disabled={busy}>
              {busy ? "Saving…" : "Change password"}
            </Button>
          </div>
        </form>
      </Section>
    </>
  );
}
