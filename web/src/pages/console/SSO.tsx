// Security & SSO (docs/API.md "Enterprise identity"): verified domains, SAML sign-in, SCIM
// provisioning and IdP group → role mappings. Owner only.
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Download, Plus, RefreshCw, Trash2 } from "lucide-react";
import { CopyButton, Section, TableFrame } from "@/components/console/bits";
import { SecretDialog } from "@/components/console/SecretDialog";
import { ConfirmDialog, Field, FormError, NoAccess } from "@/components/platform/kit";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, identityApi, when, type Domain, type GroupMapping, type Role, type SAMLInput, type SSOSettings } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

const ROLES: [Role, string][] = [
  ["viewer", "Viewer"],
  ["member", "Member"],
  ["admin", "Admin"],
];

export default function SSO() {
  const { org, can } = useSession();
  const owner = can("owner");
  const id = useMemo(() => (org ? identityApi(org.id) : null), [org]);
  const domains = useAsync(() => (owner && id ? id.domains() : Promise.resolve([] as Domain[])), [id, owner]);
  const sso = useAsync(() => (owner && id ? id.sso() : Promise.resolve(null)), [id, owner]);

  if (!org || !id) return <EmptyState title="You are not in an organization yet" />;
  const description = "Verify your email domains, sign members in with your identity provider over SAML, and provision them with SCIM.";
  if (!owner)
    return (
      <>
        <PageHeader title="Security & SSO" description={description} />
        <NoAccess>Only organization owners manage domains, single sign-on and SCIM.</NoAccess>
      </>
    );

  const verified = (domains.data ?? []).some((d) => d.verified);
  return (
    <>
      <PageHeader title="Security & SSO" description={description} />
      <div className="max-w-4xl">
        <Domains api={id} state={domains} />
        <DataState state={sso} rows={6}>
          {(s) =>
            s && (
              <>
                <SAMLSection key={s.updated_at} api={id} s={s} verified={verified} onSaved={sso.reload} />
                <SCIMSection api={id} s={s} onSaved={sso.reload} />
                <GroupMappings key={"gm" + s.updated_at} api={id} s={s} onSaved={sso.reload} />
              </>
            )
          }
        </DataState>
      </div>
    </>
  );
}

type IdAPI = ReturnType<typeof identityApi>;

// ---- domains ----

function Domains({ api, state }: { api: IdAPI; state: ReturnType<typeof useAsync<Domain[]>> }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState("");
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [deleting, setDeleting] = useState<Domain | null>(null);

  async function add(e: FormEvent) {
    e.preventDefault();
    const n = name.trim().toLowerCase();
    if (!n) return;
    setBusy("add");
    setErr("");
    try {
      await api.addDomain(n);
      setName("");
      toast(`${n} added. Publish its TXT record, then verify.`);
      state.reload();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(null);
    }
  }

  async function verify(d: Domain) {
    setBusy(d.id);
    setNotes((n) => ({ ...n, [d.id]: "" }));
    try {
      await api.verifyDomain(d.id);
      toast(`${d.name} verified`);
      state.reload();
    } catch (e) {
      const st = e instanceof ApiError ? e.status : 0;
      const msg =
        st === 422
          ? "The TXT record is not visible yet. DNS changes can take from a few minutes to a few hours to reach every resolver; check the record name and value, then try again."
          : st === 409
            ? "Another organization has already verified this domain. A domain belongs to one organization; ask its owner to remove it first."
            : (e as Error).message;
      setNotes((n) => ({ ...n, [d.id]: msg }));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Section
      title="Domains"
      description="Prove you own an email domain with a DNS TXT record. SAML, SCIM and enforced SSO only act on emails at verified domains."
    >
      <form onSubmit={add} className="mb-3 flex max-w-lg gap-2">
        <label htmlFor="domain-name" className="sr-only">Domain name</label>
        <Input id="domain-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="example.com" autoComplete="off" spellCheck={false} />
        <Button type="submit" disabled={busy === "add" || !name.trim()}>
          <Plus aria-hidden /> Add domain
        </Button>
      </form>
      <FormError message={err} />
      <DataState
        state={state}
        rows={2}
        empty={<p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">No domains yet. Add the domain of your work email addresses.</p>}
      >
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Domain</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>DNS record</TableHead>
                  <TableHead><span className="sr-only">Actions</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((d) => (
                  <TableRow key={d.id} className="align-top">
                    <TableCell className="font-medium">{d.name}</TableCell>
                    <TableCell className="whitespace-nowrap">
                      {d.verified ? (
                        <Badge variant="outline" className="text-success">Verified</Badge>
                      ) : (
                        <Badge variant="outline" className="border-warning/60 text-foreground">Pending</Badge>
                      )}
                      {d.verified && <span className="mt-1 block text-xs text-muted-foreground">{when(d.verified_at)}</span>}
                    </TableCell>
                    <TableCell className="min-w-72 whitespace-normal">
                      {d.verified ? (
                        <span className="text-xs text-muted-foreground">You may keep or remove the TXT record.</span>
                      ) : (
                        <div className="grid gap-2">
                          <RecordLine label="TXT name" value={d.record.name} />
                          <RecordLine label="Value" value={d.record.value} />
                          {notes[d.id] && (
                            <p role="alert" className="text-xs text-destructive">{notes[d.id]}</p>
                          )}
                        </div>
                      )}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        {!d.verified && (
                          <Button size="sm" variant="outline" disabled={busy === d.id} onClick={() => verify(d)}>
                            {busy === d.id ? "Checking…" : "Verify"}
                          </Button>
                        )}
                        <Button size="icon" variant="ghost" onClick={() => setDeleting(d)} aria-label={`Remove domain ${d.name}`}>
                          <Trash2 aria-hidden />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Remove ${deleting?.name ?? "domain"}?`}
        description="The claim is released. SAML sign-in, SCIM and enforced SSO stop covering emails at this domain."
        confirmLabel="Remove domain"
        success="Domain removed"
        onConfirm={async () => {
          await api.removeDomain(deleting!.id);
          state.reload();
        }}
      />
    </Section>
  );
}

function RecordLine({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <span className="w-16 shrink-0 text-xs text-muted-foreground">{label}</span>
      <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-xs" title={value}>{value}</code>
      <CopyButton text={value} label="Copy" />
    </div>
  );
}

// ---- SAML ----

function SAMLSection({ api, s, verified, onSaved }: { api: IdAPI; s: SSOSettings; verified: boolean; onSaved: () => void }) {
  const c = s.saml;
  const [mode, setMode] = useState<"url" | "xml">(c.idp_metadata_xml && !c.idp_metadata_url ? "xml" : "url");
  const [url, setUrl] = useState(c.idp_metadata_url ?? "");
  const [xml, setXml] = useState(c.idp_metadata_url ? "" : (c.idp_metadata_xml ?? ""));
  const [refetch, setRefetch] = useState(false);
  const [attrEmail, setAttrEmail] = useState(c.attribute_email ?? "");
  const [attrName, setAttrName] = useState(c.attribute_name ?? "");
  const [attrGroups, setAttrGroups] = useState(c.attribute_groups ?? "");
  const [role, setRole] = useState<Role>(c.default_role || "member");
  const [idpInit, setIdpInit] = useState(c.allow_idp_initiated);
  const [enabled, setEnabled] = useState(c.enabled);
  const [enforce, setEnforce] = useState(c.enforce);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const configured = !!c.idp_metadata_xml;
  const canEnforce = enabled && verified;

  async function save(e: FormEvent) {
    e.preventDefault();
    const body: SAMLInput = {
      enabled,
      default_role: role,
      attribute_email: attrEmail.trim(),
      attribute_name: attrName.trim(),
      attribute_groups: attrGroups.trim(),
      allow_idp_initiated: idpInit,
      enforce: canEnforce && enforce,
    };
    // Metadata is sent only when it changed (or a re-fetch was asked); omitted keeps the stored copy.
    if (mode === "url" && url.trim() && (url.trim() !== (c.idp_metadata_url ?? "") || refetch)) body.idp_metadata_url = url.trim();
    if (mode === "xml" && xml.trim() && xml.trim() !== (c.idp_metadata_xml ?? "").trim()) body.idp_metadata_xml = xml.trim();
    if (enabled && !configured && !body.idp_metadata_url && !body.idp_metadata_xml)
      return setErr("Give the identity provider's metadata URL or XML before turning SAML on.");
    setBusy(true);
    setErr("");
    try {
      await api.putSAML(body);
      toast("SAML settings saved");
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="SAML single sign-on"
      description="Members at your verified domains sign in through your identity provider. New users get an account on first sign-in."
    >
      <div className="grid gap-6">
        <div className="grid gap-3 rounded-lg border p-4">
          <div>
            <h3 className="text-sm font-semibold">For your identity provider</h3>
            <p className="text-xs text-muted-foreground">Enter these in the SAML app you create for Polyrail.</p>
          </div>
          <dl className="grid gap-2">
            <SPLine label="Entity ID (audience)" value={s.sp.entity_id} />
            <SPLine label="ACS URL (reply URL)" value={s.sp.acs_url} />
            <SPLine label="Metadata URL" value={s.sp.metadata_url} />
            <SPLine label="Sign-in URL" value={s.sp.login_url} />
          </dl>
          <details className="text-sm">
            <summary className="cursor-pointer rounded-sm text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">
              Signing certificate
            </summary>
            <div className="mt-2 grid gap-2">
              <label htmlFor="sp-cert" className="sr-only">Service provider certificate</label>
              <Textarea id="sp-cert" readOnly rows={6} value={s.sp.certificate} className="max-h-48 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
              <div className="flex gap-2">
                <CopyButton text={s.sp.certificate} label="Copy certificate" />
                <Button type="button" size="sm" variant="outline" onClick={() => download("polyrail-sp.crt", s.sp.certificate)}>
                  <Download aria-hidden /> Download .crt
                </Button>
              </div>
            </div>
          </details>
        </div>

        <Guides />

        <form onSubmit={save} className="grid gap-4">
          <fieldset className="grid gap-3">
            <legend className="mb-1.5 text-sm font-medium">Identity provider metadata</legend>
            {configured && (
              <p className="text-xs text-muted-foreground">
                Saved: <span className="font-mono">{c.idp_entity_id || "metadata"}</span>
                {c.metadata_fetched_at ? `, fetched ${when(c.metadata_fetched_at)}` : ""}. Leave unchanged to keep it.
              </p>
            )}
            <div className="flex gap-1" role="radiogroup" aria-label="Metadata source">
              {(["url", "xml"] as const).map((m) => (
                <Button key={m} type="button" size="sm" variant={mode === m ? "secondary" : "ghost"} role="radio" aria-checked={mode === m} onClick={() => setMode(m)}>
                  {m === "url" ? "Metadata URL" : "Metadata XML"}
                </Button>
              ))}
            </div>
            {mode === "url" ? (
              <Field label="Metadata URL" hint="Fetched once when you save (public HTTPS only). Save again after the IdP rotates its certificate.">
                {(id, hint) => (
                  <Input id={id} aria-describedby={hint} type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://idp.example.com/app/…/sso/saml/metadata" spellCheck={false} className="font-mono text-xs" />
                )}
              </Field>
            ) : (
              <Field label="Metadata XML" hint="Paste the IdP metadata document (an EntityDescriptor).">
                {(id, hint) => (
                  <Textarea id={id} aria-describedby={hint} rows={6} value={xml} onChange={(e) => setXml(e.target.value)} placeholder="<md:EntityDescriptor …>" spellCheck={false} className="max-h-64 font-mono text-xs" />
                )}
              </Field>
            )}
            {mode === "url" && c.idp_metadata_url && url.trim() === c.idp_metadata_url && (
              <label className="flex items-center gap-2 text-sm">
                <Checkbox checked={refetch} onCheckedChange={(v) => setRefetch(v === true)} />
                Fetch the metadata again on save
              </label>
            )}
          </fieldset>

          <fieldset className="grid gap-3">
            <legend className="mb-1.5 text-sm font-medium">Attribute mapping</legend>
            <p className="text-xs text-muted-foreground">Names of the SAML attributes. Empty uses the usual Okta, Entra ID and Google names; the NameID is the email fallback.</p>
            <div className="grid gap-3 sm:grid-cols-3">
              <Field label="Email">{(id) => <Input id={id} value={attrEmail} onChange={(e) => setAttrEmail(e.target.value)} placeholder="email" spellCheck={false} className="font-mono text-xs" />}</Field>
              <Field label="Name">{(id) => <Input id={id} value={attrName} onChange={(e) => setAttrName(e.target.value)} placeholder="name" spellCheck={false} className="font-mono text-xs" />}</Field>
              <Field label="Groups">{(id) => <Input id={id} value={attrGroups} onChange={(e) => setAttrGroups(e.target.value)} placeholder="groups" spellCheck={false} className="font-mono text-xs" />}</Field>
            </div>
          </fieldset>

          <Field label="Default role" hint="For new members without a matching group mapping." className="max-w-xs">
            {(id, hint) => <RoleSelect id={id} hint={hint} value={role} onChange={setRole} />}
          </Field>

          <div className="grid gap-3 border-t pt-4">
            <ToggleRow id="saml-idp" label="Allow IdP-initiated sign-in" help="Accept sign-ins started from the IdP's app dashboard, without a request from Polyrail." checked={idpInit} onChange={setIdpInit} />
            <ToggleRow id="saml-enabled" label="Enable SAML sign-in" help="Show “Continue with SSO” to people at your verified domains." checked={enabled} onChange={(v) => (setEnabled(v), v || setEnforce(false))} />
            <ToggleRow
              id="saml-enforce"
              label="Require SSO"
              disabled={!canEnforce}
              help={
                canEnforce
                  ? "Password sign-in and sign-up are refused for emails at your verified domains; they must use SSO. Platform operators can still sign in with their password."
                  : !verified
                    ? "Needs a verified domain and SAML enabled. Then password sign-in is refused for those emails (platform operators excepted)."
                    : "Turn on SAML sign-in first. Then password sign-in is refused for emails at your verified domains (platform operators excepted)."
              }
              checked={canEnforce && enforce}
              onChange={setEnforce}
            />
          </div>
          <FormError message={err} />
          <div>
            <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save SAML settings"}</Button>
          </div>
        </form>
      </div>
    </Section>
  );
}

function SPLine({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[11rem_minmax(0,1fr)_auto] sm:items-center sm:gap-3">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate font-mono text-xs" title={value}>{value}</dd>
      <dd><CopyButton text={value} /></dd>
    </div>
  );
}

function ToggleRow({ id, label, help, checked, onChange, disabled }: { id: string; label: string; help: ReactNode; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <label htmlFor={id} className="text-sm font-medium">{label}</label>
        <p id={id + "-help"} className="text-xs text-muted-foreground">{help}</p>
      </div>
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onChange} aria-describedby={id + "-help"} />
    </div>
  );
}

function RoleSelect({ id, hint, value, onChange, label }: { id?: string; hint?: string; value: Role; onChange: (r: Role) => void; label?: string }) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as Role)}>
      <SelectTrigger id={id} aria-describedby={hint} aria-label={label} className="w-full"><SelectValue /></SelectTrigger>
      <SelectContent>
        {ROLES.map(([r, l]) => <SelectItem key={r} value={r}>{l}</SelectItem>)}
      </SelectContent>
    </Select>
  );
}

function download(name: string, text: string) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text], { type: "application/x-pem-file" }));
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

const guides: [string, string[]][] = [
  [
    "Okta",
    [
      "Admin Console → Applications → Create App Integration → SAML 2.0.",
      "Single sign-on URL: the ACS URL above. Audience URI (SP Entity ID): the entity ID above.",
      "Name ID format: EmailAddress; Application username: Email.",
      "Attribute statements: email = user.email, name = user.displayName. Group attribute statement: groups, filter “Matches regex” .* (or narrower).",
      "Finish, assign people or groups, then copy the Metadata URL from the Sign On tab into the field below.",
    ],
  ],
  [
    "Microsoft Entra ID",
    [
      "Entra admin center → Enterprise applications → New application → Create your own application (non-gallery).",
      "Single sign-on → SAML. Basic SAML Configuration: Identifier (Entity ID) = the entity ID above; Reply URL = the ACS URL; Sign on URL = the sign-in URL.",
      "Attributes & Claims: the default email and name claims are read as they are. Add a group claim to use group mappings (Entra sends group object IDs unless you choose cloud-only group display names).",
      "SAML Certificates: copy the App Federation Metadata Url into the field below.",
      "Users and groups: assign who may sign in.",
    ],
  ],
  [
    "Google Workspace",
    [
      "Admin console → Apps → Web and mobile apps → Add app → Add custom SAML app.",
      "On Google Identity Provider details, download the IdP metadata and paste it under Metadata XML below.",
      "Service provider details: ACS URL and Entity ID from above; Name ID format EMAIL, Name ID = Basic Information › Primary email.",
      "Attribute mapping: Primary email → email, and Group membership → groups for the groups you want to map.",
      "User access: turn the app on for the organizational units that should sign in.",
    ],
  ],
];

function Guides() {
  return (
    <div className="grid gap-1">
      <h3 className="text-sm font-semibold">Setup guides</h3>
      {guides.map(([name, steps]) => (
        <details key={name} className="rounded-md border px-3 py-2 text-sm">
          <summary className="cursor-pointer rounded-sm font-medium focus-visible:outline-2 focus-visible:outline-ring">{name}</summary>
          <ol className="mt-2 list-decimal space-y-1 pl-5 text-muted-foreground">
            {steps.map((st) => <li key={st}>{st}</li>)}
          </ol>
        </details>
      ))}
    </div>
  );
}

// ---- SCIM ----

function SCIMSection({ api, s, onSaved }: { api: IdAPI; s: SSOSettings; onSaved: () => void }) {
  const [busy, setBusy] = useState(false);
  const [secret, setSecret] = useState<{ token: string; scim_base_url: string } | null>(null);
  const [confirm, setConfirm] = useState(false);
  const hasToken = !!s.scim.token_prefix;

  async function put(enabled: boolean, role: Role) {
    setBusy(true);
    try {
      await api.putSCIM({ enabled, default_role: role });
      toast(enabled === s.scim.enabled ? "SCIM default role saved" : enabled ? "SCIM enabled" : "SCIM disabled");
      onSaved();
    } catch (e) {
      toast((e as Error).message, { error: true });
    } finally {
      setBusy(false);
    }
  }
  async function rotate() {
    const r = await api.rotateSCIMToken();
    setSecret(r);
    onSaved();
  }

  return (
    <Section
      title="SCIM provisioning"
      description="Your identity provider creates, updates and removes members. Deprovisioning removes the membership and revokes the keys the person created here."
      actions={
        <Button variant={hasToken ? "outline" : "default"} onClick={() => (hasToken ? setConfirm(true) : rotate().catch((e) => toast((e as Error).message, { error: true })))}>
          <RefreshCw aria-hidden /> {hasToken ? "Rotate token" : "Generate token"}
        </Button>
      }
    >
      <div className="grid gap-4 rounded-lg border p-4">
        <div className="grid gap-1 sm:grid-cols-[11rem_minmax(0,1fr)_auto] sm:items-center sm:gap-3">
          <span className="text-xs text-muted-foreground">Base URL</span>
          <span className="min-w-0 truncate font-mono text-xs" title={s.scim_base_url}>{s.scim_base_url}</span>
          <CopyButton text={s.scim_base_url} />
        </div>
        <div className="grid gap-1 sm:grid-cols-[11rem_minmax(0,1fr)] sm:items-center sm:gap-3">
          <span className="text-xs text-muted-foreground">Token</span>
          <span className="text-sm">
            {hasToken ? (
              <>
                <span className="font-mono text-xs">{s.scim.token_prefix}…</span>
                <span className="text-muted-foreground"> created {when(s.scim.token_created_at)}</span>
              </>
            ) : (
              <span className="text-muted-foreground">None yet. Generate one to turn SCIM on.</span>
            )}
          </span>
        </div>
        <ToggleRow
          id="scim-enabled"
          label="Enable SCIM"
          help={hasToken ? "Off refuses every SCIM request; the token is kept." : "Generate a token first."}
          checked={s.scim.enabled}
          disabled={busy || !hasToken}
          onChange={(v) => put(v, s.scim.default_role || "member")}
        />
        <Field label="Default role" hint="For provisioned members without a matching group mapping." className="max-w-xs">
          {(id, hint) => <RoleSelect id={id} hint={hint} value={s.scim.default_role || "member"} onChange={(r) => put(s.scim.enabled, r)} />}
        </Field>
      </div>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title="Rotate the SCIM token?"
        description="The current token stops working at once. Paste the new one into your identity provider right away."
        confirmLabel="Rotate token"
        success="New SCIM token created"
        onConfirm={rotate}
      />
      {secret && (
        <SecretDialog
          open
          onClose={() => setSecret(null)}
          title="SCIM token"
          description={
            <>
              Enter it as the bearer token (API token) in your IdP's SCIM app, with the base URL{" "}
              <span className="font-mono text-xs break-all">{secret.scim_base_url}</span>.
            </>
          }
          label="SCIM token"
          secret={secret.token}
          warning="This is the only time the token is shown. Only its hash is stored."
        />
      )}
    </Section>
  );
}

// ---- group mappings ----

function GroupMappings({ api, s, onSaved }: { api: IdAPI; s: SSOSettings; onSaved: () => void }) {
  const init = (s.group_mappings ?? []).map((m) => ({ ...m, role: (m.role || "member") as Role }));
  const [rows, setRows] = useState<GroupMapping[]>(init);
  // The default workspace cannot be mapped: every member is in it.
  const mappable = useSession().workspaces.filter((w) => !w.is_default);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const dirty = JSON.stringify(rows) !== JSON.stringify(init);
  const set = (i: number, p: Partial<GroupMapping>) => setRows((r) => r.map((x, j) => (j === i ? { ...x, ...p } : x)));

  async function save() {
    const clean = rows.map((r) => ({ ...r, group: r.group.trim(), workspace_id: r.workspace_id || undefined }));
    if (clean.some((r) => !r.group)) return setErr("Every mapping needs a group name.");
    // A group may have one organization mapping and one per workspace.
    const seen = new Set<string>();
    for (const r of clean) {
      const k = r.group.toLowerCase() + "\n" + (r.workspace_id ?? "");
      if (seen.has(k)) return setErr(`“${r.group}” is mapped twice to ${r.workspace_id ? "the same workspace" : "the organization"}.`);
      seen.add(k);
    }
    setBusy(true);
    setErr("");
    try {
      await api.putGroupMappings(clean);
      toast("Group mappings saved");
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Group → role mapping"
      description="A member in a mapped IdP group (SCIM group or SAML groups attribute) gets the highest mapped role. Owners keep their role. A mapping with a workspace sets the role in that workspace only; the organization role comes from mappings without one."
      actions={
        <Button variant="outline" size="sm" onClick={() => setRows((r) => [...r, { group: "", role: "member" }])}>
          <Plus aria-hidden /> Add mapping
        </Button>
      }
    >
      {rows.length === 0 ? (
        <p className="rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground">
          No mappings. Members get the default role of SAML or SCIM.
        </p>
      ) : (
        <TableFrame>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>IdP group (display name)</TableHead>
                <TableHead className="w-44">Role</TableHead>
                <TableHead className="w-44">Workspace</TableHead>
                <TableHead><span className="sr-only">Actions</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r, i) => (
                <TableRow key={i}>
                  <TableCell>
                    <Input aria-label={`Group name, row ${i + 1}`} value={r.group} onChange={(e) => set(i, { group: e.target.value })} placeholder="Polyrail Admins" autoComplete="off" />
                  </TableCell>
                  <TableCell>
                    <RoleSelect label={`Role for ${r.group || `row ${i + 1}`}`} value={(r.role || "member") as Role} onChange={(role) => set(i, { role })} />
                  </TableCell>
                  <TableCell>
                    <Select value={r.workspace_id || "org"} onValueChange={(v) => set(i, { workspace_id: v === "org" ? undefined : v, ...(v !== "org" && r.role === "owner" ? { role: "admin" as Role } : {}) })}>
                      <SelectTrigger aria-label={`Workspace for ${r.group || `row ${i + 1}`}`} className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="org">Whole organization</SelectItem>
                        {mappable.map((w) => (
                          <SelectItem key={w.id} value={w.id}>
                            {w.name}
                          </SelectItem>
                        ))}
                        {r.workspace_id && !mappable.some((w) => w.id === r.workspace_id) && (
                          <SelectItem value={r.workspace_id}>Deleted workspace ({r.workspace_id.slice(0, 6)})</SelectItem>
                        )}
                      </SelectContent>
                    </Select>
                  </TableCell>
                  <TableCell>
                    <Button size="icon" variant="ghost" onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))} aria-label={`Remove mapping ${r.group || i + 1}`}>
                      <Trash2 aria-hidden />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableFrame>
      )}
      <p className="mt-2 text-xs text-muted-foreground">
        The default workspace has no mappings: every member is in it with their organization role. Workspace roles are admin, member or viewer.
      </p>
      <div className="mt-3 grid gap-3">
        <FormError message={err} />
        <div className="flex gap-2">
          <Button disabled={busy || !dirty} onClick={save}>{busy ? "Saving…" : "Save mappings"}</Button>
          {dirty && <Button variant="ghost" disabled={busy} onClick={() => (setRows(init), setErr(""))}>Discard</Button>}
        </div>
      </div>
    </Section>
  );
}
