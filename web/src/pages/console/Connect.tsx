import { useMemo, useState, type ReactNode } from "react";
import { Link } from "wouter";
import { Check, KeyRound, Plus } from "lucide-react";
import { CodeBlock, CopyButton } from "@/components/public/CodeTabs";
import { EmptyState, PageHeader } from "@/components/states";
import { toast } from "@/components/toast";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, get, orgBase, publicApi, type APIKey } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { cn } from "@/lib/utils";

/**
 * Connect: pick a tool and an API key, get the endpoint, key and exact config to paste.
 * Stored keys are only kept as hashes, so an existing key shows as its prefix and the snippets use
 * $GATEWAY_API_KEY; a key created here keeps its secret in this tab's memory so the snippets can
 * carry it (never saved anywhere).
 */
type Tool = {
  id: string;
  name: string;
  /** One line on where the settings go. */
  where: string;
  /** "openai" = …/v1, "anthropic" = …/anthropic, "gemini" = …/genai */
  api: "openai" | "anthropic" | "gemini";
  snippet: (c: { origin: string; key: string; model: string }) => string;
  lang?: string;
};

const TOOLS: Tool[] = [
  {
    id: "codex",
    name: "Codex",
    where: "Add a provider to ~/.codex/config.toml and export the key, then run codex.",
    api: "openai",
    snippet: ({ origin, key, model }) => `# ~/.codex/config.toml
model = "${model}"
model_provider = "polyrail"

[model_providers.polyrail]
name = "Polyrail"
base_url = "${origin}/v1"
env_key = "GATEWAY_API_KEY"
wire_api = "responses"

# shell
export GATEWAY_API_KEY="${key}"
codex`,
  },
  {
    id: "claude-code",
    name: "Claude Code",
    where: "Claude Code speaks the Anthropic API: point it at the gateway's /anthropic endpoint.",
    api: "anthropic",
    snippet: ({ origin, key, model }) => `export ANTHROPIC_BASE_URL="${origin}/anthropic"
export ANTHROPIC_AUTH_TOKEN="${key}"
export ANTHROPIC_MODEL="${model}"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="${model}"
claude`,
  },
  {
    id: "opencode",
    name: "OpenCode",
    where: "Add a provider to opencode.json (project) or ~/.config/opencode/opencode.json.",
    api: "openai",
    snippet: ({ origin, model }) => `{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "polyrail": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Polyrail",
      "options": { "baseURL": "${origin}/v1", "apiKey": "{env:GATEWAY_API_KEY}" },
      "models": { "${model}": { "name": "${model}" } }
    }
  },
  "model": "polyrail/${model}"
}`,
  },
  {
    id: "gemini-cli",
    name: "Gemini CLI",
    where: "Gemini CLI speaks the Gemini API: point it at the gateway's /genai endpoint.",
    api: "gemini",
    snippet: ({ origin, key, model }) => `export GOOGLE_GEMINI_BASE_URL="${origin}/genai"
export GEMINI_API_KEY="${key}"
gemini -m "${model}"`,
  },
  {
    id: "cline",
    name: "Cline",
    where: 'In Cline settings choose API Provider "OpenAI Compatible" and fill in these fields.',
    api: "openai",
    snippet: ({ origin, key, model }) => `API Provider:  OpenAI Compatible
Base URL:      ${origin}/v1
API Key:       ${key}
Model ID:      ${model}`,
  },
  {
    id: "open-webui",
    name: "Open WebUI",
    where: "Add an OpenAI API in Admin Panel → Settings → Connections with this URL and key.",
    api: "openai",
    snippet: ({ origin, key }) => `URL:  ${origin}/v1
Key:  ${key}`,
  },
  {
    id: "curl",
    name: "curl",
    where: "Any OpenAI-compatible client works with the same endpoint and key.",
    api: "openai",
    snippet: ({ origin, key, model }) => `curl ${origin}/v1/chat/completions \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${model}", "messages": [{"role": "user", "content": "Hello"}]}'`,
  },
  {
    id: "python",
    name: "Python",
    where: "The official openai package with the gateway as base_url.",
    api: "openai",
    snippet: ({ origin, key, model }) => `from openai import OpenAI

client = OpenAI(base_url="${origin}/v1", api_key="${key}")
r = client.chat.completions.create(model="${model}", messages=[{"role": "user", "content": "Hello"}])
print(r.choices[0].message.content)`,
  },
  {
    id: "typescript",
    name: "TypeScript",
    where: "The official openai package with the gateway as baseURL.",
    api: "openai",
    snippet: ({ origin, key, model }) => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${origin}/v1", apiKey: "${key}" });
const r = await client.chat.completions.create({ model: "${model}", messages: [{ role: "user", content: "Hello" }] });
console.log(r.choices[0].message.content);`,
  },
];

const NEW = "__new__";
const PLACEHOLDER = "YOUR_API_KEY";

export default function Connect() {
  const { org, wsList, wsBody, wsCan } = useSession();
  const base = org ? orgBase(org.id) : "";
  const keys = useAsync(() => (base ? get<APIKey[]>(wsList(`${base}/keys`)) : Promise.resolve([] as APIKey[])), [base]);
  const models = useAsync(() => publicApi.models(), []);
  const [toolId, setToolId] = useState(TOOLS[0].id);
  const [keyId, setKeyId] = useState<string>("");
  // Secrets of keys created on this page, by key id: memory only.
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [creating, setCreating] = useState(false);

  const active = useMemo(() => (keys.data ?? []).filter((k) => !k.revoked), [keys.data]);
  const key = active.find((k) => k.id === keyId) ?? active[0];
  const secret = key ? secrets[key.id] : undefined;
  const tool = TOOLS.find((t) => t.id === toolId) ?? TOOLS[0];
  const origin = location.origin;
  const endpoint = origin + (tool.api === "openai" ? "/v1" : tool.api === "anthropic" ? "/anthropic" : "/genai");
  const model = models.data?.find((m) => m.kind === "chat")?.id ?? "openai/gpt-4o-mini";
  const keyText = secret ?? PLACEHOLDER;
  const canCreate = wsCan("member");

  const createKey = async () => {
    setCreating(true);
    try {
      const r = await api<{ key: APIKey; secret: string }>("POST", `${base}/keys`, { ...wsBody, name: `${tool.name} (${new Date().toLocaleDateString()})` });
      setSecrets((s) => ({ ...s, [r.key.id]: r.secret }));
      setKeyId(r.key.id);
      keys.reload();
      toast(`Created “${r.key.name}”. Its secret is shown on this page until you leave it.`);
    } catch (e) {
      toast(e instanceof Error ? e.message : "Could not create the key", { error: true });
    } finally {
      setCreating(false);
    }
  };

  if (!org) return <EmptyState title="You are not in an organization yet">Ask an admin to add you, then sign in again.</EmptyState>;

  return (
    <>
      <PageHeader title="Connect" description="Use the gateway from coding agents, chat apps and SDKs: pick a tool and a key, then copy the settings." />
      <div className="grid gap-6 md:grid-cols-[12rem_minmax(0,1fr)]">
        <nav aria-label="Tools" className="no-scrollbar -mx-4 flex gap-1 overflow-x-auto px-4 md:mx-0 md:flex-col md:overflow-visible md:px-0">
          {TOOLS.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setToolId(t.id)}
              aria-current={t.id === tool.id ? "true" : undefined}
              className={cn(
                "h-9 shrink-0 rounded-md px-3 text-left text-sm whitespace-nowrap text-muted-foreground hover:bg-muted hover:text-foreground",
                "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
                t.id === tool.id && "bg-muted font-medium text-foreground",
              )}
            >
              {t.name}
            </button>
          ))}
        </nav>

        <section aria-label={tool.name} className="grid min-w-0 content-start gap-5">
          <div className="grid gap-2">
            <label htmlFor="connect-key" className="text-sm font-medium">
              Access key
            </label>
            <Select
              value={key?.id ?? ""}
              onValueChange={(v) => (v === NEW ? void createKey() : setKeyId(v))}
              disabled={keys.loading || creating}
            >
              <SelectTrigger id="connect-key" className="w-full justify-start [&>span]:flex-1 [&>span]:text-left">
                <KeyRound className="text-muted-foreground" aria-hidden />
                <SelectValue placeholder={keys.loading ? "Loading keys…" : "No API key yet"} />
              </SelectTrigger>
              <SelectContent>
                {active.map((k) => (
                  <SelectItem key={k.id} value={k.id}>
                    {k.name}
                    <span className="ml-2 font-mono text-xs text-muted-foreground">{k.prefix}…</span>
                  </SelectItem>
                ))}
                {canCreate && (
                  <>
                    {active.length > 0 && <SelectSeparator />}
                    <SelectItem value={NEW}>
                      <Plus aria-hidden /> Create a new key for {tool.name}
                    </SelectItem>
                  </>
                )}
              </SelectContent>
            </Select>
            {!secret && (
              <p className="text-xs text-muted-foreground">
                Keys are stored as hashes, so an existing key&apos;s secret can&apos;t be shown again: put the one you saved where the configuration says YOUR_API_KEY, or create a new key here to fill it in.{" "}
                <Link href="/keys" className="underline underline-offset-4">
                  Manage keys
                </Link>
              </p>
            )}
          </div>

          <div className="grid gap-3 rounded-lg border p-4">
            <p className="text-sm text-muted-foreground">{tool.where}</p>
            <Field label="Endpoint" value={endpoint} />
            <Field
              label="API key"
              value={secret ?? (key ? `${key.prefix}…` : "Create a key first")}
              copy={secret}
              hint={secret ? (
                <span className="inline-flex items-center gap-1 text-xs text-emerald-700 dark:text-emerald-400">
                  <Check className="size-3.5" aria-hidden /> New key: copy it now, it won&apos;t be shown again after you leave this page.
                </span>
              ) : undefined}
            />
            <Field label="Model" value={model} />
          </div>

          <div className="grid gap-2">
            <h2 className="text-sm font-medium">Configuration</h2>
            <CodeBlock code={tool.snippet({ origin, key: keyText, model })} />
            <p className="text-xs text-muted-foreground">
              Any model id from <Link href="/models" className="underline underline-offset-4">Models</Link> works in place of {model}.
            </p>
          </div>
        </section>
      </div>
    </>
  );
}

function Field({ label, value, copy, hint }: { label: string; value: string; copy?: string; hint?: ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[6rem_minmax(0,1fr)] sm:items-center">
      <span className="text-sm text-muted-foreground">{label}</span>
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-1 rounded-md border bg-muted/40 px-3 py-1.5">
          <code className="min-w-0 flex-1 truncate font-mono text-sm" title={value}>
            {value}
          </code>
          {(copy ?? (label !== "API key" ? value : undefined)) && <CopyButton text={copy ?? value} label={`Copy ${label.toLowerCase()}`} />}
        </div>
        {hint}
      </div>
    </div>
  );
}

