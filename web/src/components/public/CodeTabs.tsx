import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { toast } from "@/components/toast";
import { cn, writeClipboard } from "@/lib/utils";
import type { ModelKind } from "@/lib/api";

/** Icon button that copies `text` and confirms briefly. */
export function CopyButton({ text, label = "Copy", className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      aria-label={label}
      title={label}
      className={cn("text-muted-foreground hover:text-foreground", className)}
      onClick={() =>
        writeClipboard(text).then(
          () => {
            setDone(true);
            setTimeout(() => setDone(false), 1500);
          },
          () => toast("Could not copy. Select the text instead.", { error: true }),
        )
      }
    >
      {done ? <Check /> : <Copy />}
    </Button>
  );
}

/** Monospace block with a copy button. */
export function CodeBlock({ code, className }: { code: string; className?: string }) {
  return (
    <div className={cn("relative rounded-lg border bg-muted/50", className)}>
      <CopyButton text={code} label="Copy code" className="absolute top-2 right-2 bg-muted" />
      <pre className="overflow-x-auto p-4 pr-12 font-mono text-[13px] leading-relaxed">
        <code>{code}</code>
      </pre>
    </div>
  );
}

export type Sample = { label: string; code: string };

/** The same request in several languages, one tab each. */
export function CodeTabs({ samples, className }: { samples: Sample[]; className?: string }) {
  const [tab, setTab] = useState(samples[0]?.label ?? "");
  return (
    <Tabs value={tab} onValueChange={setTab} className={cn("min-w-0", className)}>
      <TabsList>
        {samples.map((s) => (
          <TabsTrigger key={s.label} value={s.label}>
            {s.label}
          </TabsTrigger>
        ))}
      </TabsList>
      {samples.map((s) => (
        <TabsContent key={s.label} value={s.label} className="min-w-0 rounded-lg focus-visible:ring-[3px] focus-visible:ring-ring/50">
          <CodeBlock code={s.code} />
        </TabsContent>
      ))}
    </Tabs>
  );
}

/** curl, Python and TypeScript (OpenAI SDK) for one call of the model's kind against this gateway. */
export function requestSamples(base: string, model: string, kind: ModelKind = "chat"): Sample[] {
  if (kind === "speech")
    return [
      {
        label: "curl",
        code: `curl ${base}/audio/speech \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "input": "Hello from the gateway.",
    "voice": "alloy",
    "response_format": "mp3"
  }' \\
  --output speech.mp3`,
      },
      {
        label: "Python",
        code: `import os
from openai import OpenAI

client = OpenAI(base_url="${base}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.audio.speech.create(
    model="${model}",
    voice="alloy",
    input="Hello from the gateway.",
    response_format="mp3",
)
res.write_to_file("speech.mp3")`,
      },
      {
        label: "TypeScript",
        code: `import fs from "node:fs";
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.audio.speech.create({
  model: "${model}",
  voice: "alloy",
  input: "Hello from the gateway.",
  response_format: "mp3",
});
fs.writeFileSync("speech.mp3", Buffer.from(await res.arrayBuffer()));`,
      },
    ];
  if (kind === "transcription")
    return [
      {
        label: "curl",
        code: `curl ${base}/audio/transcriptions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -F model=${model} \\
  -F file=@audio.mp3`,
      },
      {
        label: "Python",
        code: `import os
from openai import OpenAI

client = OpenAI(base_url="${base}", api_key=os.environ["GATEWAY_API_KEY"])

with open("audio.mp3", "rb") as f:
    res = client.audio.transcriptions.create(model="${model}", file=f)
print(res.text)`,
      },
      {
        label: "TypeScript",
        code: `import fs from "node:fs";
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.audio.transcriptions.create({
  model: "${model}",
  file: fs.createReadStream("audio.mp3"),
});
console.log(res.text);`,
      },
    ];
  if (kind === "embedding")
    return [
      {
        label: "curl",
        code: `curl ${base}/embeddings \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "input": "The quick brown fox"
  }'`,
      },
      {
        label: "Python",
        code: `import os
from openai import OpenAI

client = OpenAI(base_url="${base}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.embeddings.create(model="${model}", input="The quick brown fox")
print(len(res.data[0].embedding))`,
      },
      {
        label: "TypeScript",
        code: `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.embeddings.create({ model: "${model}", input: "The quick brown fox" });
console.log(res.data[0].embedding.length);`,
      },
    ];
  return [
    {
      label: "curl",
      code: `curl ${base}/chat/completions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "messages": [{"role": "user", "content": "Hello"}]
  }'`,
    },
    {
      label: "Python",
      code: `import os
from openai import OpenAI

client = OpenAI(base_url="${base}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello"}],
)
print(res.choices[0].message.content)`,
    },
    {
      label: "TypeScript",
      code: `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${base}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "Hello" }],
});
console.log(res.choices[0].message.content);`,
    },
  ];
}

/** Gateway base URL as seen by this browser. */
export const apiBase = () => `${location.origin}/v1`;
