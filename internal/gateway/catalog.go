package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// Snapshot is the immutable in-memory view of providers and models used on the hot path.
// shortcut: every provider and model is held in memory; fine up to ~100k models, shard per org beyond.
type Snapshot struct {
	Version   string
	Providers map[string]store.Provider // by provider id (keys decrypted)
	Models    map[string]store.Model    // by owner + "|" + model id ("_" = public)
	byBifrost map[schemas.ModelProvider]string
}

// MultiInstance reports whether many providers of this type can coexist (public and per-org):
// the types Bifrost can instantiate many times under custom names (schemas.SupportedBaseProviders).
func MultiInstance(t string) bool { return bifrost.IsSupportedBaseProvider(schemas.ModelProvider(t)) }

// OpenAICompatible maps key-only provider types whose API is OpenAI-compatible to the vendor's API
// base, as Bifrost v1.15.2's native adapters default it, without /v1 (the openai base appends
// /v1/...). Org providers of these types run as Bifrost custom providers on the openai base at
// this fixed URL, so every org can have its own; platform providers keep the native adapter.
// Not here: byteplus (/api/v3 paths, regional host, no model list), runware and runway (task
// APIs), elevenlabs (not OpenAI-compatible). A var only so tests can point a vendor at httptest.
var OpenAICompatible = map[string]string{
	"groq":         "https://api.groq.com/openai",
	"deepseek":     "https://api.deepseek.com",
	"xai":          "https://api.x.ai",
	"mistral":      "https://api.mistral.ai",
	"openrouter":   "https://openrouter.ai/api",
	"fireworks":    "https://api.fireworks.ai/inference",
	"cerebras":     "https://api.cerebras.ai",
	"perplexity":   "https://api.perplexity.ai",
	"nebius":       "https://api.tokenfactory.nebius.com",
	"parasail":     "https://api.parasail.io",
	"minimax":      "https://api.minimax.io",
	"sarvam":       "https://api.sarvam.ai",
	"wafer":        "https://pass.wafer.ai",
	"opencode-go":  "https://opencode.ai/zen/go",
	"opencode-zen": "https://opencode.ai/zen",
}

// compatPaths are the vendor paths that differ from OpenAI's /v1/... (from the native adapters).
var compatPaths = map[string]map[schemas.RequestType]string{
	"deepseek": {schemas.ChatCompletionRequest: "/chat/completions", schemas.ChatCompletionStreamRequest: "/chat/completions",
		schemas.ListModelsRequest: "/models"},
	"perplexity": {schemas.ChatCompletionRequest: "/chat/completions", schemas.ChatCompletionStreamRequest: "/chat/completions"},
}

// OrgType reports whether org admins may create providers of type t: the multi-instance types
// and the OpenAI-compatible vendors.
func OrgType(t string) bool { return MultiInstance(t) || OpenAICompatible[t] != "" }

// orgCompatible reports whether p is an org provider served through the openai base.
func orgCompatible(p store.Provider) bool { return p.OrgID != "" && OpenAICompatible[p.Type] != "" }

// BifrostKey is the Bifrost provider key a provider is registered under.
func BifrostKey(p store.Provider) schemas.ModelProvider {
	if MultiInstance(p.Type) || orgCompatible(p) {
		return schemas.ModelProvider("p-" + p.ID)
	}
	return schemas.ModelProvider(p.Type)
}

func loadSnapshot(ctx context.Context, s *store.Store) (*Snapshot, error) {
	ver, err := s.Version(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := s.ListProviders(ctx, true)
	if err != nil {
		return nil, err
	}
	ms, err := s.ListModels(ctx, "*")
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Version: ver, Providers: map[string]store.Provider{}, Models: map[string]store.Model{},
		byBifrost: map[schemas.ModelProvider]string{}}
	sort.Slice(ps, func(i, j int) bool { return ps[i].CreatedAt.Before(ps[j].CreatedAt) })
	for _, p := range ps {
		if !p.Enabled {
			continue
		}
		k := BifrostKey(p)
		if _, dup := snap.byBifrost[k]; dup {
			continue // a second non-custom provider of the same type: oldest wins (documented)
		}
		snap.byBifrost[k] = p.ID
		snap.Providers[p.ID] = p
	}
	for _, m := range ms {
		owner := m.OrgID
		if owner == "" {
			owner = "_"
		}
		snap.Models[owner+"|"+m.ID] = m
	}
	return snap, nil
}

// Live reports whether a model is enabled and at least one of its targets' providers is loaded
// (enabled). Catalog models of providers nobody has turned on stay hidden and unroutable.
func (s *Snapshot) Live(m store.Model) bool {
	if !m.Enabled {
		return false
	}
	for _, t := range m.Targets {
		if _, ok := s.Providers[t.ProviderID]; ok {
			return true
		}
	}
	return false
}

// Model resolves a live model id for an org: org-private first, then public.
func (s *Snapshot) Model(orgID, id string) (store.Model, bool) {
	if m, ok := s.Models[orgID+"|"+id]; ok && s.Live(m) {
		return m, true
	}
	m, ok := s.Models["_|"+id]
	return m, ok && s.Live(m)
}

// Visible lists the live models an org can use, sorted by id.
func (s *Snapshot) Visible(orgID string) []store.Model {
	seen := map[string]bool{}
	var out []store.Model
	for _, owner := range []string{orgID, "_"} {
		for _, m := range s.Models {
			if (m.OrgID == owner || (owner == "_" && m.OrgID == "")) && !seen[m.ID] && s.Live(m) {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- Bifrost Account backed by the current snapshot ----

type account struct {
	snap         func() *Snapshot
	concurrency  int
	buffer       int
	allowPrivate bool // AllowPrivateURLs: tenant providers may dial private networks too
	proxy        *GlobalProxy
	tenantProxy  bool // AllowTenantProxy: org providers' own proxy settings apply
}

func (a *account) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	s := a.snap()
	out := make([]schemas.ModelProvider, 0, len(s.byBifrost))
	for k := range s.byBifrost {
		out = append(out, k)
	}
	return out, nil
}

func (a *account) provider(k schemas.ModelProvider) (store.Provider, error) {
	s := a.snap()
	p, ok := s.Providers[s.byBifrost[k]]
	if !ok {
		return p, fmt.Errorf("provider %s not configured", k)
	}
	return p, nil
}

func (a *account) GetKeysForProvider(_ context.Context, k schemas.ModelProvider) ([]schemas.Key, error) {
	p, err := a.provider(k)
	if err != nil {
		return nil, err
	}
	if len(p.Keys) == 0 && (p.Type == "ollama" || p.Type == "sgl" || p.Type == "vllm") {
		// Bifrost reads these servers' URL from a key; keyless providers get one implicit key.
		return []schemas.Key{withServerURL(p, schemas.Key{ID: p.ID, Name: "default", Models: schemas.WhiteList{"*"}, Weight: 1})}, nil
	}
	keys := make([]schemas.Key, 0, len(p.Keys))
	for _, pk := range p.Keys {
		w := pk.Weight
		if w <= 0 {
			w = 1
		}
		if p.OrgID != "" && pk.Bedrock != nil && pk.Value == "" && pk.Bedrock.AccessKey == "" {
			continue // tenants never borrow the gateway host's AWS identity (admin rejects this too)
		}
		keys = append(keys, withServerURL(p, bifrostKey(p.Type, pk, w)))
	}
	return keys, nil
}

// withServerURL sets the per-key server URL Bifrost requires for Ollama, SGLang and vLLM to the
// provider's base URL (the only place the platform configures it).
func withServerURL(p store.Provider, k schemas.Key) schemas.Key {
	switch p.Type {
	case "vllm":
		k.VLLMKeyConfig = &schemas.VLLMKeyConfig{URL: secret(p.BaseURL)}
	case "ollama":
		k.OllamaKeyConfig = &schemas.OllamaKeyConfig{URL: secret(p.BaseURL)}
	case "sgl":
		k.SGLKeyConfig = &schemas.SGLKeyConfig{URL: secret(p.BaseURL)}
	}
	return k
}

// secret wraps a stored value as a literal SecretVar. schemas.NewSecretVar would resolve
// "env.*" and "vault.*" references, letting a tenant send the gateway host's env vars upstream.
func secret(v string) schemas.SecretVar {
	return schemas.SecretVar{Val: v, SecretType: schemas.SecretTypePlainText}
}

func secretPtr(v string) *schemas.SecretVar {
	if v == "" {
		return nil
	}
	s := secret(v)
	return &s
}

// aliases maps model name -> deployment (Azure deployment, Bedrock profile/ARN, Vertex endpoint)
// to Bifrost key aliases; azure carries the key's api-version on every alias.
func aliases(deployments map[string]string, azure *schemas.AzureAliasCfg) schemas.KeyAliases {
	if len(deployments) == 0 {
		return nil
	}
	out := make(schemas.KeyAliases, len(deployments))
	for model, dep := range deployments {
		out[model] = schemas.AliasConfig{ModelID: dep, AzureAliasCfg: azure}
	}
	return out
}

// bifrostKey maps a stored provider key (secrets decrypted) of provider type t to Bifrost's key
// schema. bedrock, bedrock_mantle and sagemaker keys share the stored AWS config.
func bifrostKey(t string, pk store.ProviderKey, w float64) schemas.Key {
	models := schemas.WhiteList(pk.Models)
	if len(models) == 0 {
		models = schemas.WhiteList{"*"} // empty means every model here; Bifrost reads empty as none
	}
	k := schemas.Key{ID: pk.ID, Name: pk.Name, Value: secret(pk.Value), Models: models, Weight: w}
	if a := pk.Azure; a != nil {
		k.AzureKeyConfig = &schemas.AzureKeyConfig{Endpoint: secret(strings.TrimRight(a.Endpoint, "/"))}
		var cfg *schemas.AzureAliasCfg
		if a.APIVersion != "" {
			cfg = &schemas.AzureAliasCfg{APIVersion: &a.APIVersion}
		}
		k.Aliases = aliases(a.Deployments, cfg)
	}
	if b := pk.Bedrock; b != nil && t == "bedrock_mantle" {
		k.BedrockMantleKeyConfig = &schemas.BedrockMantleKeyConfig{AccessKey: secret(b.AccessKey), SecretKey: secret(b.SecretKey),
			SessionToken: secretPtr(b.SessionToken), Region: secretPtr(b.Region)}
		k.Aliases = aliases(b.Deployments, nil)
	} else if b != nil && t == "sagemaker" {
		k.SageMakerKeyConfig = &schemas.SageMakerKeyConfig{AccessKey: secret(b.AccessKey), SecretKey: secret(b.SecretKey),
			SessionToken: secretPtr(b.SessionToken), Region: secretPtr(b.Region)}
		k.Aliases = aliases(b.Deployments, nil)
	} else if b != nil {
		k.BedrockKeyConfig = &schemas.BedrockKeyConfig{AccessKey: secret(b.AccessKey), SecretKey: secret(b.SecretKey),
			SessionToken: secretPtr(b.SessionToken), Region: secretPtr(b.Region), ARN: secretPtr(b.ARN)}
		k.Aliases = aliases(b.Deployments, nil)
	}
	if c := pk.Cloudflare; c != nil {
		k.CloudflareKeyConfig = &schemas.CloudflareKeyConfig{AccountID: secret(c.AccountID)}
	}
	if v := pk.Vertex; v != nil {
		k.VertexKeyConfig = &schemas.VertexKeyConfig{ProjectID: secret(v.ProjectID), ProjectNumber: secret(v.ProjectNumber),
			Region: secret(v.Region), AuthCredentials: secret(v.AuthCredentials)}
		k.Aliases = aliases(v.Deployments, nil)
	}
	return k
}

func (a *account) GetConfigForProvider(k schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	p, err := a.provider(k)
	if err != nil {
		return nil, err
	}
	timeout := p.TimeoutSeconds
	if timeout <= 0 {
		timeout = 120
	}
	proxy, err := a.proxyConfig(p)
	if err != nil {
		return nil, err
	}
	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        p.BaseURL,
			DefaultRequestTimeoutInSeconds: timeout,
			MaxRetries:                     1,
			RetryBackoffInitial:            200 * time.Millisecond,
			RetryBackoffMax:                2 * time.Second,
			// Bifrost's dialer refuses RFC 1918 targets unless allowed: operators may use private
			// hosts (Ollama on the LAN); tenant providers only when AllowPrivateURLs is set.
			AllowPrivateNetwork: p.OrgID == "" || a.allowPrivate,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: a.concurrency, BufferSize: a.buffer},
		ProxyConfig:              proxy,
	}
	if MultiInstance(p.Type) {
		cfg.CustomProviderConfig = &schemas.CustomProviderConfig{
			BaseProviderType: schemas.ModelProvider(p.Type),
			IsKeyLess:        len(p.Keys) == 0,
		}
	} else if orgCompatible(p) {
		// the URL is fixed (admin rejects base_url for these org types): no tenant-chosen host
		cfg.NetworkConfig.BaseURL = OpenAICompatible[p.Type]
		cfg.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI,
			RequestPathOverrides: compatPaths[p.Type]}
	}
	return cfg, nil
}
