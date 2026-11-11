import { useMemo, useState, type ReactNode } from "react";
import { Search, SlidersHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DataState, EmptyState, PageHeader } from "@/components/states";
import { ModelListHead, ModelRow } from "@/components/public/ModelRow";
import { publicApi, type ModelInfo } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const ANY = "any";

const contextOptions = [
  { v: ANY, label: "Any" },
  { v: "8000", label: "8K or more" },
  { v: "32000", label: "32K or more" },
  { v: "128000", label: "128K or more" },
  { v: "200000", label: "200K or more" },
];

const priceOptions = [
  { v: ANY, label: "Any" },
  { v: "0", label: "Free only" },
  { v: "0.5", label: "Up to $0.50" },
  { v: "1", label: "Up to $1" },
  { v: "5", label: "Up to $5" },
  { v: "10", label: "Up to $10" },
];

const sorts = [
  { v: "newest", label: "Newest" },
  { v: "name", label: "Name" },
  { v: "price", label: "Price, low to high" },
  { v: "context", label: "Context, largest" },
];

export default function Models() {
  const state = useAsync(publicApi.models);
  const [q, setQ] = useQueryParam("q");
  const [kind, setKind] = useQueryParam("kind");
  const [modality, setModality] = useQueryParam("modality");
  const [ctx, setCtx] = useQueryParam("context", ANY);
  const [price, setPrice] = useQueryParam("max_price", ANY);
  const [provider, setProvider] = useQueryParam("provider", ANY);
  const [sort, setSort] = useQueryParam("sort", "newest");
  const [open, setOpen] = useState(false);

  const all = state.data ?? [];
  const providers = useMemo(() => [...new Set(all.flatMap((m) => m.providers ?? []))].sort(), [all]);
  const active = [kind, modality, ctx !== ANY, price !== ANY, provider !== ANY].filter(Boolean).length;

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const out = all.filter(
      (m) =>
        (!needle || [m.id, m.name, m.description ?? "", ...(m.providers ?? [])].some((s) => s.toLowerCase().includes(needle))) &&
        (!kind || m.kind === kind) &&
        (!modality || (m.modalities ?? ["text"]).includes(modality)) &&
        (ctx === ANY || (m.context_length ?? 0) >= Number(ctx)) &&
        (price === ANY || Number(m.pricing.input) <= Number(price)) &&
        (provider === ANY || (m.providers ?? []).includes(provider)),
    );
    const cmp: Record<string, (a: ModelInfo, b: ModelInfo) => number> = {
      newest: (a, b) => b.created - a.created || a.name.localeCompare(b.name),
      name: (a, b) => a.name.localeCompare(b.name),
      price: (a, b) => Number(a.pricing.input) - Number(b.pricing.input) || Number(a.pricing.output) - Number(b.pricing.output),
      context: (a, b) => (b.context_length ?? 0) - (a.context_length ?? 0),
    };
    return out.sort(cmp[sort] ?? cmp.newest);
  }, [all, q, kind, modality, ctx, price, provider, sort]);

  const reset = () => {
    setKind("");
    setModality("");
    setCtx(ANY);
    setPrice(ANY);
    setProvider(ANY);
  };

  return (
    <>
      <PageHeader
        title="Models"
        description="Every public model on this gateway. Prices are USD per 1M tokens; call any of them by id through /v1."
      />
      <div className="grid gap-6 lg:grid-cols-[15rem_minmax(0,1fr)] lg:gap-8">
        <div className="lg:hidden">
          <Button variant="outline" size="sm" aria-expanded={open} aria-controls="model-filters" onClick={() => setOpen((o) => !o)}>
            <SlidersHorizontal />
            Filters{active ? ` (${active})` : ""}
          </Button>
        </div>
        <aside id="model-filters" aria-label="Filters" className={cn("space-y-6 lg:block", !open && "hidden")}>
          <Group label="Kind">
            <Segmented
              value={kind}
              onChange={setKind}
              options={[
                ["", "All"],
                ["chat", "Chat"],
                ["embedding", "Embedding"],
              ]}
            />
          </Group>
          <Group label="Input modality">
            <Segmented
              value={modality}
              onChange={setModality}
              options={[
                ["", "Any"],
                ["text", "Text"],
                ["image", "Image"],
              ]}
            />
          </Group>
          <SelectFilter id="f-context" label="Context length" value={ctx} onChange={setCtx} options={contextOptions} />
          <SelectFilter id="f-price" label="Input price per 1M" value={price} onChange={setPrice} options={priceOptions} />
          <SelectFilter
            id="f-provider"
            label="Provider"
            value={provider}
            onChange={setProvider}
            options={[{ v: ANY, label: "Any" }, ...providers.map((p) => ({ v: p, label: p }))]}
          />
          {active > 0 && (
            <Button variant="ghost" size="sm" onClick={reset}>
              Clear filters
            </Button>
          )}
        </aside>

        <section aria-label="Model list" className="min-w-0">
          <div className="mb-4 flex flex-wrap items-center gap-3">
            <div className="relative min-w-48 flex-1">
              <Search aria-hidden className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                type="search"
                aria-label="Search models"
                placeholder="Search by name, id or provider"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                className="pl-9"
              />
            </div>
            <div className="flex items-center gap-2">
              <Label htmlFor="f-sort" className="text-sm text-muted-foreground">
                Sort
              </Label>
              <Select value={sort} onValueChange={setSort}>
                <SelectTrigger id="f-sort" className="w-44">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {sorts.map((s) => (
                    <SelectItem key={s.v} value={s.v}>
                      {s.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DataState
            state={state}
            rows={6}
            empty={<EmptyState title="No public models yet">An operator adds them in the console under Platform, Models.</EmptyState>}
          >
            {() =>
              shown.length === 0 ? (
                <EmptyState
                  title="No models match"
                  action={
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        reset();
                        setQ("");
                      }}
                    >
                      Clear search and filters
                    </Button>
                  }
                >
                  Try a shorter search or fewer filters.
                </EmptyState>
              ) : (
                <>
                  <p className="mb-2 text-sm text-muted-foreground" aria-live="polite">
                    {shown.length === all.length ? `${all.length} models` : `${shown.length} of ${all.length} models`}
                  </p>
                  <div className="rounded-lg border">
                    <ModelListHead />
                    <ul className="divide-y">
                      {shown.map((m) => (
                        <ModelRow key={m.id} m={m} />
                      ))}
                    </ul>
                  </div>
                </>
              )
            }
          </DataState>
        </section>
      </div>
    </>
  );
}

function Group({ label, children }: { label: string; children: ReactNode }) {
  return (
    <fieldset>
      <legend className="mb-2 text-sm font-medium">{label}</legend>
      {children}
    </fieldset>
  );
}

function Segmented({ value, onChange, options }: { value: string; onChange: (v: string) => void; options: [string, string][] }) {
  return (
    <div className="flex rounded-md border p-0.5">
      {options.map(([v, label]) => (
        <button
          key={v}
          type="button"
          aria-pressed={value === v}
          onClick={() => onChange(v)}
          className={cn(
            "flex-1 rounded-[5px] px-2 py-1 text-sm text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring",
            value === v && "bg-accent font-medium text-foreground",
          )}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

function SelectFilter({
  id,
  label,
  value,
  onChange,
  options,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: { v: string; label: string }[];
}) {
  return (
    <div>
      <Label htmlFor={id} className="mb-2 text-sm font-medium">
        {label}
      </Label>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((o) => (
            <SelectItem key={o.v} value={o.v}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  );
}
