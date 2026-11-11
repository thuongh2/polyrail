import { useState } from "react";
import { Trash2 } from "lucide-react";
import { CopyButton } from "@/components/console/bits";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { when } from "@/lib/api";
import { shortenData, snippets, type Meta, type Saved } from "./lib";

function Code({ text, label }: { text: string; label: string }) {
  return (
    <div className="grid min-w-0 gap-2">
      <pre aria-label={label} tabIndex={0} className="scrollbar-thin max-h-[55dvh] overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed">
        {text}
      </pre>
      <div>
        <CopyButton text={text} label={`Copy ${label}`} />
      </div>
    </div>
  );
}

export function ViewCodeDialog({ open, onOpenChange, body, headers }: { open: boolean; onOpenChange: (v: boolean) => void; body: Record<string, unknown>; headers: Record<string, string> }) {
  const [tab, setTab] = useState<"curl" | "python" | "ts">("curl");
  const s = snippets(location.origin, body, headers);
  const text = tab === "curl" ? s.curl : tab === "python" ? s.python : s.ts;
  const shortened = JSON.stringify(body).includes('"data:');
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[calc(100%-2rem)] sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>View code</DialogTitle>
          <DialogDescription>
            The request the Send button would make now, with this conversation and settings. Set POLYRAIL_API_KEY to your key.
            {shortened ? " Attached image data is shortened here; send it as a data: URL or an https URL." : ""}
          </DialogDescription>
        </DialogHeader>
        <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)}>
          <TabsList aria-label="Language">
            <TabsTrigger value="curl">curl</TabsTrigger>
            <TabsTrigger value="python">Python</TabsTrigger>
            <TabsTrigger value="ts">TypeScript</TabsTrigger>
          </TabsList>
        </Tabs>
        <Code text={text} label={tab === "ts" ? "TypeScript" : tab === "python" ? "Python" : "curl"} />
      </DialogContent>
    </Dialog>
  );
}

export function InspectSheet({ meta, onClose }: { meta?: Meta; onClose: () => void }) {
  const [tab, setTab] = useState<"request" | "response">("request");
  const req = meta?.request;
  const res = meta?.response;
  const reqText = req ? `POST ${req.url}\n${Object.entries(req.headers).map(([k, v]) => `${k}: ${v}`).join("\n")}\n\n${JSON.stringify(shortenData(req.body), null, 2)}` : "";
  const resText = res
    ? `HTTP ${res.status}\n${Object.entries(res.headers).map(([k, v]) => `${k}: ${v}`).join("\n")}\n\n${typeof res.body === "string" ? res.body : JSON.stringify(res.body, null, 2)}`
    : "No response yet.";
  return (
    <Sheet open={!!meta} onOpenChange={(v) => !v && onClose()}>
      <SheetContent className="w-full gap-0 sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>Inspect</SheetTitle>
          <SheetDescription>
            Raw request and response. The API key is redacted.
            {res?.chunks != null ? ` Streamed in ${res.chunks} chunks; the body below is assembled from them.` : ""}
          </SheetDescription>
        </SheetHeader>
        <div className="grid min-h-0 flex-1 content-start gap-3 overflow-y-auto px-4 pb-4">
          <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)}>
            <TabsList aria-label="Part">
              <TabsTrigger value="request">Request</TabsTrigger>
              <TabsTrigger value="response">Response</TabsTrigger>
            </TabsList>
          </Tabs>
          <Code text={tab === "request" ? reqText : resText} label={tab} />
        </div>
      </SheetContent>
    </Sheet>
  );
}

export function HistorySheet({
  open,
  onOpenChange,
  saved,
  current,
  onLoad,
  onDelete,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  saved: Saved[];
  current: string;
  onLoad: (c: Saved) => void;
  onDelete: (id: string) => void;
}) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Saved conversations</SheetTitle>
          <SheetDescription>The last 20 chats, saved in this browser only. API keys and attached image data are never saved.</SheetDescription>
        </SheetHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-4">
          {saved.length === 0 ? (
            <p className="text-sm text-muted-foreground">No saved conversations yet. Chats are saved after each answer.</p>
          ) : (
            <ul className="divide-y rounded-md border">
              {saved.map((c) => (
                <li key={c.id} className="flex items-center gap-2 px-3 py-2">
                  <button
                    type="button"
                    className="min-w-0 flex-1 rounded-sm text-left focus-visible:outline-2 focus-visible:outline-ring"
                    onClick={() => onLoad(c)}
                    aria-current={c.id === current || undefined}
                  >
                    <span className="block truncate text-sm font-medium">{c.title || "Untitled"}</span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {c.model} · {c.msgs.length} messages · {when(new Date(c.at).toISOString())}
                      {c.id === current ? " · open" : ""}
                    </span>
                  </button>
                  <Button type="button" variant="ghost" size="icon-sm" aria-label={`Delete "${c.title || "Untitled"}"`} onClick={() => onDelete(c.id)}>
                    <Trash2 aria-hidden />
                  </Button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
