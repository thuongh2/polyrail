import { useState, type ReactNode } from "react";
import { Link, useLocation } from "wouter";
import { Search } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { DataState, EmptyState } from "@/components/states";
import { apiBase, CodeTabs, CopyButton, requestSamples } from "@/components/public/CodeTabs";
import { ModelListHead, ModelRow } from "@/components/public/ModelRow";
import { publicApi, type ModelInfo } from "@/lib/api";
import { useAsync } from "@/lib/hooks";
import { useSession } from "@/lib/session";

export default function Home() {
  const models = useAsync(publicApi.models);
  const [, navigate] = useLocation();
  const [q, setQ] = useState("");
  const { user } = useSession();
  const base = apiBase();
  const list = models.data ?? [];
  const providers = new Set(list.flatMap((m) => m.providers ?? []));
  const example = list.find((m) => m.kind === "chat")?.id ?? "vendor/model";

  return (
    <div className="space-y-16">
      <section className="grid gap-10 pt-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,28rem)] lg:items-center">
        <div className="min-w-0">
          <h1 className="max-w-2xl text-4xl font-semibold tracking-tight text-balance [overflow-wrap:break-word] md:text-5xl">
            One API for every model.
          </h1>
          <p className="mt-4 max-w-xl text-lg text-muted-foreground">
            Self-hosted, light, open. Polyrail is an OpenAI-compatible endpoint with routing, failover and per-key
            budgets in a single binary.
          </p>
          <form
            role="search"
            className="mt-8 flex max-w-xl gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              navigate(q.trim() ? `/models?q=${encodeURIComponent(q.trim())}` : "/models");
            }}
          >
            <div className="relative flex-1">
              <Search aria-hidden className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                aria-label="Search models"
                placeholder="Search models"
                value={q}
                onChange={(e) => setQ(e.target.value)}
                className="h-10 pl-9"
              />
            </div>
            <Button type="submit" size="lg">
              Search
            </Button>
          </form>
        </div>
        <div className="grid grid-cols-2 divide-x rounded-lg border">
          <Count label="Public models" value={models.data ? String(list.length) : undefined} href="/models" />
          <Count label="Providers" value={models.data ? String(providers.size) : undefined} href="/status" />
        </div>
      </section>

      <section aria-labelledby="models-h">
        <div className="mb-3 flex items-end justify-between gap-4">
          <h2 id="models-h" className="text-xl font-semibold tracking-tight">
            Models
          </h2>
          <Link href="/models" className="text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">
            Browse all models
          </Link>
        </div>
        <DataState
          state={models}
          rows={4}
          empty={<EmptyState title="No public models yet">An operator adds them in the console under Platform, Models.</EmptyState>}
        >
          {(ms: ModelInfo[]) => (
            <div className="rounded-lg border">
              <ModelListHead />
              <ul className="divide-y">
                {[...ms]
                  .sort((a, b) => b.created - a.created || a.name.localeCompare(b.name))
                  .slice(0, 5)
                  .map((m) => (
                    <ModelRow key={m.id} m={m} compactRow />
                  ))}
              </ul>
            </div>
          )}
        </DataState>
      </section>

      <section aria-labelledby="qs-h" className="grid gap-8 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
        <div>
          <h2 id="qs-h" className="text-xl font-semibold tracking-tight">
            Quickstart
          </h2>
          <ol className="mt-4 space-y-5 text-sm">
            <Step n={1} title="Create an API key">
              {user ? (
                <Link href="/app/keys" className="underline underline-offset-4">Open API keys in the console</Link>
              ) : (
                <>
                  <Link href="/login?next=%2Fapp%2Fkeys" className="underline underline-offset-4">Sign in</Link>, then open API keys.
                </>
              )}
            </Step>
            <Step n={2} title="Point your OpenAI SDK at the gateway">
              <span className="flex items-center gap-1">
                <code className="truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{base}</code>
                <CopyButton text={base} label="Copy base URL" />
              </span>
            </Step>
            <Step n={3} title="Send a request">
              Use any model id from the catalog. <Link href="/docs" className="underline underline-offset-4">Read the docs</Link>
            </Step>
          </ol>
        </div>
        <CodeTabs samples={requestSamples(base, example)} />
      </section>
    </div>
  );
}

function Count({ label, value, href }: { label: string; value?: string; href: string }) {
  return (
    <Link href={href} className="block p-5 hover:bg-accent/50 focus-visible:outline-2 focus-visible:outline-ring">
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="mt-1 text-3xl font-semibold tabular-nums">{value ?? <span className="text-muted-foreground">–</span>}</p>
    </Link>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span aria-hidden className="grid size-6 shrink-0 place-items-center rounded-full border text-xs font-medium tabular-nums">
        {n}
      </span>
      <div className="min-w-0">
        <p className="font-medium">
          <span className="sr-only">Step {n}: </span>
          {title}
        </p>
        <div className="mt-1 text-muted-foreground">{children}</div>
      </div>
    </li>
  );
}
