import type { ReactNode } from "react";
import { Link } from "wouter";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { PageHeader } from "@/components/states";
import { apiBase, CodeBlock, CodeTabs, requestSamples } from "@/components/public/CodeTabs";

const sections = [
  ["base", "Base URL and auth"],
  ["chat", "Chat completions"],
  ["stream", "Streaming"],
  ["completions", "Text completions"],
  ["responses", "Responses API"],
  ["embeddings", "Embeddings"],
  ["speech", "Text to speech"],
  ["transcriptions", "Speech to text"],
  ["models", "List models"],
  ["mcp", "MCP tools"],
  ["headers", "Gateway headers"],
  ["errors", "Errors"],
] as const;

const errors: [string, string, string][] = [
  ["invalid_api_key", "401", "Missing, unknown or revoked key."],
  ["expired_api_key", "401", "The key is past its expiry date."],
  ["model_not_allowed", "403", "The key is limited to other models."],
  ["model_not_found", "404", "No model with this id is visible to your org."],
  ["wrong_model_kind", "400", "The model's kind does not match the endpoint, such as a chat model on /v1/embeddings."],
  ["guardrail_blocked", "400", "The org's guardrails rejected the input."],
  ["insufficient_credits", "402", "The prepaid org is out of credits."],
  ["key_budget_exceeded", "402", "The key reached its monthly budget."],
  ["rpm_exceeded, tpm_exceeded", "429", "The key's requests or tokens per minute limit."],
  ["body_too_large", "413", "The request body is over the server limit (8 MB by default)."],
  ["provider_error", "upstream or 502", "Every provider for the model failed."],
];

export default function Docs() {
  const base = apiBase();
  return (
    <>
      <PageHeader
        title="API reference"
        description="The gateway speaks the OpenAI API. Point any OpenAI SDK at the base URL below and use a model id from the catalog."
      />
      <div className="grid gap-10 lg:grid-cols-[12rem_minmax(0,1fr)]">
        <nav aria-label="On this page" className="hidden lg:block">
          <ul className="sticky top-20 space-y-1 text-sm">
            {sections.map(([id, label]) => (
              <li key={id}>
                <a href={`#${id}`} className="block rounded-md px-2 py-1 text-muted-foreground hover:bg-accent hover:text-foreground">
                  {label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
        <div className="min-w-0 max-w-3xl space-y-12">
          <Section id="base" title="Base URL and auth">
            <CodeBlock code={base} />
            <p>
              Send an API key from the console (<Link href="/app/keys">API keys</Link>) as{" "}
              <C>Authorization: Bearer sk-gw-…</C> or as the <C>x-api-key</C> header. Keys can carry a model allow-list,
              rate limits (requests and tokens per minute), a monthly budget and an expiry.
            </p>
          </Section>

          <Section id="chat" title="Chat completions">
            <Endpoint method="POST" path="/v1/chat/completions" />
            <p>
              OpenAI request body. <C>model</C> is a catalog id such as <C>vendor/name</C>. The gateway routes across the
              model's providers by weight and fails over to the next provider on error. The response is the OpenAI shape plus a{" "}
              <C>provider</C> field naming the provider that served it.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/model")} />
          </Section>

          <Section id="stream" title="Streaming">
            <p>
              Set <C>"stream": true</C> to get server-sent events. The stream ends with <C>data: [DONE]</C> and the last chunk
              carries <C>usage</C>.
            </p>
            <CodeBlock
              code={`curl -N ${base}/chat/completions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "vendor/model", "stream": true,
       "messages": [{"role": "user", "content": "Hello"}]}'`}
            />
          </Section>

          <Section id="completions" title="Text completions">
            <Endpoint method="POST" path="/v1/completions" />
            <p>
              The legacy OpenAI completions API for chat models: send <C>prompt</C> instead of <C>messages</C>. The response
              has <C>choices[].text</C>. <C>"stream": true</C> works as for chat.
            </p>
            <CodeBlock
              code={`curl ${base}/completions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "vendor/model", "prompt": "Write a haiku about routers.", "max_tokens": 64}'`}
            />
          </Section>

          <Section id="responses" title="Responses API">
            <Endpoint method="POST" path="/v1/responses" />
            <p>
              The OpenAI Responses API for chat models: send <C>input</C> (a string or a list of messages) and optional{" "}
              <C>instructions</C>. With <C>"stream": true</C> the gateway sends named events: text arrives as{" "}
              <C>event: response.output_text.delta</C> with the text in <C>delta</C>, and the stream ends with{" "}
              <C>response.completed</C>.
            </p>
            <CodeBlock
              code={`curl -N ${base}/responses \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "vendor/model", "input": "Hello", "stream": true}'`}
            />
          </Section>

          <Section id="embeddings" title="Embeddings">
            <Endpoint method="POST" path="/v1/embeddings" />
            <p>OpenAI request body. Only embedding models are accepted here.</p>
            <CodeBlock code={requestSamples(base, "vendor/embedding-model", "embedding")[0].code} />
          </Section>

          <Section id="speech" title="Text to speech">
            <Endpoint method="POST" path="/v1/audio/speech" />
            <p>
              Speech models only. JSON body: <C>model</C>, <C>input</C> (the text), <C>voice</C> (for example alloy, echo,
              fable, onyx, nova, shimmer), <C>response_format</C> (<C>mp3</C>, <C>opus</C>, <C>aac</C>, <C>flac</C>,{" "}
              <C>wav</C> or <C>pcm</C>), and optional <C>speed</C> and <C>instructions</C>. The response body is the audio
              with an <C>audio/*</C> content type; <C>x-gateway-provider</C> names the provider. Input is billed per 1M
              tokens, or per 1M characters when the provider reports no token usage.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/tts-model", "speech")} />
          </Section>

          <Section id="transcriptions" title="Speech to text">
            <Endpoint method="POST" path="/v1/audio/transcriptions" />
            <p>
              Transcription models only. Send <C>multipart/form-data</C> with <C>file</C> (the audio), <C>model</C>, and
              optional <C>language</C>, <C>prompt</C> and <C>response_format</C> (<C>json</C> by default, or <C>text</C>).
              The JSON response has <C>text</C> and, when the provider returns them, <C>language</C>, <C>duration</C>{" "}
              (seconds), <C>segments</C> and <C>usage</C>. Billing is per minute of audio where the model has an audio price.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/stt-model", "transcription")} />
          </Section>

          <Section id="models" title="List models">
            <Endpoint method="GET" path="/v1/models" />
            <p>
              Models visible to the key, each with <C>kind</C> (chat, embedding, speech or transcription), <C>pricing.input</C> and{" "}
              <C>pricing.output</C> in USD per 1M tokens, <C>pricing.audio_minute</C> in USD per audio minute for
              transcription models, and <C>context_length</C>. The public catalog is also browsable on the <Link href="/models">models page</Link>.
            </p>
          </Section>

          <Section id="mcp" title="MCP tools">
            <Endpoint method="POST" path="/v1/mcp/tool/execute" />
            <p>
              Send <C>x-gateway-mcp-clients</C> on a chat request to add tools from MCP servers granted to your org. When the
              model returns <C>tool_calls</C>, post one item to this endpoint; it returns the tool message to append to the
              conversation.
            </p>
          </Section>

          <Section id="headers" title="Gateway headers">
            <h3 className="font-medium text-foreground">Request</h3>
            <HeaderTable
              rows={[
                ["x-gateway-cache: <seconds>", "Serve an identical earlier non-stream request from cache (max 86400). Cache hits cost nothing."],
                ["x-gateway-mcp-clients: name1,name2 or *", "Inject tools from these MCP servers (only servers granted to your org)."],
              ]}
            />
            <h3 className="pt-2 font-medium text-foreground">Response</h3>
            <HeaderTable
              rows={[
                ["x-gateway-provider", "The provider that served the request."],
                ["x-gateway-request-id", "A unique id for this request."],
                ["x-gateway-cache-hit", "true when the response came from the cache."],
              ]}
            />
          </Section>

          <Section id="errors" title="Errors">
            <p>
              Errors use the OpenAI shape: <C>{`{"error": {"message", "type", "code"}}`}</C>.
            </p>
            <div className="rounded-lg border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Code</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Meaning</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {errors.map(([code, status, meaning]) => (
                    <TableRow key={code}>
                      <TableCell className="font-mono text-xs">{code}</TableCell>
                      <TableCell className="tabular-nums">{status}</TableCell>
                      <TableCell className="whitespace-normal text-muted-foreground">{meaning}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </Section>
        </div>
      </div>
    </>
  );
}

function Section({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-h`} className="scroll-mt-20 space-y-3 text-sm leading-relaxed text-muted-foreground [&_a]:text-foreground [&_a]:underline [&_a]:underline-offset-4">
      <h2 id={`${id}-h`} className="text-lg font-semibold text-foreground">
        {title}
      </h2>
      {children}
    </section>
  );
}

function C({ children }: { children: ReactNode }) {
  return <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em] text-foreground">{children}</code>;
}

function Endpoint({ method, path }: { method: string; path: string }) {
  return (
    <p className="flex items-center gap-2 font-mono text-sm text-foreground">
      <span className="rounded border px-1.5 py-0.5 text-xs font-medium">{method}</span>
      {path}
    </p>
  );
}

function HeaderTable({ rows }: { rows: [string, string][] }) {
  return (
    <div className="rounded-lg border">
      <Table>
        <TableBody>
          {rows.map(([h, d]) => (
            <TableRow key={h}>
              <TableCell className="w-2/5 font-mono text-xs whitespace-normal text-foreground">{h}</TableCell>
              <TableCell className="whitespace-normal text-muted-foreground">{d}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
