// Provider type labels and logos. Logo files live in web/public/logos (MIT, @lobehub/icons-static-svg
// 1.102.0; see web/public/logos/LICENSE) and are fetched as static files, never bundled.

/** Human names of the Bifrost provider types the gateway configures. */
export const providerLabels: Record<string, string> = {
  openai: "OpenAI",
  anthropic: "Anthropic",
  gemini: "Google Gemini",
  vertex: "Google Vertex AI",
  bedrock: "AWS Bedrock",
  bedrock_mantle: "AWS Bedrock Mantle",
  sagemaker: "AWS SageMaker",
  azure: "Azure OpenAI",
  cloudflare: "Cloudflare Workers AI",
  cohere: "Cohere",
  mistral: "Mistral",
  groq: "Groq",
  cerebras: "Cerebras",
  perplexity: "Perplexity",
  openrouter: "OpenRouter",
  nebius: "Nebius",
  parasail: "Parasail",
  elevenlabs: "ElevenLabs",
  deepseek: "DeepSeek",
  xai: "xAI",
  fireworks: "Fireworks AI",
  huggingface: "Hugging Face",
  replicate: "Replicate",
  ollama: "Ollama",
  sgl: "SGLang",
  vllm: "vLLM",
  databricks: "Databricks",
  minimax: "MiniMax",
  byteplus: "BytePlus",
  sarvam: "Sarvam",
  wafer: "Wafer",
  typesafe: "Typesafe",
  "opencode-go": "OpenCode Go",
  "opencode-zen": "OpenCode Zen",
  runway: "Runway",
  runware: "Runware",
};

export const providerLabel = (type: string) => providerLabels[type] ?? type;

/** Logo file (web/public/logos/<file>.svg) per provider type or model author slug. */
const logoFile: Record<string, string> = {
  bedrock_mantle: "bedrock",
  sagemaker: "aws",
  amazon: "aws",
  "opencode-go": "opencode",
  "opencode-zen": "opencode",
  "x-ai": "xai",
  "meta-llama": "meta",
  mistralai: "mistral",
  moonshotai: "moonshot",
  kimi: "moonshot",
  "z-ai": "zai",
  "zai-org": "zai",
  thudm: "zhipu",
  azureai: "azure",
  "google-vertex": "vertex",
};
for (const f of [
  "openai", "anthropic", "gemini", "google", "vertex", "bedrock", "aws", "azure", "cloudflare", "xai", "huggingface",
  "vllm", "ollama", "opencode", "byteplus", "minimax", "wafer", "typesafe", "runway", "cohere", "mistral", "groq",
  "cerebras", "perplexity", "openrouter", "nebius", "parasail", "elevenlabs", "deepseek", "fireworks", "replicate",
  "meta", "qwen", "moonshot", "zai", "zhipu", "nvidia", "microsoft",
])
  logoFile[f] = f;

/** Files with their own colors (drawn as <img>); every other logo is one-color and follows the text color. */
const colored = new Set([
  "azure", "bedrock", "byteplus", "cloudflare", "cohere", "deepseek", "fireworks", "gemini", "google", "huggingface",
  "meta", "microsoft", "minimax", "mistral", "nvidia", "perplexity", "qwen", "vertex", "vllm", "zhipu",
]);

/** The logo for a provider type or model author slug, or null when there is none (use a monogram). */
export function logoFor(key: string): { src: string; colored: boolean } | null {
  const f = logoFile[key.toLowerCase()];
  return f ? { src: `/logos/${f}.svg`, colored: colored.has(f) } : null;
}

/** The author slug of an OpenRouter-style model id ("meta-llama/llama-3" → "meta-llama"), or "" without one. */
export const modelAuthor = (id: string) => (id.includes("/") ? id.slice(0, id.indexOf("/")) : "");
