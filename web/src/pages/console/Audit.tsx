import type { Scope } from "@/App";
import { TableFrame } from "@/components/console/bits";
import { DataState, EmptyState, PageHeader } from "@/components/states";
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
                    <TableCell className="font-mono text-xs whitespace-nowrap">{e.action}</TableCell>
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
