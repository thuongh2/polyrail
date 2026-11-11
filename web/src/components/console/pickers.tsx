// Multi-value form controls shared by the policy and preset editors.
import { useId, useState, type KeyboardEvent, type ReactNode } from "react";
import { X } from "lucide-react";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { get, publicApi, type Provider } from "@/lib/api";
import { providerLabel } from "@/lib/providers";

export type Option = { value: string; label: string; hint?: string };

/** A bordered, scrollable checkbox list with a text filter once it is long. */
export function CheckList({
  legend, help, options, value, onChange, error, loading, empty = "Nothing to choose from yet.", mono,
}: {
  legend: string;
  help?: ReactNode;
  options?: Option[];
  value: string[];
  onChange: (v: string[]) => void;
  error?: Error;
  loading?: boolean;
  empty?: string;
  mono?: boolean;
}) {
  const id = useId();
  const [q, setQ] = useState("");
  const list = options ?? [];
  // Keep stored values that are no longer offered visible, so the form shows the truth.
  const all = [...list, ...value.filter((v) => !list.some((o) => o.value === v)).map((v) => ({ value: v, label: v, hint: "not found" }))];
  const shown = q ? all.filter((o) => (o.label + " " + o.value).toLowerCase().includes(q.toLowerCase())) : all;
  return (
    <fieldset className="grid min-w-0 gap-1.5">
      <legend className="mb-1.5 text-sm font-medium">{legend}</legend>
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
      {all.length > 8 && (
        <Input aria-label={`Filter ${legend.toLowerCase()}`} placeholder="Filter…" value={q} onChange={(e) => setQ(e.target.value)} className="h-8" />
      )}
      <div className="max-h-40 overflow-y-auto scrollbar-thin rounded-md border p-2">
        {error ? (
          <p className="text-sm text-destructive">{error.message}</p>
        ) : loading && !options ? (
          <p className="text-sm text-muted-foreground">Loading…</p>
        ) : shown.length === 0 ? (
          <p className="text-sm text-muted-foreground">{q ? "No match." : empty}</p>
        ) : (
          shown.map((o) => (
            <label key={o.value} htmlFor={`${id}-${o.value}`} className="flex items-center gap-2 rounded px-1 py-1 text-sm hover:bg-accent">
              <Checkbox
                id={`${id}-${o.value}`}
                checked={value.includes(o.value)}
                onCheckedChange={(c) => onChange(c ? [...value, o.value] : value.filter((x) => x !== o.value))}
              />
              <span className={cn("min-w-0 truncate", mono && "font-mono text-xs")}>{o.label}</span>
              {o.hint && <span className="ml-auto shrink-0 text-xs text-muted-foreground">{o.hint}</span>}
            </label>
          ))
        )}
      </div>
      {value.length > 0 && <p className="text-xs text-muted-foreground">{value.length} selected</p>}
    </fieldset>
  );
}

/** Free-text values as removable chips: type and press Enter (or comma) to add. */
export function ChipInput({
  label, help, value, onChange, placeholder, suggestions, mono = true,
}: {
  label: string;
  help?: ReactNode;
  value: string[];
  onChange: (v: string[]) => void;
  placeholder?: string;
  suggestions?: string[];
  mono?: boolean;
}) {
  const id = useId();
  const [text, setText] = useState("");
  const add = (raw: string) => {
    const parts = raw.split(",").map((s) => s.trim()).filter((s) => s && !value.includes(s));
    if (parts.length) onChange([...value, ...parts]);
    setText("");
  };
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add(text);
    } else if (e.key === "Backspace" && !text && value.length) {
      onChange(value.slice(0, -1));
    }
  };
  return (
    <div className="grid min-w-0 gap-1.5">
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <div className="flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border px-2 py-1.5 focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50">
        {value.map((v, i) => (
          <span key={v} className="inline-flex max-w-full items-center gap-1 rounded-md bg-secondary px-1.5 py-0.5 text-xs">
            {value.length > 1 && <span className="text-muted-foreground tabular-nums">{i + 1}.</span>}
            <span className={cn("truncate", mono && "font-mono")}>{v}</span>
            <button
              type="button"
              className="rounded-sm text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
              aria-label={`Remove ${v}`}
              onClick={() => onChange(value.filter((x) => x !== v))}
            >
              <X className="size-3" aria-hidden />
            </button>
          </span>
        ))}
        <input
          id={id}
          list={suggestions ? id + "-list" : undefined}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKey}
          onBlur={() => text.trim() && add(text)}
          placeholder={value.length ? "" : placeholder}
          className={cn("min-w-32 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground", mono && "font-mono text-xs")}
          autoComplete="off"
          spellCheck={false}
          aria-describedby={help ? id + "-help" : undefined}
        />
        {suggestions && (
          <datalist id={id + "-list"}>
            {suggestions.map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
        )}
      </div>
      {help && (
        <p id={id + "-help"} className="text-xs text-muted-foreground">
          {help}
        </p>
      )}
    </div>
  );
}

/** Switch row: label and help on the left, control on the right (the Settings pattern). */
export function SwitchRow({ id, label, help, children }: { id: string; label: string; help?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
      <div className="min-w-0">
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        {help && <p className="text-xs text-muted-foreground">{help}</p>}
      </div>
      {children}
    </div>
  );
}

/**
 * Provider names and types a policy or preset can name: the org's own providers (admin API) plus
 * the public providers from the status page. Entries match case-insensitively on the server.
 */
export async function providerOptions(base: string): Promise<Option[]> {
  const [own, pub] = await Promise.all([
    get<Provider[]>(`${base}/providers`).catch(() => [] as Provider[]),
    publicApi.status().then((s) => s.providers).catch(() => []),
  ]);
  const out: Option[] = [];
  const seen = new Set<string>();
  const push = (o: Option) => !seen.has(o.value.toLowerCase()) && (seen.add(o.value.toLowerCase()), out.push(o));
  for (const t of [...new Set([...own, ...pub].map((p) => p.type))].sort()) push({ value: t, label: providerLabel(t), hint: "all of this type" });
  for (const p of own) push({ value: p.name, label: p.name, hint: "private" });
  for (const p of pub) push({ value: p.name, label: p.name, hint: "public" });
  return out;
}

/** A titled group inside a form dialog: hairline above, small heading, no extra outer margin. */
export function Part({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="grid gap-3 border-t pt-4">
      <div>
        <h3 className="text-sm font-semibold">{title}</h3>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
      {children}
    </section>
  );
}
