import { useState } from "react";
import { Field } from "@/components/console/bits";
import { EmptyState, PageHeader } from "@/components/states";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { get, orgBase, type ModelKind, type ModelView } from "@/lib/api";
import { useAsync, useQueryParam } from "@/lib/hooks";
import { useSession } from "@/lib/session";
import { ChatPanel } from "./playground/chat/ChatPanel";
import { ImagePanel, RerankPanel, SpeechPanel, TranscribePanel } from "./playground/media";

// The page is one lazy chunk (App.tsx); the chat panel's Markdown renderer (react-markdown + remark-gfm)
// is imported only from here, so it never reaches the initial bundle.

const modes: { v: string; label: string; kind: ModelKind }[] = [
  { v: "chat", label: "Chat", kind: "chat" },
  { v: "speech", label: "Text to speech", kind: "speech" },
  { v: "transcription", label: "Speech to text", kind: "transcription" },
  { v: "image", label: "Image", kind: "image" },
  { v: "rerank", label: "Rerank", kind: "rerank" },
];

export default function Playground() {
  const { org, wsList } = useSession();
  const base = org ? orgBase(org.id) : "";
  const models = useAsync(() => get<ModelView[]>(wsList(`${base}/models`)).then((ms) => ms.filter((m) => m.enabled)), [base]);
  const [mode, setMode] = useQueryParam("mode", "chat");
  const [apiKey, setApiKey] = useState(""); // memory only: never persisted

  if (!org) return <EmptyState title="You are not in an organization yet" />;

  const keyField = (
    <Field id="pg-key" label="API key" help="Kept in this tab's memory only; it is gone when you leave the page.">
      <Input
        id="pg-key"
        type="password"
        autoComplete="off"
        spellCheck={false}
        placeholder="sk-gw-…"
        value={apiKey}
        onChange={(e) => setApiKey(e.target.value)}
        aria-describedby="pg-key-help"
      />
    </Field>
  );
  const props = { models, apiKey, keyField };

  return (
    <>
      <PageHeader
        title="Playground"
        description="Try any model your organization can use. Requests go through the gateway with your API key and count toward usage."
      />
      <Tabs value={mode} onValueChange={setMode} className="mb-6">
        <TabsList aria-label="Mode" className="h-auto flex-wrap">
          {modes.map((m) => (
            <TabsTrigger key={m.v} value={m.v}>
              {m.label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      {mode === "speech" ? (
        <SpeechPanel {...props} />
      ) : mode === "transcription" ? (
        <TranscribePanel {...props} />
      ) : mode === "image" ? (
        <ImagePanel {...props} />
      ) : mode === "rerank" ? (
        <RerankPanel {...props} />
      ) : (
        <ChatPanel {...props} />
      )}
    </>
  );
}
