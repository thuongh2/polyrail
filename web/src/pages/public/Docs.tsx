import type { ReactNode } from "react";
import { Link } from "wouter";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { PageHeader } from "@/components/states";
import { apiBase, CodeBlock, CodeTabs, requestSamples, sdkSamples } from "@/components/public/CodeTabs";

const sections = [
  ["base", "Base URL and auth"],
  ["sdks", "SDK drop-ins"],
  ["chat", "Chat completions"],
  ["stream", "Streaming"],
  ["completions", "Text completions"],
  ["responses", "Responses API"],
  ["embeddings", "Embeddings"],
  ["speech", "Text to speech"],
  ["transcriptions", "Speech to text"],
  ["images", "Images"],
  ["rerank", "Rerank"],
  ["ocr", "OCR"],
  ["tokens", "Token counting"],
  ["files", "Files and batches"],
  ["videos", "Videos"],
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
  ["bad_batch_file", "400", "A batch file line has a url the model's kind can't serve, or a body.model that is not the file's catalog id."],
  ["video_not_ready", "409", "The video job has not completed yet; poll GET /v1/videos/{id}."],
  ["not_found", "404", "No file, batch or video with this gateway id in your org."],
  ["body_too_large", "413", "The request body is over the server limit (8 MB by default)."],
  ["provider_error", "upstream or 502", "Every provider for the model failed."],
];

export default function Docs() {
  const base = apiBase();
  return (
    <>
      <PageHeader
        title="API reference"
        description="The gateway speaks the OpenAI API, plus Anthropic and Google GenAI drop-ins. Point an SDK at the base URL below and use a model id from the catalog."
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

          <Section id="sdks" title="SDK drop-ins">
            <p>
              Three SDKs work unchanged; only the base URL and key change. The <C>model</C> is always a catalog id, and
              routing, limits, guardrails, pricing and logs are the same whichever API you use.
            </p>
            <HeaderTable
              rows={[
                [`${location.origin}/v1 or /openai/v1`, "OpenAI SDKs and any OpenAI-compatible client. Every endpoint on this page."],
                [`${location.origin}/anthropic`, "Anthropic SDKs: /v1/messages (stream supported), /v1/messages/count_tokens and /v1/models, for chat models. Key as x-api-key. Errors use Anthropic's shape."],
                [`${location.origin}/genai`, "Google GenAI (Gemini) SDKs: generateContent, streamGenerateContent, countTokens for chat models, embedContent and batchEmbedContents for embedding models. Send the gateway key as x-goog-api-key (the SDK's api_key). Errors use Google's shape."],
              ]}
            />
            <CodeTabs samples={sdkSamples(location.origin, "vendor/model")} />
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

          <Section id="images" title="Images">
            <Endpoint method="POST" path="/v1/images/generations" />
            <Endpoint method="POST" path="/v1/images/edits" />
            <Endpoint method="POST" path="/v1/images/variations" />
            <p>
              Image models only. Generations take JSON: <C>model</C>, <C>prompt</C> and optional <C>n</C>, <C>size</C>,{" "}
              <C>quality</C>, <C>response_format</C>, <C>background</C>, <C>output_format</C>, <C>negative_prompt</C>,{" "}
              <C>seed</C>. Edits take multipart form data with <C>image</C> (or repeated <C>image[]</C>), <C>prompt</C> and
              optional <C>mask</C>; variations take <C>image</C>. The response is{" "}
              <C>{`{"data": [{"b64_json" | "url", "revised_prompt"}]}`}</C> as the provider returns it. Streaming is not
              supported. Billing is the model's price per image times the images returned, plus tokens when reported.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/image-model", "image")} />
          </Section>

          <Section id="rerank" title="Rerank">
            <Endpoint method="POST" path="/v1/rerank" />
            <p>
              Rerank models only. Cohere/Jina body: <C>model</C>, <C>query</C>, <C>documents</C> (strings or{" "}
              <C>{`{"text"}`}</C> objects) and optional <C>top_n</C>, <C>return_documents</C>,{" "}
              <C>max_tokens_per_doc</C>. Results are sorted by <C>relevance_score</C>; <C>index</C> points into your
              documents. Guardrails apply to the query and the documents. Billed per request, plus tokens when reported.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/rerank-model", "rerank")} />
          </Section>

          <Section id="ocr" title="OCR">
            <Endpoint method="POST" path="/v1/ocr" />
            <p>
              OCR models only (served through Mistral-type providers). Send <C>model</C> and <C>document</C>, either{" "}
              <C>{`{"type": "document_url", "document_url"}`}</C> or <C>{`{"type": "image_url", "image_url"}`}</C>, plus
              optional <C>pages</C>, <C>include_image_base64</C>, <C>table_format</C>. The response lists{" "}
              <C>pages</C> with <C>markdown</C> per page. Billed per request.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/ocr-model", "ocr")} />
          </Section>

          <Section id="tokens" title="Token counting">
            <Endpoint method="POST" path="/v1/responses/input_tokens" />
            <Endpoint method="POST" path="/anthropic/v1/messages/count_tokens" />
            <Endpoint method="POST" path="/genai/v1beta/models/{model}:countTokens" />
            <p>
              Count input tokens for a chat model before sending: a Responses body returns{" "}
              <C>{`{"input_tokens"}`}</C>, the Anthropic form (<C>model</C>, <C>messages</C>, optional <C>system</C> and{" "}
              <C>tools</C>) returns <C>{`{"input_tokens"}`}</C>, the Gemini form returns <C>{`{"totalTokens"}`}</C>.
              The serving provider counts. These calls are rate limited like others and logged at no cost.
            </p>
            <CodeBlock
              code={`curl ${base}/responses/input_tokens \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "vendor/model", "input": "How many tokens is this?"}'`}
            />
          </Section>

          <Section id="files" title="Files and batches">
            <Endpoint method="POST" path="/v1/files" />
            <Endpoint method="POST" path="/v1/batches" />
            <p>
              Upload a file with multipart <C>file</C>, <C>purpose</C> (such as <C>batch</C>) and the model it is for, as the
              form field or header <C>x-gateway-model</C> (a catalog id). The file goes to that model's primary healthy
              provider, and a batch runs where its file is. In a batch file every line's <C>body.model</C> must be that
              catalog id and its <C>url</C> an endpoint of the model's kind (<C>/v1/chat/completions</C>,{" "}
              <C>/v1/completions</C>, <C>/v1/responses</C> or <C>/v1/embeddings</C>); otherwise the upload fails with{" "}
              <C>bad_batch_file</C>.
            </p>
            <p>
              Ids are gateway ids (<C>file-gw-…</C>, <C>batch-gw-…</C>), scoped to your organization; provider ids are never
              shown or accepted. Also available: <C>GET /v1/files</C>, <C>GET /v1/files/{"{id}"}</C>,{" "}
              <C>GET /v1/files/{"{id}"}/content</C>, <C>DELETE /v1/files/{"{id}"}</C>, <C>GET /v1/batches</C>,{" "}
              <C>GET /v1/batches/{"{id}"}</C>, <C>POST /v1/batches/{"{id}"}/cancel</C>. A batch is billed once, at full
              token prices, when first seen finished; its output and error files appear after that.
            </p>
            <CodeBlock
              code={`curl ${base}/files \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "x-gateway-model: vendor/model" \\
  -F purpose=batch -F file=@requests.jsonl

curl ${base}/batches \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"input_file_id": "file-gw-…", "endpoint": "/v1/chat/completions", "completion_window": "24h"}'`}
            />
          </Section>

          <Section id="videos" title="Videos">
            <Endpoint method="POST" path="/v1/videos" />
            <p>
              Video models only. JSON or multipart <C>model</C>, <C>prompt</C> and optional <C>seconds</C>, <C>size</C>,{" "}
              <C>input_reference</C>. The job runs on the model's primary provider (no failover) and returns a{" "}
              <C>video-gw-…</C> id. Poll <C>GET /v1/videos/{"{id}"}</C> until <C>status</C> is <C>completed</C>, then
              download <C>GET /v1/videos/{"{id}"}/content</C> (409 <C>video_not_ready</C> before that).{" "}
              <C>DELETE /v1/videos/{"{id}"}</C> removes it. Billed once on completion: seconds times the model's price per
              second.
            </p>
            <CodeTabs samples={requestSamples(base, "vendor/video-model", "video")} />
          </Section>

          <Section id="models" title="List models">
            <Endpoint method="GET" path="/v1/models" />
            <p>
              Models visible to the key, each with <C>kind</C> (chat, embedding, speech, transcription, image, rerank, ocr
              or video), <C>pricing.input</C> and <C>pricing.output</C> in USD per 1M tokens, and when set{" "}
              <C>pricing.audio_minute</C> (per audio minute), <C>pricing.image</C> (per image), <C>pricing.request</C>{" "}
              (per request), <C>pricing.video_second</C> (per second of video), plus <C>context_length</C>. The public catalog is also browsable on the <Link href="/models">models page</Link>.
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
