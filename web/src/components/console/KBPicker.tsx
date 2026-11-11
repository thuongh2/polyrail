import { Checkbox } from "@/components/ui/checkbox";
import { kbLimits, type KB } from "@/lib/api";

/** Checkbox list of the org's knowledge bases, at most kbLimits.perRequest picked. */
export function KBPicker({
  legend, help, kbs, error, value, onChange,
}: { legend: string; help: string; kbs?: KB[]; error?: Error; value: string[]; onChange: (ids: string[]) => void }) {
  const max = kbLimits.perRequest;
  return (
    <fieldset className="grid gap-1.5">
      <legend className="mb-1.5 text-sm font-medium">{legend}</legend>
      <p className="text-xs text-muted-foreground">{value.length ? `${value.length} of ${max} selected` : help}</p>
      <div className="max-h-40 overflow-y-auto scrollbar-thin rounded-md border p-2">
        {error ? (
          <p className="text-sm text-destructive">{error.message}</p>
        ) : !kbs ? (
          <p className="text-sm text-muted-foreground">Loading knowledge bases…</p>
        ) : kbs.length === 0 ? (
          <p className="text-sm text-muted-foreground">No knowledge bases yet.</p>
        ) : (
          kbs.map((k) => {
            const on = value.includes(k.id);
            return (
              <label key={k.id} className="flex items-center gap-2 rounded px-1 py-1 text-sm hover:bg-accent">
                <Checkbox
                  checked={on}
                  disabled={!on && value.length >= max}
                  onCheckedChange={(v) => onChange(v ? [...value, k.id] : value.filter((x) => x !== k.id))}
                />
                <span>{k.name}</span>
              </label>
            );
          })
        )}
      </div>
    </fieldset>
  );
}
