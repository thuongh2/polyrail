package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// Snapshot is the immutable in-memory view of providers and models used on the hot path.
// shortcut: every provider and model is held in memory; fine up to ~100k models, shard per org beyond.
type Snapshot struct {
	Version   string
	Providers map[string]store.Provider // by provider id (keys decrypted)
	Models    map[string]store.Model    // by owner + "|" + model id ("_" = public)
	Pools     []store.ProxyPool         // operator proxy pools (member passwords decrypted)
	Presets   map[string]store.Preset   // by org id + "|" + slug
	byBifrost map[schemas.ModelProvider]string
	keyOwner  map[string]string // provider key id -> provider id (loaded providers)
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
	pools, err := s.ListProxyPools(ctx, true)
	if err != nil {
		return nil, err
	}
	presets, err := s.ListPresets(ctx, "*")
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Version: ver, Providers: map[string]store.Provider{}, Models: map[string]store.Model{},
		Pools: pools, Presets: make(map[string]store.Preset, len(presets)), byBifrost: map[schemas.ModelProvider]string{}, keyOwner: map[string]string{}}
	for _, p := range presets {
		snap.Presets[p.OrgID+"|"+p.Slug] = p
	}
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
		for _, pk := range p.Keys {
			snap.keyOwner[pk.ID] = p.ID
		}
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

// providerType is the type of the provider named name that served model m ("" when unknown):
// one of m's targets, else (a fallback model's provider) any provider of that name.
func (s *Snapshot) providerType(m store.Model, name string) string {
	if name == "" {
		return ""
	}
	for _, t := range m.Targets {
		if p, ok := s.Providers[t.ProviderID]; ok && p.Name == name {
			return p.Type
		}
	}
	for _, p := range s.Providers {
		if p.Name == name && (p.OrgID == "" || p.OrgID == m.OrgID) {
			return p.Type
		}
	}
	return ""
}

// liveIn reports whether workspace ws of org can use model m: a live public model, or a live org
// model of that workspace with a target provider usable there (D31).
func (s *Snapshot) liveIn(m store.Model, ws string) bool {
	if m.OrgID == "" {
		return s.Live(m)
	}
	if !m.Enabled || store.WSID(m.WorkspaceID) != store.WSID(ws) {
		return false
	}
	for _, t := range m.Targets {
		if p, ok := s.Providers[t.ProviderID]; ok && p.UsableIn(m.OrgID, ws) {
			return true
		}
	}
	return false
}

// Model resolves a live model id for workspace ws ("" = default) of an org: org-private first,
// then public.
func (s *Snapshot) Model(orgID, ws, id string) (store.Model, bool) {
	if m, ok := s.Models[orgID+"|"+id]; ok && s.liveIn(m, ws) {
		return m, true
	}
	m, ok := s.Models["_|"+id]
	return m, ok && s.Live(m)
}

// Preset returns the preset slug of org when workspace ws may use it.
func (s *Snapshot) Preset(orgID, ws, slug string) (store.Preset, bool) {
	p, ok := s.Presets[orgID+"|"+slug]
	return p, ok && store.WSID(p.WorkspaceID) == store.WSID(ws)
}

// Visible lists the live models workspace ws of an org can use, sorted by id.
func (s *Snapshot) Visible(orgID, ws string) []store.Model {
	seen := map[string]bool{}
	var out []store.Model
	for _, owner := range []string{orgID, "_"} {
		for _, m := range s.Models {
			if (m.OrgID == owner || (owner == "_" && m.OrgID == "")) && !seen[m.ID] && s.liveIn(m, ws) {
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
	pools        *poolForwarder
	poolNoProxy  string // UPSTREAM_NO_PROXY, for the global pool
	// keys filters out unavailable keys (nil: no status filtering, for pinned-key lookups);
	// unavailable is called when a provider has no available key.
	keys        *keyHealth
	unavailable func(store.Provider)
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

// GetKeysForProvider returns the provider's available keys: not disabled, not quarantined, not
// cooling down (docs/KEYS.md). Bifrost calls it per request. When none is available it reports
// the provider unavailable and returns every enabled key, so the request still runs and fails
// over through normal routing; with no enabled key at all it returns an error, which Bifrost
// treats as this provider failing.
// shortcut: availability ignores the requested model; a pool whose only available keys do not
// serve the model fails over like an unavailable provider.
func (a *account) GetKeysForProvider(_ context.Context, k schemas.ModelProvider) ([]schemas.Key, error) {
	p, err := a.provider(k)
	if err != nil {
		return nil, err
	}
	if len(p.Keys) == 0 && (p.Type == "ollama" || p.Type == "sgl" || p.Type == "vllm") {
		// Bifrost reads these servers' URL from a key; keyless providers get one implicit key.
		return []schemas.Key{withServerURL(p, schemas.Key{ID: p.ID, Name: "default", Models: schemas.WhiteList{"*"}, Weight: 1})}, nil
	}
	enabled := make([]schemas.Key, 0, len(p.Keys))
	avail := make([]schemas.Key, 0, len(p.Keys))
	for _, pk := range p.Keys {
		w := pk.Weight
		if w <= 0 {
			w = 1
		}
		if p.OrgID != "" && pk.Bedrock != nil && pk.Value == "" && pk.Bedrock.AccessKey == "" {
			continue // tenants never borrow the gateway host's AWS identity (admin rejects this too)
		}
		if a.keys == nil { // pinned-key lookup: every key
			avail = append(avail, withServerURL(p, bifrostKey(p.Type, pk, w)))
			continue
		}
		if pk.Disabled {
			continue
		}
		bk := withServerURL(p, bifrostKey(p.Type, pk, w))
		enabled = append(enabled, bk)
		if _, cooling := a.keys.cooling(pk.ID); !pk.Quarantined && !cooling {
			avail = append(avail, bk)
		}
	}
	if len(avail) > 0 || len(p.Keys) == 0 {
		return avail, nil
	}
	if a.unavailable != nil {
		a.unavailable(p)
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("provider %s has no enabled key", k)
	}
	return enabled, nil
}

// v1Appending are the provider types whose Bifrost adapter appends "/v1/..." to the base URL.
var v1Appending = map[string]bool{"openai": true, "ollama": true, "vllm": true, "sgl": true}

// UpstreamBase is the base URL handed to Bifrost: without a trailing slash and, for adapters that
// add "/v1/..." themselves, without a trailing "/v1" — vendor docs list base URLs as
// "https://host/v1", which would otherwise become /v1/v1/chat/completions (a 404 on every key).
func UpstreamBase(t, u string) string {
	u = strings.TrimRight(u, "/")
	if v1Appending[t] || OpenAICompatible[t] != "" {
		u = strings.TrimSuffix(u, "/v1")
	}
	return u
}

// withServerURL sets the per-key server URL Bifrost requires for Ollama, SGLang and vLLM to the
// provider's base URL (the only place the platform configures it).
func withServerURL(p store.Provider, k schemas.Key) schemas.Key {
	p.BaseURL = UpstreamBase(p.Type, p.BaseURL)
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
	// Name is the stored key id as well: Bifrost reports the serving key's name in RoutingInfo.Key.
	k := schemas.Key{ID: pk.ID, Name: pk.ID, Value: secret(pk.Value), Models: models, Weight: w}
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
			BaseURL:                        UpstreamBase(p.Type, p.BaseURL),
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
