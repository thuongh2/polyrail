package gateway

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
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

// BifrostKey is the Bifrost provider key a provider is registered under.
func BifrostKey(p store.Provider) schemas.ModelProvider {
	if MultiInstance(p.Type) {
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

// Model resolves a model id for an org: org-private first, then public.
func (s *Snapshot) Model(orgID, id string) (store.Model, bool) {
	if m, ok := s.Models[orgID+"|"+id]; ok && m.Enabled {
		return m, true
	}
	m, ok := s.Models["_|"+id]
	return m, ok && m.Enabled
}

// Visible lists models an org can use, sorted by id.
func (s *Snapshot) Visible(orgID string) []store.Model {
	seen := map[string]bool{}
	var out []store.Model
	for _, owner := range []string{orgID, "_"} {
		for _, m := range s.Models {
			if (m.OrgID == owner || (owner == "_" && m.OrgID == "")) && m.Enabled && !seen[m.ID] {
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
	snap        func() *Snapshot
	concurrency int
	buffer      int
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
		keys = append(keys, withServerURL(p, bifrostKey(pk, w)))
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

// bifrostKey maps a stored provider key (secrets decrypted) to Bifrost's key schema.
func bifrostKey(pk store.ProviderKey, w float64) schemas.Key {
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
	if b := pk.Bedrock; b != nil {
		k.BedrockKeyConfig = &schemas.BedrockKeyConfig{AccessKey: secret(b.AccessKey), SecretKey: secret(b.SecretKey),
			SessionToken: secretPtr(b.SessionToken), Region: secretPtr(b.Region), ARN: secretPtr(b.ARN)}
		k.Aliases = aliases(b.Deployments, nil)
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
	cfg := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        p.BaseURL,
			DefaultRequestTimeoutInSeconds: timeout,
			MaxRetries:                     1,
			RetryBackoffInitial:            200 * time.Millisecond,
			RetryBackoffMax:                2 * time.Second,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: a.concurrency, BufferSize: a.buffer},
	}
	if MultiInstance(p.Type) {
		cfg.CustomProviderConfig = &schemas.CustomProviderConfig{
			BaseProviderType: schemas.ModelProvider(p.Type),
			IsKeyLess:        len(p.Keys) == 0,
		}
	}
	return cfg, nil
}

// ---- routing ----

// breaker skips targets that failed repeatedly, for a cool-down period.
type breaker struct {
	mu        sync.Mutex
	fails     map[string]int
	openUntil map[string]time.Time
	threshold int
	cooldown  time.Duration
}

func newBreaker() *breaker {
	return &breaker{fails: map[string]int{}, openUntil: map[string]time.Time{}, threshold: 3, cooldown: 30 * time.Second}
}

func (b *breaker) ok(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().After(b.openUntil[id])
}

func (b *breaker) result(id string, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		delete(b.fails, id)
		return
	}
	b.fails[id]++
	if b.fails[id] >= b.threshold {
		b.openUntil[id] = time.Now().Add(b.cooldown)
		b.fails[id] = 0
	}
}

func targetID(t store.Target) string { return t.ProviderID + "|" + t.Model }

// plan orders a model's targets: primary picked by weight among healthy targets, then the other
// healthy targets by weight, then open-circuit targets as a last resort.
func plan(m store.Model, snap *Snapshot, br *breaker) []store.Target {
	var healthy, sick []store.Target
	for _, t := range m.Targets {
		if _, ok := snap.Providers[t.ProviderID]; !ok {
			continue
		}
		if br.ok(targetID(t)) {
			healthy = append(healthy, t)
		} else {
			sick = append(sick, t)
		}
	}
	sort.SliceStable(healthy, func(i, j int) bool { return healthy[i].Weight > healthy[j].Weight })
	if len(healthy) > 1 {
		var total float64
		for _, t := range healthy {
			total += weight(t)
		}
		r := rand.Float64() * total
		for i, t := range healthy {
			if r -= weight(t); r < 0 {
				healthy[0], healthy[i] = healthy[i], healthy[0]
				break
			}
		}
	}
	return append(healthy, sick...)
}

func weight(t store.Target) float64 {
	if t.Weight <= 0 {
		return 1
	}
	return t.Weight
}
