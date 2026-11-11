import { useMemo } from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { checkSchema, parseJSON } from "./lib";

/** One JSON value; objects and arrays fold with <details> (open to depth 2). */
function Node({ k, v, depth, last }: { k?: string; v: unknown; depth: number; last: boolean }) {
  const key = k !== undefined && <span className="text-foreground">{JSON.stringify(k)}: </span>;
  const comma = last ? "" : ",";
  if (v && typeof v === "object") {
    const arr = Array.isArray(v);
    const entries = arr ? (v as unknown[]).map((x, i) => [String(i), x] as const) : Object.entries(v as object);
    const [open, close] = arr ? ["[", "]"] : ["{", "}"];
    if (!entries.length)
      return (
        <div>
          {key}
          <span className="text-muted-foreground">
            {open}
            {close}
            {comma}
          </span>
        </div>
      );
    return (
      <details open={depth < 2} className="group">
        <summary className="cursor-pointer list-none rounded-sm marker:hidden hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring [&::-webkit-details-marker]:hidden">
          <span aria-hidden className="inline-block w-3 text-muted-foreground group-open:rotate-90">
            ›
          </span>
          {key}
          <span className="text-muted-foreground">{open}</span>
          <span className="text-muted-foreground group-open:hidden">
            {" "}
            {entries.length} {arr ? (entries.length === 1 ? "item" : "items") : entries.length === 1 ? "key" : "keys"} {close}
            {comma}
          </span>
        </summary>
        <div className="ml-1.5 border-l pl-3">
          {entries.map(([ek, ev], i) => (
            <Node key={ek} k={arr ? undefined : ek} v={ev} depth={depth + 1} last={i === entries.length - 1} />
          ))}
        </div>
        <span className="text-muted-foreground">
          {close}
          {comma}
        </span>
      </details>
    );
  }
  const cls = typeof v === "string" ? "text-success" : typeof v === "number" ? "text-signal" : "font-medium text-foreground";
  return (
    <div className="pl-3">
      {key}
      <span className={cn("break-all", cls)}>{JSON.stringify(v)}</span>
      <span className="text-muted-foreground">{comma}</span>
    </div>
  );
}

/** Status of a JSON answer: parse result and, with a schema, the basic schema check. */
export function useJsonCheck(text: string, schema: unknown) {
  return useMemo(() => {
    const r = parseJSON(stripFence(text));
    if (!r.ok) return { ok: false as const, error: r.error };
    return { ok: true as const, value: r.value, problems: schema ? checkSchema(r.value, schema) : undefined };
  }, [text, schema]);
}

/** Models sometimes wrap JSON in a ```json fence even in JSON mode. */
export const stripFence = (t: string) => t.trim().replace(/^```(?:json)?\s*\n([\s\S]*?)\n?```$/i, "$1");

export function JsonBadge({ check }: { check: ReturnType<typeof useJsonCheck> }) {
  if (!check.ok)
    return (
      <Badge variant="outline" className="border-danger/50 text-danger" title={check.error}>
        Invalid JSON
      </Badge>
    );
  if (!check.problems) return <Badge variant="outline" className="text-success">Valid JSON</Badge>;
  if (!check.problems.length) return <Badge variant="outline" className="text-success">Matches schema</Badge>;
  return (
    <Badge variant="outline" className="border-warning/60 text-foreground" title={check.problems.join("\n")}>
      Schema mismatch ({check.problems.length})
    </Badge>
  );
}

export function JsonView({ check }: { check: ReturnType<typeof useJsonCheck> }) {
  if (!check.ok) return <p className="text-sm text-danger">Not valid JSON: {check.error}</p>;
  return (
    <div className="grid gap-2">
      <div className="scrollbar-thin overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-[0.8125rem] leading-relaxed">
        <Node v={check.value} depth={0} last />
      </div>
      {!!check.problems?.length && (
        <ul className="list-disc pl-5 text-xs text-muted-foreground">
          {check.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
