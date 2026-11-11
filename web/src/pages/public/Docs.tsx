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
  ["routing", "Request routing"],
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
  ["models", "List and query models"],
  ["mcp", "MCP tools"],
  ["kb", "Knowledge bases"],
  ["keys", "Provider keys and alerts"],
  ["policies", "Policies"],
  ["workspaces", "Workspaces"],
  ["regions", "Data regions"],
  ["mgmt", "Management tokens"],
  ["sso", "SSO & SCIM"],
  ["traces", "Trace export"],
  ["exports", "Exports"],
  ["headers", "Gateway headers"],
  ["errors", "Errors"],
] as const;

const errors: [string, string, string][] = [
  ["invalid_api_key", "401", "Missing, unknown or revoked key."],
  ["expired_api_key", "401", "The key is past its expiry date."],
  ["model_not_allowed", "403", "The key is limited to other models."],
  ["model_not_found", "404", "No model with this id is visible to your org."],
  ["wrong_model_kind", "400", "The model's kind does not match the endpoint, such as a chat model on /v1/embeddings."],
  ["kb_not_found", "404", "An X-GW-KB id is not a knowledge base of your org."],
  ["guardrail_blocked", "400", "The org's guardrails rejected the input."],
  ["prompt_injection", "400", "The org blocks prompts that look like jailbreak attempts."],
  ["invalid_idempotency_key", "400", "The Idempotency-Key header is longer than 255 characters."],
  ["idempotency_key_in_progress", "409", "A request with this Idempotency-Key is still running."],
  ["idempotency_key_reuse", "422", "This Idempotency-Key was used with a different body."],
  ["insufficient_credits", "402", "The prepaid org is out of credits."],
  ["key_budget_exceeded", "402", "The key reached its monthly budget."],
  ["rpm_exceeded, tpm_exceeded", "429", "The key's requests or tokens per minute limit."],
  ["bad_batch_file", "400", "A batch file line has a url the model's kind can't serve, or a body.model that is not the file's catalog id."],
  ["video_not_ready", "409", "The video job has not completed yet; poll GET /v1/videos/{id}."],
  ["not_found", "404", "No file, batch or video with this gateway id in your org."],
  ["invalid_routing", "400", "The models list or provider object is invalid, such as an unknown field or a sort value."],
  ["preset_not_found", "404", "No preset with this slug in your org."],
  ["no_provider", "503", "Provider filters left no target for the model."],
  ["policy_model_not_allowed", "403", "A guardrail policy does not allow this model (error.policy names it)."],
  ["policy_budget_exceeded", "403", "A policy budget for this key or its creator is used up until it resets."],
  ["policy_no_eligible_provider", "403", "A policy's provider or privacy rules leave no provider for the model."],
  ["policy_filter_blocked", "403", "A policy filter with action block matched the request text."],
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

          <Section id="routing" title="Request routing">
            <p>
              Chat completions, Responses and text completions accept OpenRouter's routing fields. The gateway reads them and removes{" "}
              <C>models</C>, <C>provider</C>, <C>preset</C> and <C>usage</C> before calling the provider.
            </p>
            <HeaderTable
              rows={[
                ["models: [\"vendor/a\", \"vendor/b\"]", "Fallback models tried after model, in order (at most 5). Models your key cannot use are skipped. Billing and the response model are the model that served."],
                ["provider.order, only, ignore", "Provider names or types (case-insensitive): try these first in this order, keep only these, or drop these."],
                ["provider.sort", "\"price\", \"throughput\" or \"latency\", or {by, partition: \"none\"} to sort across all models' providers instead of within each model."],
                ["provider.max_price", "{prompt, completion} in USD per 1M tokens, {request}, {image}: providers above any cap are skipped."],
                ["provider.allow_fallbacks: false", "Only the first eligible provider of each model is tried (models fallbacks still apply)."],
                ["provider.zdr: true", "Only providers that declare zero data retention."],
                ["provider.data_collection: \"deny\"", "Skip providers that declare they train on prompts or retain them."],
                ["provider.preferred_min_throughput, preferred_max_latency", "Tokens per second and seconds: providers measured below or above move behind the others."],
                ["model suffix :nitro / :floor", "Sort that model's providers by throughput / by price."],
                ["\"model\": \"@preset/<slug>\"", "Use an org preset (Organization → Presets): its model, fallbacks, provider preferences and parameters. Fields in the request win."],
                ["usage: {\"include\": true}", "The response usage gains cost (USD), cost_micros and cached token counts when the provider reports them."],
                ["user", "Recorded on the request log as the end user and grouped under Activity → End users."],
              ]}
            />
            <p>
              Unknown fields or bad values in <C>provider</C> answer 400 <C>invalid_routing</C>; when the filters leave no provider, 503{" "}
              <C>no_provider</C>. Routing preferences can only narrow what your organization's policies allow.
            </p>
            <CodeBlock
              code={`curl ${base}/chat/completions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -d '{
    "model": "openai/gpt-4o:floor",
    "models": ["anthropic/claude-sonnet"],
    "provider": {"zdr": true, "ignore": ["azure"], "max_price": {"prompt": 3}},
    "usage": {"include": true},
    "user": "customer-42",
    "messages": [{"role": "user", "content": "Hello"}]
  }'`}
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

          <Section id="models" title="List and query models">
            <Endpoint method="GET" path="/v1/models" />
            <p>
              Models visible to the key, each with <C>kind</C> (chat, embedding, speech, transcription, image, rerank, ocr
              or video), <C>pricing.input</C> and <C>pricing.output</C> in USD per 1M tokens, and when set{" "}
              <C>pricing.audio_minute</C> (per audio minute), <C>pricing.image</C> (per image), <C>pricing.request</C>{" "}
              (per request), <C>pricing.video_second</C> (per second of video), plus <C>context_length</C>. The public catalog is also browsable on the <Link href="/models">models page</Link>.
            </p>
            <Endpoint method="POST" path="/v1/models/query" />
            <p>
              The same models, filtered by an optional JSON body: <C>q</C> (substring of id, name or description), <C>kind</C>,{" "}
              <C>modalities</C>, <C>min_context</C>, <C>min_output</C>, <C>max_input_price</C> and <C>max_output_price</C> (USD
              per 1M tokens), <C>owned_by</C>, <C>exclude</C>, <C>sort</C> (<C>id</C>, <C>price</C> or <C>context</C>) and{" "}
              <C>limit</C> (default 50, max 100).
            </p>
            <CodeBlock code={`curl ${base}/v1/models/query \\
  -H "Authorization: Bearer $GATEWAY_KEY" \\
  -d '{"kind":"chat","min_context":100000,"sort":"price","limit":5}'`} />
          </Section>

          <Section id="mcp" title="MCP tools">
            <Endpoint method="POST" path="/v1/mcp/tool/execute" />
            <p>
              Send <C>x-gateway-mcp-clients</C> on a chat request to add tools from MCP servers granted to your org. When the
              model returns <C>tool_calls</C>, post one item to this endpoint; it returns the tool message to append to the
              conversation.
            </p>
          </Section>

          <Section id="kb" title="Knowledge bases">
            <p>
              A knowledge base holds documents the gateway chunks, embeds with a model you choose and searches with vector
              plus keyword ranking. Create one and add documents in the console under Knowledge bases. A chat request is
              grounded when it names knowledge bases: the gateway searches them with the latest user message and inserts the
              best excerpts as a system message right after it, asking the model to cite them as <C>[n]</C>. Retrieval
              embeds the query once per embedding model, billed to the calling key.
            </p>
            <HeaderTable
              rows={[
                ["X-GW-KB: vs_a,vs_b", "Request: ground in these knowledge bases (at most 3). Overrides the key's kb_ids. An id that is not your org's fails with 404 kb_not_found."],
                ["X-GW-KB: off", "Request: no grounding for this request."],
                ["X-GW-KB", "Response: the knowledge bases used, or empty (no excerpt matched) or error (retrieval failed; the request was answered without it)."],
                ["X-GW-KB-Sources", "Response: the excerpts injected, as file_id#chunk, comma separated (at most 20)."],
              ]}
            />
            <p>
              An admin can attach up to 3 knowledge bases to an API key (<C>kb_ids</C> on key create and update); every chat
              request with that key is then grounded unless it sends <C>X-GW-KB</C>.
            </p>
            <Endpoint method="POST" path="/v1/vector_stores" />
            <p>
              The OpenAI vector stores API, also under <C>/openai/v1</C>. Create takes <C>name</C>, <C>embedding_model</C>{" "}
              (required, a gateway extension), optional <C>dimensions</C>, <C>chunking_strategy</C> (<C>static</C>, token
              sizes) and <C>metadata</C>. Also: <C>GET /v1/vector_stores</C>, <C>GET</C> and <C>DELETE{" "}
              /v1/vector_stores/{"{id}"}</C>, <C>POST /v1/vector_stores/{"{id}"}/files</C> (multipart <C>file</C>, or JSON{" "}
              <C>{`{"text", "filename"}`}</C> or <C>{`{"url"}`}</C>, plus <C>attributes</C>), <C>GET …/files</C>,{" "}
              <C>GET</C> and <C>DELETE …/files/{"{file_id}"}</C> (status <C>in_progress</C>, <C>completed</C> or{" "}
              <C>failed</C>) and <C>POST /v1/vector_stores/{"{id}"}/search</C> with <C>query</C>,{" "}
              <C>max_num_results</C> (up to 50), <C>filters</C> and <C>ranking_options.score_threshold</C>. Any key of the
              org can list, read and search; a key changes only the stores it created.
            </p>
            <CodeBlock
              code={`curl ${base}/vector_stores/vs_…/files \
  -H "Authorization: Bearer $GATEWAY_API_KEY" \
  -F file=@handbook.md

curl ${base}/chat/completions \
  -H "Authorization: Bearer $GATEWAY_API_KEY" \
  -H "X-GW-KB: vs_…" \
  -d '{"model": "vendor/model", "messages": [{"role": "user", "content": "How do refunds work?"}]}'`}
            />
          </Section>

          <Section id="keys" title="Provider keys and alerts">
            <p>
              Admins manage a provider's upstream keys in the console: open a provider under Providers. Paste many keys at
              once (one per line or comma separated, up to 1,000 per call); keys already stored are skipped as duplicates.
              Requests spread across a provider's keys by weight, and each key shows its status, when it was last used and
              its successes and errors over 24 hours, with an hourly chart for the last 72 hours.
            </p>
            <HeaderTable
              rows={[
                ["Available", "Enabled and serving requests."],
                ["Cooldown", "Rested after a 429, 5xx or timeout: until Retry-After (at most 5 minutes), else 30 seconds doubling to 5 minutes. A success clears it."],
                ["Quarantined", "Left out after 3 auth failures (401/403) in a row, or by an admin, until a successful Test or a re-enable."],
                ["Disabled", "Turned off by an admin."],
              ]}
            />
            <p>
              Test sends a small request with one key and reports latency or the error; a passing test clears cooldown and
              quarantine. If every key of a provider is out, all of them are tried so the request still fails over through
              normal routing.
            </p>
            <p>
              Alerts (Organization → Alerts, Platform → Alerts) send <C>key.quarantined</C>, <C>key.recovered</C>,{" "}
              <C>provider.unavailable</C>, <C>budget.exceeded</C> and <C>request.failed</C> to Telegram, Discord, Slack,
              Microsoft Teams, email, ntfy or any service{" "}
              <a href="https://github.com/nicholas-fedor/shoutrrr#readme" target="_blank" rel="noreferrer">shoutrrr</a>{" "}
              supports, one URL per channel. The same alert is sent at most once per channel and subject every 10 minutes;
              a channel is disabled after 10 failed deliveries in a row. Organizations can use services with fixed vendor
              hosts; services on a custom host (SMTP, self-hosted ntfy, Gotify, generic webhooks) are for operators unless
              private URLs are allowed.
            </p>
          </Section>

          <Section id="policies" title="Policies">
            <p>
              Admins manage guardrail policies under Organization → Policies. A policy applies to a key when it is the organization default
              (unless the key or its creator is opted out), when the key's creator is one of its members, or when the key is listed. Every
              policy that applies is enforced:
            </p>
            <HeaderTable
              rows={[
                ["Budget", "A spend limit per day, week (from Monday), month or lifetime, UTC. Through the org default or members it counts the creator's spend across all their keys; through keys, the key's own spend. Over the limit: 403 policy_budget_exceeded."],
                ["Allowed models", "Model ids or globs where * matches anything, including /. Several policies intersect. Other models: 403 policy_model_not_allowed; /v1/models lists only allowed ones."],
                ["Allowed providers", "Provider types or names. Targets of other providers are skipped; none left: 403 policy_no_eligible_provider."],
                ["Filters", "RE2 patterns on the request text: redact replaces matches with [REDACTED:name], block refuses with 403 policy_filter_blocked."],
                ["Privacy", "Require zero data retention, or deny providers that train on prompts. Providers declare this in their settings; undeclared providers fail both."],
              ]}
            />
            <p>
              The API keys page shows each key's effective policy with budget spend and reset time; members see it for the keys they created.
              Errors carry the policy's name in <C>error.policy</C>.
            </p>
          </Section>

          <Section id="workspaces" title="Workspaces">
            <p>
              An organization splits its work into workspaces (Organization → Workspaces). Every organization has a default workspace; org
              owners and admins create more and are admins of all of them. Pick the workspace in the switcher next to the organization in
              the console's top bar.
            </p>
            <HeaderTable
              rows={[
                ["Scoped", "API keys, private providers, private models, policies, presets, knowledge bases and trace exports belong to one workspace. A key reaches public models plus the private models, presets and knowledge bases of its own workspace."],
                ["Organization-wide", "Credits, members, webhooks, alerts, guardrails, content logging and SSO. Usage and logs can be filtered by workspace; admins also group activity by workspace."],
                ["Members", "Every org member is in the default workspace with their org role. Other workspaces have their own admins, members and viewers, added by hand or through SSO group mappings."],
                ["Sharing", "An org admin can share a provider with all workspaces so every workspace's models may route to it."],
                ["Budgets", "A policy marked as the workspace default applies to every key of the workspace, and its budget is one limit for all of them."],
              ]}
            />
            <p>
              In the API, create bodies take <C>workspace_id</C> and lists take <C>?workspace=</C>; the default workspace's id is{" "}
              <C>default</C>. A workspace that still holds anything cannot be deleted (409 with what is left).
            </p>
          </Section>

          <Section id="regions" title="Data regions">
            <p>
              Each provider declares where it processes prompts: <C>global</C>, <C>europe</C>, <C>us</C> or another region name (provider
              settings → Privacy). Names are normalized on save, so <C>eu-west-1</C> becomes <C>europe</C> and <C>us-east-1</C> becomes{" "}
              <C>us</C>; a provider that declares nothing counts as <C>global</C>.
            </p>
            <p>
              A policy's allowed regions keep requests on providers in those regions. Lists from several policies intersect, and a model whose
              providers are all outside them answers 403 <C>policy_no_eligible_provider</C>: the gateway never falls back to another region.
              Model pages show each provider's declared region.
            </p>
          </Section>

          <Section id="mgmt" title="Management tokens">
            <p>
              Org admins create management tokens under API keys → Management tokens, for scripts and CI that call the admin API. Send the
              token as a bearer token to any <C>/api/orgs/{"{org}"}/…</C> route of its own organization; no cookie or CSRF header is needed.
              A token acts with its role (admin, member or viewer), never above its creator's current role, and cannot call <C>/v1</C>,
              platform routes or the token routes themselves. A token limited to a workspace reaches only that workspace's keys,
              providers, models, policies, presets and trace exports.
            </p>
            <CodeBlock
              code={`curl ${location.origin}/api/orgs/$ORG/usage?days=7 \\
  -H "Authorization: Bearer pr-mgmt-…"`}
            />
          </Section>

          <Section id="sso" title="SSO & SCIM">
            <p>
              Owners set this up under Organization → Security &amp; SSO. First add your email domain and publish its DNS TXT record
              (<C>_polyrail-verify.&lt;domain&gt;</C>), then press Verify. A domain belongs to one organization.
            </p>
            <HeaderTable
              rows={[
                ["SAML", "Create a SAML app in Okta, Entra ID or Google Workspace with the entity ID and ACS URL shown on the page, then give Polyrail the IdP metadata URL or XML. People at your verified domains get “Continue with SSO” on the sign-in page; new users get an account on first sign-in."],
                ["Require SSO", "Refuses password sign-in and sign-up for emails at your verified domains (error sso_required). Needs SAML on and a verified domain. Platform operators can still use their password."],
                ["SCIM 2.0", "Generate a token (shown once) and enter it with the base URL /scim/v2/{org} in your IdP. The IdP creates, updates and deprovisions users and groups; deprovisioning removes the membership and revokes the keys the person created in the organization."],
                ["Group mappings", "Map an IdP group name (SCIM group or the SAML groups attribute) to viewer, member or admin. The highest mapped role wins; owners keep their role. A mapping with a workspace gives that role in the workspace only."],
              ]}
            />
            <p>The IdP can only sign in or provision emails at domains the organization verified.</p>
          </Section>

          <Section id="traces" title="Trace export">
            <p>
              Send one OpenTelemetry span per request to an OTLP/HTTP collector (Grafana Tempo, Datadog Agent, Jaeger, any OpenTelemetry
              Collector) or to Langfuse. Admins add exports under Organization → Observability; operators add platform exports that see every
              organization's requests under Platform → Observability.
            </p>
            <HeaderTable
              rows={[
                ["OTLP", "The full traces URL, such as https://collector.example.com/v1/traces, plus optional headers (write-only) such as Authorization."],
                ["Langfuse", "The Langfuse base URL and the project's public and secret keys; spans go to <base>/api/public/otel/v1/traces."],
                ["Sample rate", "0–100% of requests, decided by the request id."],
                ["Keys", "Optionally only some API keys' requests."],
                ["Content", "Prompt and completion are added only when the export includes content and the organization has content logging on, each cut at 16 KB."],
              ]}
            />
            <p>
              Spans follow the OpenTelemetry gen_ai conventions (<C>gen_ai.request.model</C>, <C>gen_ai.usage.input_tokens</C>, cost and latency
              attributes). Endpoints must be public HTTPS unless the operator allows private URLs. Delivery is batched and best effort; an export is disabled after
              10 failed batches in a row, and the Test button sends one span now.
            </p>
          </Section>

          <Section id="exports" title="Exports">
            <p>
              Usage and request logs download as CSV from Activity → Export CSV, or with a management token. <C>from</C> and <C>to</C> are dates
              (UTC, both included) at most 366 days apart; usage defaults to the last 30 days, logs to the last 7.
            </p>
            <HeaderTable
              rows={[
                ["GET /api/orgs/{org}/usage/export.csv?group=model|key|creator|day", "Daily usage rows: requests, errors, tokens, cost_usd, latency and cache hits per group."],
                ["GET /api/orgs/{org}/logs/export.csv", "One row per request, newest first, at most 100,000."],
                ["GET /api/admin/usage/export.csv?group=model|day", "Operators: platform usage rollups."],
              ]}
            />
            <p>Cells that a spreadsheet would run as a formula get a leading apostrophe.</p>
          </Section>

          <Section id="headers" title="Gateway headers">
            <h3 className="font-medium text-foreground">Request</h3>
            <HeaderTable
              rows={[
                ["x-gateway-cache: <seconds>", "Serve an identical earlier non-stream request from cache (max 86400). Cache hits cost nothing."],
                ["x-gateway-mcp-clients: name1,name2 or *", "Inject tools from these MCP servers (only servers granted to your org)."],
                ["Idempotency-Key: <up to 255 chars>", "Non-stream chat, embeddings, responses and image generation requests: a retry with the same key and body within 24 h returns the stored response without billing again (Idempotent-Replayed: true). Kept per node."],
              ]}
            />
            <h3 className="pt-2 font-medium text-foreground">Response</h3>
            <HeaderTable
              rows={[
                ["x-gateway-provider", "The provider that served the request."],
                ["x-gateway-request-id", "A unique id for this request."],
                ["x-gateway-cache-hit", "true when the response came from the cache."],
                ["X-GW-Cost-Micros", "Micro-USD billed for this request (non-stream responses only; 0 for cache hits)."],
                ["X-GW-Route", "Chat completions: the routing strategy and the route that served the request, as <strategy>; <provider>/<upstream model>."],
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
