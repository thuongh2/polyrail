import type { ReactNode } from "react";
import { ChevronRight, X } from "lucide-react";
import { Field } from "@/components/console/bits";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { ModelView, Preset } from "@/lib/api";
import { exampleSchema, exampleTools, parseJSON, parseTools, type Params } from "./lib";

/** A collapsible group of controls (native <details>: keyboard and screen-reader friendly). */
export function Group({ title, summary, open, children }: { title: string; summary?: string; open?: boolean; children: ReactNode }) {
  return (
    <details open={open} className="group/g rounded-lg border">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-lg px-3 py-2 text-sm font-medium hover:bg-accent focus-visible:outline-2 focus-visible:outline-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-4 text-muted-foreground group-open/g:rotate-90 motion-safe:transition-transform" />
        {title}
        {summary && <span className="ml-auto truncate text-xs font-normal text-muted-foreground">{summary}</span>}
      </summary>
      <div className="grid gap-4 border-t px-3 py-3">{children}</div>
    </details>
  );
}

const PRESET = "@preset/";

type ModelPickerProps = { models: { data?: ModelView[]; error?: Error; reload: () => void }; presets: Preset[]; value: string; onChange: (v: string) => void };

export function ModelPicker({ models, presets, value, onChange }: ModelPickerProps) {
  const list = models.data?.filter((m) => m.kind === "chat");
  return (
    <Field id="pg-model" label="Model" help={value.startsWith(PRESET) ? "A preset brings its own model, fallbacks, routing and parameters; settings here win." : undefined}>
      {models.error ? (
        <ErrorState error={models.error} onRetry={models.reload} />
      ) : !list ? (
        <Skeleton className="h-9 w-full" />
      ) : list.length === 0 && presets.length === 0 ? (
        <p className="text-sm text-muted-foreground">No chat models are enabled for this organization.</p>
      ) : (
        <Select value={value} onValueChange={onChange}>
          <SelectTrigger id="pg-model" className="w-full" aria-describedby={value.startsWith(PRESET) ? "pg-model-help" : undefined}>
            <SelectValue placeholder="Choose a model" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectLabel>Models</SelectLabel>
              {list.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  {m.name}
                </SelectItem>
              ))}
            </SelectGroup>
            {presets.length > 0 && (
              <SelectGroup>
                <SelectLabel>Presets</SelectLabel>
                {presets.map((p) => (
                  <SelectItem key={p.id} value={PRESET + p.slug}>
                    {PRESET + p.slug}
                  </SelectItem>
                ))}
              </SelectGroup>
            )}
          </SelectContent>
        </Select>
      )}
    </Field>
  );
}

type SetP = (patch: Partial<Params>) => void;

/** Fallback models (`models`, at most 5) and provider routing shortcuts (D27). */
export function RoutingGroup({ p, set, models, primary }: { p: Params; set: SetP; models: ModelView[]; primary: string }) {
  const options = models.filter((m) => m.kind === "chat" && m.id !== primary && !p.fallbacks.includes(m.id));
  const summary = [p.fallbacks.length ? `${p.fallbacks.length} fallback${p.fallbacks.length > 1 ? "s" : ""}` : "", p.sort && `by ${p.sort}`, p.zdr && "ZDR"].filter(Boolean).join(", ");
  return (
    <Group title="Routing" summary={summary || "Default"}>
      <div className="grid gap-1.5">
        <Label htmlFor="pg-fallback">Fallback models</Label>
        {p.fallbacks.length > 0 && (
          <ol className="grid gap-1">
            {p.fallbacks.map((id, i) => (
              <li key={id} className="flex items-center gap-2 rounded-md border px-2 py-1 text-sm">
                <span className="text-xs text-muted-foreground tabular-nums">{i + 1}.</span>
                <span className="min-w-0 flex-1 truncate">{id}</span>
                <Button type="button" variant="ghost" size="icon-xs" aria-label={`Remove fallback ${id}`} onClick={() => set({ fallbacks: p.fallbacks.filter((x) => x !== id) })}>
                  <X aria-hidden />
                </Button>
              </li>
            ))}
          </ol>
        )}
        <Select value="" onValueChange={(v) => set({ fallbacks: [...p.fallbacks, v] })} disabled={p.fallbacks.length >= 5 || options.length === 0}>
          <SelectTrigger id="pg-fallback" className="w-full" aria-describedby="pg-fallback-help">
            <SelectValue placeholder={p.fallbacks.length >= 5 ? "At most 5 fallbacks" : "Add a fallback model"} />
          </SelectTrigger>
          <SelectContent>
            {options.map((m) => (
              <SelectItem key={m.id} value={m.id}>
                {m.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p id="pg-fallback-help" className="text-xs text-muted-foreground">
          Sent as <code>models</code>: tried in order when the model fails.
        </p>
      </div>
      <Field id="pg-sort" label="Provider order">
        <Select value={p.sort || "default"} onValueChange={(v) => set({ sort: v === "default" ? "" : v })}>
          <SelectTrigger id="pg-sort" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">Model's strategy</SelectItem>
            <SelectItem value="price">Lowest price first</SelectItem>
            <SelectItem value="latency">Lowest latency first</SelectItem>
            <SelectItem value="throughput">Highest throughput first</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      <div className="flex items-start justify-between gap-3">
        <div>
          <Label htmlFor="pg-zdr">Zero data retention only</Label>
          <p className="text-xs text-muted-foreground">Only providers declared ZDR (<code>provider.zdr</code>).</p>
        </div>
        <Switch id="pg-zdr" checked={p.zdr} onCheckedChange={(v) => set({ zdr: v })} />
      </div>
    </Group>
  );
}

function Num({ id, label, value, onChange, min, max, step, help }: { id: string; label: string; value: string; onChange: (v: string) => void; min: number; max?: number; step: number; help?: string }) {
  return (
    <Field id={id} label={label} help={help}>
      <Input id={id} type="number" inputMode="decimal" min={min} max={max} step={step} placeholder="Default" value={value} onChange={(e) => onChange(e.target.value)} aria-describedby={help ? id + "-help" : undefined} />
    </Field>
  );
}

export function ParamsGroup({ p, set, problems }: { p: Params; set: SetP; problems: string[] }) {
  const changed = (["temperature", "top_p", "max_tokens", "seed", "frequency_penalty", "presence_penalty", "stop"] as const).filter((k) => p[k].trim()).length + (p.reasoning ? 1 : 0);
  return (
    <Group title="Parameters" summary={`${changed ? `${changed} set` : "Defaults"}${p.stream ? "" : ", no stream"}`}>
      <p className="-mt-1 text-xs text-muted-foreground">Empty fields are not sent, so the model's (or preset's) default applies.</p>
      <div className="grid grid-cols-2 gap-3">
        <Num id="pg-temp" label="Temperature" value={p.temperature} onChange={(v) => set({ temperature: v })} min={0} max={2} step={0.1} />
        <Num id="pg-topp" label="Top P" value={p.top_p} onChange={(v) => set({ top_p: v })} min={0} max={1} step={0.05} />
        <Num id="pg-max" label="Max tokens" value={p.max_tokens} onChange={(v) => set({ max_tokens: v })} min={1} step={1} />
        <Num id="pg-seed" label="Seed" value={p.seed} onChange={(v) => set({ seed: v })} min={0} step={1} />
        <Num id="pg-freq" label="Frequency penalty" value={p.frequency_penalty} onChange={(v) => set({ frequency_penalty: v })} min={-2} max={2} step={0.1} />
        <Num id="pg-pres" label="Presence penalty" value={p.presence_penalty} onChange={(v) => set({ presence_penalty: v })} min={-2} max={2} step={0.1} />
      </div>
      <Field id="pg-stop" label="Stop sequences" help="One per line, up to 4.">
        <Textarea id="pg-stop" rows={2} value={p.stop} onChange={(e) => set({ stop: e.target.value })} aria-describedby="pg-stop-help" spellCheck={false} />
      </Field>
      <Field id="pg-reason" label="Reasoning effort" help="Sent as reasoning_effort. Models without reasoning ignore or reject it.">
        <Select value={p.reasoning || "default"} onValueChange={(v) => set({ reasoning: v === "default" ? "" : v })}>
          <SelectTrigger id="pg-reason" className="w-full" aria-describedby="pg-reason-help">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">Not sent</SelectItem>
            {["minimal", "low", "medium", "high"].map((v) => (
              <SelectItem key={v} value={v}>
                {v[0].toUpperCase() + v.slice(1)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <div className="flex items-center justify-between gap-3">
        <Label htmlFor="pg-stream">Stream the answer</Label>
        <Switch id="pg-stream" checked={p.stream} onCheckedChange={(v) => set({ stream: v })} />
      </div>
      <Problems list={problems.filter((x) => !x.startsWith("JSON schema") && !x.startsWith("Schema name") && !x.startsWith("Tools"))} />
    </Group>
  );
}

function Problems({ list }: { list: string[] }) {
  if (!list.length) return null;
  return (
    <ul role="alert" className="grid gap-0.5 text-xs text-danger">
      {list.map((x) => (
        <li key={x}>{x}</li>
      ))}
    </ul>
  );
}

const formatLabel = { text: "Text", json_object: "JSON object", json_schema: "JSON schema" } as const;

export function FormatGroup({ p, set }: { p: Params; set: SetP }) {
  const parsed = p.format === "json_schema" ? parseJSON(p.schema) : null;
  const nameOk = /^[a-zA-Z0-9_-]{1,64}$/.test(p.schemaName);
  return (
    <Group title="Response format" summary={formatLabel[p.format]}>
      <Field id="pg-format" label="Format">
        <Select value={p.format} onValueChange={(v) => set({ format: v as Params["format"], ...(v === "json_schema" && !p.schema.trim() ? { schema: exampleSchema } : {}) })}>
          <SelectTrigger id="pg-format" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="text">Text</SelectItem>
            <SelectItem value="json_object">JSON object</SelectItem>
            <SelectItem value="json_schema">JSON schema</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {p.format === "json_object" && <p className="text-xs text-muted-foreground">Most providers also need the word "JSON" in the prompt.</p>}
      {p.format === "json_schema" && (
        <>
          <Field id="pg-schema-name" label="Schema name">
            <Input id="pg-schema-name" value={p.schemaName} onChange={(e) => set({ schemaName: e.target.value })} spellCheck={false} aria-invalid={!nameOk || undefined} />
          </Field>
          <div className="grid gap-1.5">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="pg-schema">Schema</Label>
              <Button type="button" variant="ghost" size="xs" onClick={() => set({ schema: exampleSchema })}>
                Insert example
              </Button>
            </div>
            <Textarea
              id="pg-schema"
              rows={9}
              className="font-mono text-xs"
              spellCheck={false}
              value={p.schema}
              onChange={(e) => set({ schema: e.target.value })}
              aria-invalid={parsed?.ok === false || undefined}
              aria-describedby="pg-schema-status"
            />
            <p id="pg-schema-status" className={parsed?.ok === false ? "text-xs text-danger" : "text-xs text-muted-foreground"}>
              {parsed?.ok === false ? `Invalid JSON: ${parsed.error}` : "Valid JSON. Answers are checked against type, required, properties, items and enum."}
            </p>
          </div>
          <div className="flex items-center justify-between gap-3">
            <Label htmlFor="pg-strict">Strict</Label>
            <Switch id="pg-strict" checked={p.strict} onCheckedChange={(v) => set({ strict: v })} />
          </div>
        </>
      )}
    </Group>
  );
}

export function ToolsGroup({ p, set }: { p: Params; set: SetP }) {
  const parsed = parseTools(p.tools);
  const names = parsed.tools?.map((t) => t.function.name) ?? [];
  const choice = p.toolChoice.startsWith("fn:") && !names.includes(p.toolChoice.slice(3)) ? "auto" : p.toolChoice;
  return (
    <Group title="Tools" summary={names.length ? `${names.length} tool${names.length > 1 ? "s" : ""}` : "None"}>
      <div className="grid gap-1.5">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor="pg-tools">Definitions (JSON array)</Label>
          <Button type="button" variant="ghost" size="xs" onClick={() => set({ tools: exampleTools })}>
            Insert sample
          </Button>
        </div>
        <Textarea
          id="pg-tools"
          rows={8}
          className="font-mono text-xs"
          spellCheck={false}
          placeholder='[{"type": "function", "function": {"name": "…", "parameters": {…}}}]'
          value={p.tools}
          onChange={(e) => set({ tools: e.target.value })}
          aria-invalid={!!parsed.error || undefined}
          aria-describedby="pg-tools-status"
        />
        <p id="pg-tools-status" className={parsed.error ? "text-xs text-danger" : "text-xs text-muted-foreground"}>
          {parsed.error ?? (names.length ? `Offered: ${names.join(", ")}` : "OpenAI function tools. MCP tools granted to your key are added by the gateway.")}
        </p>
      </div>
      {names.length > 0 && (
        <Field id="pg-tool-choice" label="Tool choice">
          <Select value={choice} onValueChange={(v) => set({ toolChoice: v })}>
            <SelectTrigger id="pg-tool-choice" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto">Auto</SelectItem>
              <SelectItem value="required">Required</SelectItem>
              <SelectItem value="none">None</SelectItem>
              {names.map((n) => (
                <SelectItem key={n} value={`fn:${n}`}>
                  Only {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    </Group>
  );
}
