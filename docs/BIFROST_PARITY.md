# Bifrost feature parity

What agent-gateway covers from Bifrost's documented feature set (docs.getbifrost.ai, read
2026-11-11), built on Bifrost core v1.15.2. "Core" marks features the pinned core supports.

## Supported

| Bifrost feature | agent-gateway |
|---|---|
| OpenAI-compatible API | `/v1/chat/completions` (stream), `/v1/completions` (stream), `/v1/responses` (stream), `/v1/embeddings`, `/v1/audio/speech`, `/v1/audio/transcriptions`, `/v1/models` |
| Drop-in replacement, OpenAI SDK | base URL `http://host/openai/v1` or `http://host/v1` |
| Drop-in replacement, Anthropic SDK | `POST /anthropic/v1/messages` (stream), `GET /anthropic/v1/models` |
| Providers | OpenAI, Anthropic, Gemini, Mistral, Groq, Cohere, Ollama, Cerebras, Perplexity, OpenRouter, Nebius, Parasail, SGLang, ElevenLabs, Azure OpenAI, AWS Bedrock, Google Vertex AI; any OpenAI-compatible API (vLLM, DeepSeek, xAI, Together, LM Studio…) as type `openai` + base URL |
| Automatic fallbacks | routing targets in order; Bifrost fallbacks |
| Load balancing | weighted targets per model and weighted keys per provider |
| Governance (virtual keys, budgets, rate limits) | API keys with RPM/TPM, monthly budgets, model allowlists, expiry, MCP server lists; org credits; multi-tenant orgs |
| MCP (client, tool execution, tool filtering) | operator-registered HTTP/SSE servers, per-org grants, per-key narrowing, `POST /v1/mcp/tool/execute` |
| Observability | Prometheus `/metrics`, request logs, usage analytics by day/model/key, provider health (uptime, latency, throughput), JSON logs, ClickHouse export |
| Health and version | `/health`, `/healthz`, `/readyz`, `/api/version` |
| Enterprise in Bifrost, OSS here | OIDC SSO, RBAC, audit logs, guardrails (regex, PII masking, input limits), circuit breaker, horizontal scaling (Postgres + Valkey) |

## Not supported yet

| Feature | Why | Path forward |
|---|---|---|
| Google GenAI (Gemini) SDK drop-in (`/genai/...`) | converters exist in core v1.15.2; route not built | add route |
| Image generation, video, realtime, containers | in core v1.15.2; routes, pricing and tenant isolation not built | next milestone |
| Batches and Files APIs | in core, but need per-org isolation of file and batch ids and deferred billing | next milestone |
| Semantic cache | needs an embedding model and a vector store; exact-match cache exists | add with Valkey vector search |
| Count tokens endpoints | `CountTokensRequest` in core v1.15.2; route not built | add route |
| Providers added in later cores (Databricks, Hugging Face, Replicate, xAI native, DeepSeek native, vLLM native) | in core v1.15.2; not yet in the admin type list | add types; use `openai` + base URL today |
| LiteLLM / LangChain / PydanticAI / Bedrock / Cohere SDK routes | Bifrost HTTP transport features, not core | add on demand |
| Plugins (Go/WASM), Relays, A2A agent gateway, MCP server mode, code mode | Bifrost HTTP transport features, not core | out of scope for v1 |
| Bifrost enterprise: SCIM, secret managers, alerting, log exports, clustering via gossip | enterprise | partial equivalents: OIDC SSO, Postgres/Valkey scale-out |
