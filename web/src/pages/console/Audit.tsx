import type { Scope } from "@/App";
import { TableFrame } from "@/components/console/bits";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { PLATFORM, get, orgBase, when, type AuditEntry } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

export default function Audit({ scope = "org" }: { scope?: Scope }) {
  const { org } = useSession();
  const platform = scope === "platform";
  const base = platform ? PLATFORM : org ? orgBase(org.id) : "";
  const log = useAsync(() => get<AuditEntry[]>(`${base}/audit`), [base]);
  if (!platform && !org) return <EmptyState title="You are not in an organization yet" />;

  return (
    <>
      <PageHeader
        title={platform ? "Platform audit log" : "Audit log"}
        description={
          platform
            ? "Changes to public providers, models, users and organizations. The latest 200 entries."
            : "Every change made in this organization: keys, members, providers, models and settings. The latest 200 entries."
        }
      />
      <DataState state={log} rows={6} empty={<EmptyState title="Nothing recorded yet">Changes appear here as soon as someone makes one.</EmptyState>}>
        {(rows) => (
          <TableFrame>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Detail</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((e) => (
                  <TableRow key={e.id}>
                    <TableCell className="whitespace-nowrap text-muted-foreground tabular-nums">{when(e.at)}</TableCell>
                    <TableCell>{e.actor || <span className="text-muted-foreground">system</span>}</TableCell>
                    <TableCell className="whitespace-nowrap">
                      <ActionBadge action={e.action} />
                    </TableCell>
                    <TableCell className="max-w-56 truncate font-mono text-xs" title={e.target}>
                      {e.target}
                    </TableCell>
                    <TableCell className="max-w-80 truncate text-muted-foreground" title={e.detail}>
                      {e.detail || "–"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableFrame>
        )}
      </DataState>
    </>
  );
}

// "subject.verb" (key.create, provider.delete, …) as a badge colored by what the verb does.
const tone: [RegExp, string][] = [
  [/(delete|revoke|remove|disable|deprovision|quarantine)/, "border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300"],
  [/(create|add|import|invite|enable|verify|seed|sync)/, "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"],
  [/(update|patch|rotate|set|reindex|test|clear)/, "border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300"],
];

function ActionBadge({ action }: { action: string }) {
  const i = action.lastIndexOf(".");
  const subject = i > 0 ? action.slice(0, i) : "";
  const verb = i > 0 ? action.slice(i + 1) : action;
  const cls = tone.find(([re]) => re.test(verb))?.[1] ?? "";
  return (
    <Badge variant="outline" className={"gap-1 font-mono text-xs font-normal " + cls} title={action}>
      {subject && <span className="opacity-70">{subject}.</span>}
      <span className="font-medium">{verb}</span>
    </Badge>
  );
}
