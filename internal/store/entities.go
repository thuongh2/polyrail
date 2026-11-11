package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrExists  = errors.New("already exists")
	ErrInvalid = errors.New("invalid")
)

type Role string

const (
	RoleViewer Role = "viewer"
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// Rank orders roles; 0 means unknown role.
func (r Role) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleMember:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	}
	return 0
}

type Guardrails struct {
	MaxInputChars   int      `json:"max_input_chars,omitempty"`
	BlockedPatterns []string `json:"blocked_patterns,omitempty"` // Go regexps, matched on user/system text
	MaskPII         bool     `json:"mask_pii,omitempty"`
	MaskSecrets     bool     `json:"mask_secrets,omitempty"`     // replace API keys, tokens, private keys with [REDACTED:<type>]
	PromptInjection string   `json:"prompt_injection,omitempty"` // "" (off) | "warn" | "block"; user text only
	Compression     string   `json:"compression,omitempty"`      // "" (off) | "lossless"; chat and Responses only
}

type Org struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Prepaid       bool           `json:"prepaid"`        // if true, requests need credits
	CreditsMicros int64          `json:"credits_micros"` // total credits granted, micro-USD
	MaxTokensCap  int            `json:"max_tokens_cap,omitempty"`
	Guardrails    Guardrails     `json:"guardrails"`
	MCPServers    []string       `json:"mcp_servers,omitempty"` // MCP server names this org may use ("*" = all); set by operators
	SemanticCache *SemanticCache `json:"semantic_cache,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// SemanticCache opts an org into answering near-duplicate prompts from cache (gateway/semcache.go).
type SemanticCache struct {
	Model      string  `json:"model"`       // embedding model id visible to the org
	Threshold  float64 `json:"threshold"`   // cosine similarity needed for a hit, 0.5..0.999
	TTLSeconds int     `json:"ttl_seconds"` // entry lifetime
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"` // persisted via storedUser only, never in API output
	Operator     bool      `json:"operator"`
	SSOSubject   string    `json:"sso_subject,omitempty"` // OIDC issuer|subject this account is linked to
	SessionGen   int       `json:"session_gen,omitempty"` // bumped on sign-out to end every session
	Disabled     bool      `json:"disabled,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Member struct {
	OrgID  string `json:"org_id"`
	UserID string `json:"user_id"`
	Role   Role   `json:"role"`
}

type APIKey struct {
	ID            string     `json:"id"`
	OrgID         string     `json:"org_id"`
	Name          string     `json:"name"`
	Prefix        string     `json:"prefix"` // first chars of the secret, for display
	Hash          string     `json:"-"`
	CreatedBy     string     `json:"created_by"`
	RPM           int        `json:"rpm,omitempty"`
	TPM           int        `json:"tpm,omitempty"`
	MonthlyBudget int64      `json:"monthly_budget_micros,omitempty"`
	Models        []string   `json:"models,omitempty"`      // allowlist; empty = all visible models
	MCPServers    []string   `json:"mcp_servers,omitempty"` // MCP server names, narrowing the org grant; empty = all granted
	KBIDs         []string   `json:"kb_ids,omitempty"`      // knowledge bases grounding this key's requests (docs/KB.md §4)
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Revoked       bool       `json:"revoked"`
	CreatedAt     time.Time  `json:"created_at"`
}

// storedUser keeps the password hash in the persisted form while hiding it from API JSON.
type storedUser struct {
	User
	PasswordHash string `json:"password_hash,omitempty"`
}

func (s storedUser) user() *User {
	u := s.User
	u.PasswordHash = s.PasswordHash
	return &u
}

// storedKey keeps the hash in the persisted form while hiding it from API JSON.
type storedKey struct {
	APIKey
	Hash string `json:"hash"`
}

type ProviderKey struct {
	ID      string         `json:"id"`
	Name    string         `json:"name,omitempty"`
	Value   string         `json:"value,omitempty"`  // plaintext; only set on input and inside the gateway
	Sealed  string         `json:"sealed,omitempty"` // encrypted at rest
	Weight  float64        `json:"weight"`
	Models  []string       `json:"models,omitempty"`
	Azure   *AzureConfig   `json:"azure,omitempty"`
	Bedrock *BedrockConfig `json:"bedrock,omitempty"`
	Vertex  *VertexConfig  `json:"vertex,omitempty"`
	// Cloudflare is the Workers AI account; Value holds the API token.
	Cloudflare *CloudflareConfig `json:"cloudflare,omitempty"`
	// Key status (docs/KEYS.md). Stored keys without these fields read as enabled. They are set
	// through the key API and the gateway; a provider PUT keeps the stored values of existing keys.
	Disabled     bool   `json:"disabled,omitempty"`      // manual: never used
	Quarantined  bool   `json:"quarantined,omitempty"`   // repeated auth failures or an operator action
	StatusReason string `json:"status_reason,omitempty"` // why quarantined (no upstream text)
}

// CloudflareConfig is the per-key Cloudflare Workers AI setup.
type CloudflareConfig struct {
	AccountID string `json:"account_id"`
}

// AzureConfig is the per-key Azure OpenAI setup. Deployments map model name -> deployment name.
type AzureConfig struct {
	Endpoint    string            `json:"endpoint"`
	APIVersion  string            `json:"api_version,omitempty"`
	Deployments map[string]string `json:"deployments,omitempty"`
}

// BedrockConfig is the per-key AWS setup of bedrock, bedrock_mantle and sagemaker keys. Secret fields follow the Value/Sealed pattern:
// plaintext only on input and inside the gateway, sealed at rest. No access/secret key and no
// Value means the AWS default credential chain (IAM role) of the gateway host.
type BedrockConfig struct {
	AccessKey          string            `json:"access_key,omitempty"`
	SecretKey          string            `json:"secret_key,omitempty"`
	SessionToken       string            `json:"session_token,omitempty"`
	SealedAccessKey    string            `json:"sealed_access_key,omitempty"`
	SealedSecretKey    string            `json:"sealed_secret_key,omitempty"`
	SealedSessionToken string            `json:"sealed_session_token,omitempty"`
	Region             string            `json:"region"`
	ARN                string            `json:"arn,omitempty"`
	Deployments        map[string]string `json:"deployments,omitempty"`
}

// VertexConfig is the per-key Google Vertex AI setup. Empty AuthCredentials means the gateway
// host's Google default credentials (IAM).
type VertexConfig struct {
	ProjectID             string            `json:"project_id"`
	ProjectNumber         string            `json:"project_number,omitempty"`
	Region                string            `json:"region"`
	AuthCredentials       string            `json:"auth_credentials,omitempty"` // service-account JSON
	SealedAuthCredentials string            `json:"sealed_auth_credentials,omitempty"`
	Deployments           map[string]string `json:"deployments,omitempty"`
}

// secrets maps each secret field of a key to its (plaintext, sealed) pair.
func (k *ProviderKey) secrets() map[string][2]*string {
	f := map[string][2]*string{"value": {&k.Value, &k.Sealed}}
	if b := k.Bedrock; b != nil {
		f["bedrock.access_key"] = [2]*string{&b.AccessKey, &b.SealedAccessKey}
		f["bedrock.secret_key"] = [2]*string{&b.SecretKey, &b.SealedSecretKey}
		f["bedrock.session_token"] = [2]*string{&b.SessionToken, &b.SealedSessionToken}
	}
	if v := k.Vertex; v != nil {
		f["vertex.auth_credentials"] = [2]*string{&v.AuthCredentials, &v.SealedAuthCredentials}
	}
	return f
}

// Redacted returns the key without any secret material (plaintext or sealed); non-secret
// config (endpoint, region, deployments) stays so clients can edit it.
func (k ProviderKey) Redacted() ProviderKey {
	out := ProviderKey{ID: k.ID, Name: k.Name, Weight: k.Weight, Models: k.Models,
		Disabled: k.Disabled, Quarantined: k.Quarantined, StatusReason: k.StatusReason}
	if k.Azure != nil {
		a := *k.Azure
		out.Azure = &a
	}
	if k.Bedrock != nil {
		out.Bedrock = &BedrockConfig{Region: k.Bedrock.Region, ARN: k.Bedrock.ARN, Deployments: k.Bedrock.Deployments}
	}
	if k.Cloudflare != nil {
		c := *k.Cloudflare
		out.Cloudflare = &c
	}
	if k.Vertex != nil {
		v := *k.Vertex
		v.AuthCredentials, v.SealedAuthCredentials = "", ""
		out.Vertex = &v
	}
	return out
}

type Provider struct {
	ID             string        `json:"id"`
	OrgID          string        `json:"org_id,omitempty"` // empty = public (platform) provider
	Name           string        `json:"name"`
	Type           string        `json:"type"` // Bifrost provider type: openai, anthropic, gemini, ...
	BaseURL        string        `json:"base_url,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
	Keys           []ProviderKey `json:"keys"`
	// Proxy routes this provider's upstream calls through an HTTP(S) or SOCKS5 proxy; nil uses
	// the gateway's global proxy (UPSTREAM_PROXY_URL) unless NoProxy is set.
	Proxy   *ProviderProxy `json:"proxy,omitempty"`
	NoProxy bool           `json:"no_proxy,omitempty"` // connect directly even when a global proxy is set
	// ProxyPool names an operator proxy pool (ProxyPool.ID) to rotate through instead of one
	// proxy; platform providers only, never together with Proxy.
	ProxyPool string    `json:"proxy_pool,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// ProviderProxy is an outbound proxy. Password follows the Value/Sealed pattern: plaintext only on
// input and inside the gateway, sealed at rest, never returned (see Redacted).
type ProviderProxy struct {
	URL            string `json:"url"` // http://, https:// or socks5://host:port
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	SealedPassword string `json:"sealed_password,omitempty"`
	CACertPEM      string `json:"ca_cert_pem,omitempty"` // extra CA to trust (TLS-inspecting proxy, https proxy)
}

// Redacted returns the proxy without its password (plaintext or sealed).
func (x *ProviderProxy) Redacted() *ProviderProxy {
	if x == nil {
		return nil
	}
	out := *x
	out.Password, out.SealedPassword = "", ""
	return &out
}

type Target struct {
	ProviderID string  `json:"provider_id"`
	Model      string  `json:"model"` // upstream model name
	Weight     float64 `json:"weight"`
	// InputPrice/OutputPrice (micro-USD per 1M tokens) override the model price for the cost
	// routing strategy only; billing always uses the model price.
	InputPrice  int64 `json:"input_price_micros,omitempty"`
	OutputPrice int64 `json:"output_price_micros,omitempty"`
}

type Model struct {
	ID            string   `json:"id"` // public id, e.g. "openai/gpt-4o-mini"
	OrgID         string   `json:"org_id,omitempty"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Kind          string   `json:"kind"` // one of ModelKinds
	ContextLength int      `json:"context_length,omitempty"`
	MaxOutput     int      `json:"max_output_tokens,omitempty"`
	Modalities    []string `json:"modalities,omitempty"`
	InputPrice    int64    `json:"input_price_micros"`  // micro-USD per 1M input tokens
	OutputPrice   int64    `json:"output_price_micros"` // micro-USD per 1M output tokens
	// AudioMinutePrice is micro-USD per minute of input audio, for transcription models whose
	// provider reports duration instead of tokens.
	AudioMinutePrice int64     `json:"audio_minute_price_micros,omitempty"`
	ImagePrice       int64     `json:"image_price_micros,omitempty"`        // micro-USD per generated image
	RequestPrice     int64     `json:"request_price_micros,omitempty"`      // micro-USD per successful request, any kind
	VideoSecondPrice int64     `json:"video_second_price_micros,omitempty"` // micro-USD per second of generated video
	Targets          []Target  `json:"targets"`
	Strategy         string    `json:"strategy,omitempty"` // one of RoutingStrategies; "" = weighted
	Enabled          bool      `json:"enabled"`
	CreatedAt        time.Time `json:"created_at"`
}

type AuditEntry struct {
	ID      string    `json:"id"`
	OrgID   string    `json:"org_id"` // "_" for platform-level actions
	ActorID string    `json:"actor_id"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
}

type RequestLog struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	KeyID     string    `json:"key_id"`
	Model     string    `json:"model"`
	Provider  string    `json:"provider,omitempty"`
	Upstream  string    `json:"upstream_model,omitempty"`
	Status    int       `json:"status"`
	Error     string    `json:"error,omitempty"`
	InTokens  int       `json:"in_tokens"`
	OutTokens int       `json:"out_tokens"`
	Cost      int64     `json:"cost_micros"`
	LatencyMs int64     `json:"latency_ms"`
	Cached    bool      `json:"cached,omitempty"`
	Stream    bool      `json:"stream,omitempty"`
	KBChunks  int       `json:"kb_chunks,omitempty"` // knowledge-base excerpts injected (docs/KB.md §4)
	At        time.Time `json:"at"`
}

type MCPServer struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"` // http | sse
	URL     string   `json:"url"`
	Tools   []string `json:"tools"` // ["*"] = all
	Enabled bool     `json:"enabled"`
}

// Store is the entity layer over KV.
type Store struct {
	KV  KV
	box *Box
}

func New(kv KV, box *Box) *Store { return &Store{KV: kv, box: box} }

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func getJSON[T any](get func(string) ([]byte, error), key string) (*T, error) {
	raw, err := get(key)
	if err != nil {
		return nil, err
	}
	v := new(T)
	return v, json.Unmarshal(raw, v)
}

func putJSON(tx Tx, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return tx.Put(key, raw)
}

func listJSON[T any](ctx context.Context, kv KV, prefix, after string, limit int) ([]T, error) {
	items, err := kv.Scan(ctx, prefix, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		var v T
		if err := json.Unmarshal(it.Value, &v); err != nil {
			return nil, fmt.Errorf("decode %s: %w", it.Key, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Store) get(ctx context.Context) func(string) ([]byte, error) {
	return func(k string) ([]byte, error) { return s.KV.Get(ctx, k) }
}

// update runs fn and bumps the config version in the same transaction, so every node notices.
func (s *Store) update(ctx context.Context, fn func(Tx) error) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Put("meta/version", []byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	})
}

// Version changes whenever control-plane data changes.
func (s *Store) Version(ctx context.Context) (string, error) {
	v, err := s.KV.Get(ctx, "meta/version")
	if errors.Is(err, ErrNotFound) {
		return "0", nil
	}
	return string(v), err
}

// ---- orgs ----

func (s *Store) CreateOrg(ctx context.Context, o *Org, ownerID string) error {
	if strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("%w: org name required", ErrInvalid)
	}
	o.ID, o.CreatedAt = NewID(), time.Now().UTC()
	return s.update(ctx, func(tx Tx) error {
		if err := putJSON(tx, "org/"+o.ID, o); err != nil {
			return err
		}
		if ownerID == "" {
			return nil
		}
		return putMember(tx, Member{OrgID: o.ID, UserID: ownerID, Role: RoleOwner})
	})
}

func (s *Store) GetOrg(ctx context.Context, id string) (*Org, error) {
	return getJSON[Org](s.get(ctx), "org/"+id)
}

func (s *Store) UpdateOrg(ctx context.Context, id string, fn func(*Org) error) (*Org, error) {
	var out *Org
	err := s.update(ctx, func(tx Tx) error {
		o, err := getJSON[Org](tx.Get, "org/"+id)
		if err != nil {
			return err
		}
		if err := fn(o); err != nil {
			return err
		}
		o.ID, out = id, o
		return putJSON(tx, "org/"+id, o)
	})
	return out, err
}

func (s *Store) ListOrgs(ctx context.Context, after string, limit int) ([]Org, error) {
	if after != "" {
		after = "org/" + after
	}
	return listJSON[Org](ctx, s.KV, "org/", after, limit)
}

// DeleteOrg removes the org, its memberships, API keys, private providers, models, webhooks and
// knowledge bases (workers drop the org's KB queue entries).
func (s *Store) DeleteOrg(ctx context.Context, id string) error {
	members, err := s.ListMembers(ctx, id)
	if err != nil {
		return err
	}
	keys, err := s.ListKeys(ctx, id)
	if err != nil {
		return err
	}
	provs, err := listJSON[Provider](ctx, s.KV, "prov/", "", 0)
	if err != nil {
		return err
	}
	models, err := listJSON[Model](ctx, s.KV, "model/"+id+"/", "", 0)
	if err != nil {
		return err
	}
	hooks, err := listJSON[storedWebhook](ctx, s.KV, "hook/"+id+"/", "", 0)
	if err != nil {
		return err
	}
	err = s.update(ctx, func(tx Tx) error {
		del := []string{"org/" + id}
		for _, m := range members {
			del = append(del, "mem/"+id+"/"+m.UserID, "umem/"+m.UserID+"/"+id)
		}
		for _, k := range keys {
			sk, err := getJSON[storedKey](tx.Get, "key/"+id+"/"+k.ID)
			if err == nil {
				del = append(del, "khash/"+sk.Hash)
			}
			del = append(del, "key/"+id+"/"+k.ID)
		}
		for _, p := range provs {
			if p.OrgID == id {
				del = append(del, "prov/"+p.ID)
			}
		}
		for _, m := range models {
			del = append(del, "model/"+id+"/"+m.ID)
		}
		for _, h := range hooks {
			del = append(del, hookKey(id, h.ID))
		}
		for _, k := range del {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range append(kbOrgPrefixes(id), alertPrefix(id)) {
		if err := s.DeletePrefix(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// ---- users ----

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *Store) CreateUser(ctx context.Context, u *User) error {
	u.Email = normEmail(u.Email)
	if !strings.Contains(u.Email, "@") {
		return fmt.Errorf("%w: valid email required", ErrInvalid)
	}
	u.ID, u.CreatedAt = NewID(), time.Now().UTC()
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get("idx/email/" + u.Email); err == nil {
			return ErrExists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := tx.Put("idx/email/"+u.Email, []byte(u.ID)); err != nil {
			return err
		}
		return putJSON(tx, "user/"+u.ID, storedUser{*u, u.PasswordHash})
	})
}

func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	su, err := getJSON[storedUser](s.get(ctx), "user/"+id)
	if err != nil {
		return nil, err
	}
	return su.user(), nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	id, err := s.KV.Get(ctx, "idx/email/"+normEmail(email))
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, string(id))
}

func (s *Store) UpdateUser(ctx context.Context, id string, fn func(*User) error) (*User, error) {
	var out *User
	err := s.update(ctx, func(tx Tx) error {
		su, err := getJSON[storedUser](tx.Get, "user/"+id)
		if err != nil {
			return err
		}
		u := su.user()
		if err := fn(u); err != nil {
			return err
		}
		u.ID, u.Email, out = id, su.Email, u // email changes are not supported (index stays consistent)
		return putJSON(tx, "user/"+id, storedUser{*u, u.PasswordHash})
	})
	return out, err
}

func (s *Store) ListUsers(ctx context.Context, after string, limit int) ([]User, error) {
	if after != "" {
		after = "user/" + after
	}
	return listJSON[User](ctx, s.KV, "user/", after, limit)
}

// ---- members ----

func putMember(tx Tx, m Member) error {
	if m.Role.Rank() == 0 {
		return fmt.Errorf("%w: unknown role %q", ErrInvalid, m.Role)
	}
	if err := putJSON(tx, "mem/"+m.OrgID+"/"+m.UserID, m); err != nil {
		return err
	}
	return tx.Put("umem/"+m.UserID+"/"+m.OrgID, []byte(m.Role))
}

func (s *Store) SetMember(ctx context.Context, m Member) error {
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + m.OrgID); err != nil {
			return err
		}
		if _, err := tx.Get("user/" + m.UserID); err != nil {
			return err
		}
		return putMember(tx, m)
	})
}

func (s *Store) RemoveMember(ctx context.Context, orgID, userID string) error {
	return s.update(ctx, func(tx Tx) error {
		if err := tx.Delete("mem/" + orgID + "/" + userID); err != nil {
			return err
		}
		return tx.Delete("umem/" + userID + "/" + orgID)
	})
}

func (s *Store) GetMember(ctx context.Context, orgID, userID string) (*Member, error) {
	return getJSON[Member](s.get(ctx), "mem/"+orgID+"/"+userID)
}

func (s *Store) ListMembers(ctx context.Context, orgID string) ([]Member, error) {
	return listJSON[Member](ctx, s.KV, "mem/"+orgID+"/", "", 0)
}

// UserOrgs returns orgID → role for a user.
func (s *Store) UserOrgs(ctx context.Context, userID string) (map[string]Role, error) {
	items, err := s.KV.Scan(ctx, "umem/"+userID+"/", "", 0)
	out := make(map[string]Role, len(items))
	for _, it := range items {
		out[strings.TrimPrefix(it.Key, "umem/"+userID+"/")] = Role(it.Value)
	}
	return out, err
}

// ---- API keys ----

func (s *Store) CreateKey(ctx context.Context, k *APIKey, hash string) error {
	if k.OrgID == "" || hash == "" {
		return fmt.Errorf("%w: org and hash required", ErrInvalid)
	}
	k.ID, k.CreatedAt, k.Hash = NewID(), time.Now().UTC(), hash
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + k.OrgID); err != nil {
			return err
		}
		if err := tx.Put("khash/"+hash, []byte(k.OrgID+"/"+k.ID)); err != nil {
			return err
		}
		return putJSON(tx, "key/"+k.OrgID+"/"+k.ID, storedKey{*k, hash})
	})
}

func (s *Store) KeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	ref, err := s.KV.Get(ctx, "khash/"+hash)
	if err != nil {
		return nil, err
	}
	sk, err := getJSON[storedKey](s.get(ctx), "key/"+string(ref))
	if err != nil {
		return nil, err
	}
	sk.APIKey.Hash = sk.Hash
	return &sk.APIKey, nil
}

func (s *Store) GetKey(ctx context.Context, orgID, id string) (*APIKey, error) {
	sk, err := getJSON[storedKey](s.get(ctx), "key/"+orgID+"/"+id)
	if err != nil {
		return nil, err
	}
	sk.APIKey.Hash = sk.Hash
	return &sk.APIKey, nil
}

func (s *Store) UpdateKey(ctx context.Context, orgID, id string, fn func(*APIKey) error) (*APIKey, error) {
	var out *APIKey
	err := s.update(ctx, func(tx Tx) error {
		sk, err := getJSON[storedKey](tx.Get, "key/"+orgID+"/"+id)
		if err != nil {
			return err
		}
		if err := fn(&sk.APIKey); err != nil {
			return err
		}
		sk.APIKey.ID, sk.APIKey.OrgID, out = id, orgID, &sk.APIKey
		return putJSON(tx, "key/"+orgID+"/"+id, sk)
	})
	return out, err
}

func (s *Store) ListKeys(ctx context.Context, orgID string) ([]APIKey, error) {
	return listJSON[APIKey](ctx, s.KV, "key/"+orgID+"/", "", 0)
}

// ---- providers ----

// PutProvider creates or updates a provider. Key values and config secrets (Bedrock keys, Vertex
// credentials) are sealed; a secret sent empty keeps the stored one for the same key id, except
// across a base_url, type or Azure endpoint change, where secrets must be re-entered.
func (s *Store) PutProvider(ctx context.Context, p *Provider) error {
	if p.Name == "" || p.Type == "" {
		return fmt.Errorf("%w: provider name and type required", ErrInvalid)
	}
	create := p.ID == ""
	if create {
		p.ID, p.CreatedAt = NewID(), time.Now().UTC()
	}
	return s.update(ctx, func(tx Tx) error {
		old, err := getJSON[Provider](tx.Get, "prov/"+p.ID)
		switch {
		case err != nil && !errors.Is(err, ErrNotFound):
			return err
		case create && old != nil:
			return ErrExists
		case !create && old == nil:
			return ErrNotFound // updates need an existing provider
		}
		rehost := false // stored secrets must not follow the provider to a new host
		if old != nil {
			if old.OrgID != p.OrgID {
				return ErrNotFound // never move a provider across tenants
			}
			p.CreatedAt = old.CreatedAt
			rehost = old.BaseURL != p.BaseURL || old.Type != p.Type
			if old.OrgID != "" { // a tenant's new proxy or proxy CA could read the keys in transit
				was, now := proxyOf(old), proxyOf(p)
				rehost = rehost || was.URL != now.URL || was.CACertPEM != now.CACertPEM
			}
		}
		for i := range p.Keys {
			k := &p.Keys[i]
			if k.ID == "" {
				k.ID = NewID()
			}
			var prev map[string][2]*string
			keyRehost := rehost
			k.Quarantined, k.StatusReason = false, "" // new keys start available
			if old != nil {
				for j := range old.Keys {
					if ok := &old.Keys[j]; ok.ID == k.ID {
						prev = ok.secrets()
						keyRehost = keyRehost || (ok.Azure != nil && k.Azure != nil && ok.Azure.Endpoint != k.Azure.Endpoint)
						// status belongs to the key API: a provider PUT keeps it
						k.Disabled, k.Quarantined, k.StatusReason = ok.Disabled, ok.Quarantined, ok.StatusReason
					}
				}
			}
			for name, f := range k.secrets() {
				plain, sealed := f[0], f[1]
				if *plain != "" {
					if *sealed, err = s.box.Seal(*plain); err != nil {
						return err
					}
					*plain = ""
					continue
				}
				*sealed = "" // never trust a sealed value from input
				if pf, ok := prev[name]; ok && *pf[1] != "" {
					if keyRehost {
						return fmt.Errorf("%w: re-enter the API keys and secrets when changing base_url, type or endpoint", ErrInvalid)
					}
					*sealed = *pf[1] // empty input keeps the stored secret
				}
			}
		}
		if err := s.sealProxy(p, old); err != nil {
			return err
		}
		return putJSON(tx, "prov/"+p.ID, p)
	})
}

func proxyOf(p *Provider) ProviderProxy {
	if p.Proxy == nil {
		return ProviderProxy{}
	}
	return *p.Proxy
}

// sealProxy seals the proxy password. An empty password keeps the stored one while the proxy URL
// and username stay the same; a new URL needs the password again, like a new base_url.
func (s *Store) sealProxy(p, old *Provider) error {
	x := p.Proxy
	if x == nil || x.URL == "" {
		p.Proxy = nil
		return nil
	}
	x.SealedPassword = "" // never trust a sealed value from input
	if x.Password != "" {
		var err error
		if x.SealedPassword, err = s.box.Seal(x.Password); err != nil {
			return err
		}
		x.Password = ""
		return nil
	}
	if old == nil || old.Proxy == nil || old.Proxy.SealedPassword == "" || x.Username == "" {
		return nil
	}
	if old.Proxy.URL != x.URL || old.Proxy.Username != x.Username {
		return fmt.Errorf("%w: re-enter the proxy password when changing the proxy URL or username", ErrInvalid)
	}
	x.SealedPassword = old.Proxy.SealedPassword
	return nil
}

// CreateKeylessProvider creates a provider under a caller-chosen id (the model catalog's
// deterministic "cat-<type>" ids); ErrExists when the id is taken. It stores no key secrets.
func (s *Store) CreateKeylessProvider(ctx context.Context, p *Provider) error {
	if p.ID == "" || p.Name == "" || p.Type == "" || len(p.Keys) > 0 {
		return fmt.Errorf("%w: provider id, name and type required, and no keys", ErrInvalid)
	}
	p.CreatedAt, p.Keys = time.Now().UTC(), []ProviderKey{}
	return s.update(ctx, func(tx Tx) error {
		if _, err := getJSON[Provider](tx.Get, "prov/"+p.ID); err == nil {
			return ErrExists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return putJSON(tx, "prov/"+p.ID, p)
	})
}

func (s *Store) GetProvider(ctx context.Context, id string) (*Provider, error) {
	return getJSON[Provider](s.get(ctx), "prov/"+id)
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	return s.update(ctx, func(tx Tx) error { return tx.Delete("prov/" + id) })
}

// ListProviders returns all providers. With decrypt, key values are filled in (gateway use only).
func (s *Store) ListProviders(ctx context.Context, decrypt bool) ([]Provider, error) {
	ps, err := listJSON[Provider](ctx, s.KV, "prov/", "", 0)
	if err != nil || !decrypt {
		return ps, err
	}
	for i := range ps {
		if err := s.openProvider(&ps[i]); err != nil {
			return nil, err
		}
	}
	return ps, nil
}

// openProvider fills in the plaintext of p's sealed secrets.
func (s *Store) openProvider(p *Provider) error {
	var err error
	if x := p.Proxy; x != nil && x.SealedPassword != "" {
		if x.Password, err = s.box.Open(x.SealedPassword); err != nil {
			return fmt.Errorf("provider %s proxy password: %w", p.ID, err)
		}
	}
	for j := range p.Keys {
		for name, f := range p.Keys[j].secrets() {
			if *f[1] == "" {
				continue
			}
			if *f[0], err = s.box.Open(*f[1]); err != nil {
				return fmt.Errorf("provider %s key %s %s: %w", p.ID, p.Keys[j].ID, name, err)
			}
		}
	}
	return nil
}

// GetProviderSecrets returns one provider with its secrets decrypted (gateway and key API use
// only: never encode it to a client).
func (s *Store) GetProviderSecrets(ctx context.Context, id string) (*Provider, error) {
	p, err := s.GetProvider(ctx, id)
	if err != nil {
		return nil, err
	}
	return p, s.openProvider(p)
}

// KeyHash is the SHA-256 (hex) of a provider key value, used to find duplicates.
func KeyHash(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// AddProviderKeys appends keys to provider id of owner org ("" = platform), sealing their
// values. A key whose value equals an existing key's value (or an earlier one in keys) is
// skipped and counted as a duplicate. It returns the added keys (without secrets).
func (s *Store) AddProviderKeys(ctx context.Context, org, id string, keys []ProviderKey) ([]ProviderKey, int, error) {
	var added []ProviderKey
	dups := 0
	err := s.update(ctx, func(tx Tx) error {
		added, dups = nil, 0
		p, err := getJSON[Provider](tx.Get, "prov/"+id)
		if err != nil {
			return err
		}
		if p.OrgID != org {
			return ErrNotFound
		}
		seen := map[string]bool{}
		for _, k := range p.Keys {
			if k.Sealed == "" {
				continue
			}
			v, err := s.box.Open(k.Sealed)
			if err != nil {
				return fmt.Errorf("provider %s key %s: %w", p.ID, k.ID, err)
			}
			seen[KeyHash(v)] = true
		}
		for _, k := range keys {
			h := KeyHash(k.Value)
			if seen[h] {
				dups++
				continue
			}
			seen[h] = true
			k.ID, k.Quarantined, k.StatusReason = NewID(), false, ""
			if k.Sealed, err = s.box.Seal(k.Value); err != nil {
				return err
			}
			k.Value = ""
			p.Keys = append(p.Keys, k)
			added = append(added, k.Redacted())
		}
		return putJSON(tx, "prov/"+p.ID, p)
	})
	return added, dups, err
}

// UpdateProviderKeys applies fn to the stored provider id of owner org ("" = platform) and
// saves it. fn edits key metadata (status, weight, name, models) or removes keys; sealed
// secrets are kept as stored and plaintext set by fn is dropped.
func (s *Store) UpdateProviderKeys(ctx context.Context, org, id string, fn func(*Provider) error) (*Provider, error) {
	var out *Provider
	err := s.update(ctx, func(tx Tx) error {
		p, err := getJSON[Provider](tx.Get, "prov/"+id)
		if err != nil {
			return err
		}
		if p.OrgID != org {
			return ErrNotFound
		}
		if err := fn(p); err != nil {
			return err
		}
		p.ID, p.OrgID = id, org
		for i := range p.Keys {
			for _, f := range p.Keys[i].secrets() {
				*f[0] = ""
			}
		}
		out = p
		return putJSON(tx, "prov/"+id, p)
	})
	return out, err
}

// ---- models ----

func modelKey(orgID, id string) string {
	if orgID == "" {
		orgID = "_"
	}
	return "model/" + orgID + "/" + id
}

// RoutingStrategies order a model's targets (see gateway/routing.go); "" means weighted.
var RoutingStrategies = []string{"", "weighted", "priority", "round-robin", "random", "p2c", "least-used", "latency", "cost", "cache-affinity"}

// ModelKinds are the model kinds; each API endpoint serves one kind (count tokens: chat).
var ModelKinds = []string{"chat", "embedding", "speech", "transcription", "image", "rerank", "ocr", "video"}

func (s *Store) PutModel(ctx context.Context, m *Model) error {
	if m.ID == "" || len(m.Targets) == 0 {
		return fmt.Errorf("%w: model id and at least one target required", ErrInvalid)
	}
	if m.Kind == "" {
		m.Kind = "chat"
	}
	if !slices.Contains(ModelKinds, m.Kind) {
		return fmt.Errorf("%w: kind must be one of %s", ErrInvalid, strings.Join(ModelKinds, ", "))
	}
	if !slices.Contains(RoutingStrategies, m.Strategy) {
		return fmt.Errorf("%w: strategy must be one of %s", ErrInvalid, strings.Join(RoutingStrategies[1:], ", "))
	}
	for _, t := range m.Targets {
		if t.InputPrice < 0 || t.OutputPrice < 0 {
			return fmt.Errorf("%w: prices must be >= 0", ErrInvalid)
		}
	}
	if m.InputPrice < 0 || m.OutputPrice < 0 || m.AudioMinutePrice < 0 || m.ImagePrice < 0 || m.RequestPrice < 0 || m.VideoSecondPrice < 0 {
		return fmt.Errorf("%w: prices must be >= 0", ErrInvalid)
	}
	return s.update(ctx, func(tx Tx) error {
		old, err := getJSON[Model](tx.Get, modelKey(m.OrgID, m.ID))
		switch {
		case err == nil:
			m.CreatedAt = old.CreatedAt
		case errors.Is(err, ErrNotFound):
			m.CreatedAt = time.Now().UTC()
		default:
			return err
		}
		for _, t := range m.Targets {
			p, err := getJSON[Provider](tx.Get, "prov/"+t.ProviderID)
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("%w: target provider %q not found", ErrInvalid, t.ProviderID)
			} else if err != nil {
				return err
			}
			if p.OrgID != m.OrgID { // public models use public providers; org models use the org's own
				return fmt.Errorf("%w: target provider %q not found", ErrInvalid, t.ProviderID)
			}
		}
		return putJSON(tx, modelKey(m.OrgID, m.ID), m)
	})
}

func (s *Store) DeleteModel(ctx context.Context, orgID, id string) error {
	return s.update(ctx, func(tx Tx) error { return tx.Delete(modelKey(orgID, id)) })
}

func (s *Store) GetModel(ctx context.Context, orgID, id string) (*Model, error) {
	return getJSON[Model](s.get(ctx), modelKey(orgID, id))
}

// ListModels returns models of one owner; orgID "" = public models, "*" = all.
func (s *Store) ListModels(ctx context.Context, orgID string) ([]Model, error) {
	prefix := "model/"
	if orgID != "*" {
		prefix = modelKey(orgID, "")
	}
	return listJSON[Model](ctx, s.KV, prefix, "", 0)
}

// ---- MCP servers ----

func (s *Store) PutMCP(ctx context.Context, m *MCPServer) error {
	if m.Name == "" || m.URL == "" || (m.Type != "http" && m.Type != "sse") {
		return fmt.Errorf("%w: name, url and type (http|sse) required", ErrInvalid)
	}
	if m.ID == "" {
		m.ID = NewID()
	}
	return s.update(ctx, func(tx Tx) error { return putJSON(tx, "mcp/"+m.ID, m) })
}

func (s *Store) DeleteMCP(ctx context.Context, id string) error {
	return s.update(ctx, func(tx Tx) error { return tx.Delete("mcp/" + id) })
}

func (s *Store) ListMCP(ctx context.Context) ([]MCPServer, error) {
	return listJSON[MCPServer](ctx, s.KV, "mcp/", "", 0)
}

// ---- audit ----

// invTS makes newer entries sort first in ascending scans.
func invTS(t time.Time) string { return fmt.Sprintf("%019d", int64(1<<62)-t.UnixNano()) }

func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.OrgID == "" {
		e.OrgID = "_"
	}
	e.ID, e.At = NewID(), time.Now().UTC()
	return s.KV.Update(ctx, func(tx Tx) error {
		return putJSON(tx, "audit/"+e.OrgID+"/"+invTS(e.At)+e.ID, e)
	})
}

func (s *Store) ListAudit(ctx context.Context, orgID string, limit int) ([]AuditEntry, error) {
	if orgID == "" {
		orgID = "_"
	}
	return listJSON[AuditEntry](ctx, s.KV, "audit/"+orgID+"/", "", limit)
}

// ---- request logs (day-partitioned for cheap retention) ----

// Day is the UTC day partition used by logs and usage rollups.
func Day(t time.Time) string { return t.UTC().Format("20060102") }

func (s *Store) PutLogs(ctx context.Context, logs []RequestLog) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		for i := range logs {
			l := &logs[i]
			if err := putJSON(tx, "log/"+Day(l.At)+"/"+l.OrgID+"/"+invTS(l.At)+l.ID, l); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListLogs returns the newest logs of an org, looking back `days` days.
func (s *Store) ListLogs(ctx context.Context, orgID string, days, limit int) ([]RequestLog, error) {
	var out []RequestLog
	now := time.Now()
	for d := 0; d < days && len(out) < limit; d++ {
		ls, err := listJSON[RequestLog](ctx, s.KV, "log/"+Day(now.AddDate(0, 0, -d))+"/"+orgID+"/", "", limit-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, ls...)
	}
	return out, nil
}

// PruneLogs deletes request logs older than keepDays. Usage rollups (agg/) are kept.
func (s *Store) PruneLogs(ctx context.Context, keepDays int) error {
	cutoff := Day(time.Now().AddDate(0, 0, -keepDays))
	return s.KV.DeleteRange(ctx, "log/", "log/"+cutoff)
}

// PruneHealth deletes hourly provider health and per-key counters (keystat/) older than keepDays.
// Counters of deleted keys and providers are left to this retention.
func (s *Store) PruneHealth(ctx context.Context, keepDays int) error {
	cutoff := time.Now().AddDate(0, 0, -keepDays).UTC().Format("2006010215")
	for _, p := range []string{"health/", "keystat/"} {
		if err := s.KV.DeleteRange(ctx, p, p+cutoff); err != nil {
			return err
		}
	}
	return nil
}
