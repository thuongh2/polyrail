package gateway

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

func TestAccountMapsCloudKeyConfig(t *testing.T) {
	az := store.Provider{ID: "az", Type: "azure", Enabled: true, Keys: []store.ProviderKey{{ID: "k1", Value: "az-key",
		Azure: &store.AzureConfig{Endpoint: "https://r.openai.azure.com/", APIVersion: "2024-10-21", Deployments: map[string]string{"gpt-4o": "prod-4o"}}}}}
	br := store.Provider{ID: "br", Type: "bedrock", Enabled: true, Keys: []store.ProviderKey{
		{ID: "iam", Bedrock: &store.BedrockConfig{Region: "us-west-2"}},
		{ID: "sig", Bedrock: &store.BedrockConfig{AccessKey: "AK", SecretKey: "SK", SessionToken: "ST", Region: "eu-west-1", ARN: "arn:aws:x"}}}}
	tbr := store.Provider{ID: "tbr", OrgID: "org1", Type: "bedrock", Enabled: true, Keys: []store.ProviderKey{
		{ID: "iam", Bedrock: &store.BedrockConfig{Region: "us-west-2"}},
		{ID: "api", Value: "bedrock-api-key", Bedrock: &store.BedrockConfig{Region: "us-west-2"}}}}
	vx := store.Provider{ID: "vx", Type: "vertex", Enabled: true, Keys: []store.ProviderKey{
		{ID: "iam", Vertex: &store.VertexConfig{ProjectID: "p", Region: "us-central1"}}}}
	snap := &Snapshot{Providers: map[string]store.Provider{}, byBifrost: map[schemas.ModelProvider]string{}}
	for _, p := range []store.Provider{az, br, tbr, vx} {
		snap.Providers[p.ID] = p
		snap.byBifrost[BifrostKey(p)] = p.ID
	}
	a := &account{snap: func() *Snapshot { return snap }}
	ctx := context.Background()
	keys := func(p store.Provider) []schemas.Key {
		ks, err := a.GetKeysForProvider(ctx, BifrostKey(p))
		if err != nil {
			t.Fatal(err)
		}
		return ks
	}

	k := keys(az)[0]
	if a := k.Aliases["gpt-4o"]; k.Value.GetValue() != "az-key" || k.AzureKeyConfig.Endpoint.GetValue() != "https://r.openai.azure.com" || a.ModelID != "prod-4o" || *a.APIVersion != "2024-10-21" {
		t.Fatalf("azure key: %+v %+v", k, k.AzureKeyConfig)
	}
	if BifrostKey(az) != schemas.Azure || BifrostKey(vx) != schemas.Vertex || BifrostKey(br) != "p-br" {
		t.Fatalf("bifrost keys: %s %s %s", BifrostKey(az), BifrostKey(vx), BifrostKey(br))
	}
	bk := keys(br)
	if len(bk) != 2 || bk[0].BedrockKeyConfig.AccessKey.GetValue() != "" || bk[0].BedrockKeyConfig.Region.GetValue() != "us-west-2" || bk[0].BedrockKeyConfig.SessionToken != nil {
		t.Fatalf("bedrock IAM key: %+v", bk[0].BedrockKeyConfig)
	}
	if c := bk[1].BedrockKeyConfig; c.AccessKey.GetValue() != "AK" || c.SecretKey.GetValue() != "SK" || c.SessionToken.GetValue() != "ST" || c.ARN.GetValue() != "arn:aws:x" {
		t.Fatalf("bedrock signed key: %+v", c)
	}
	if tk := keys(tbr); len(tk) != 1 || tk[0].ID != "api" {
		t.Fatalf("tenant bedrock must not use the host IAM identity: %+v", tk)
	}
	if v := keys(vx)[0].VertexKeyConfig; v.ProjectID.GetValue() != "p" || v.Region.GetValue() != "us-central1" || v.AuthCredentials.GetValue() != "" {
		t.Fatalf("vertex IAM key: %+v", v)
	}
	if !bk[0].Models.IsAllowed("any-model") {
		t.Fatalf("a key without a model list must serve every model: %v", bk[0].Models)
	}
	aws := store.ProviderKey{ID: "m", Bedrock: &store.BedrockConfig{Region: "us-west-2", AccessKey: "AK", SecretKey: "SK"}}
	if m := bifrostKey("bedrock_mantle", aws, 1); m.BedrockMantleKeyConfig == nil || m.BedrockKeyConfig != nil || m.BedrockMantleKeyConfig.Region.GetValue() != "us-west-2" {
		t.Fatalf("bedrock_mantle key: %+v", m)
	}
	if m := bifrostKey("sagemaker", aws, 1); m.SageMakerKeyConfig == nil || m.BedrockKeyConfig != nil || m.SageMakerKeyConfig.SecretKey.GetValue() != "SK" {
		t.Fatalf("sagemaker key: %+v", m)
	}
	cf := bifrostKey("cloudflare", store.ProviderKey{ID: "c", Value: "tok", Cloudflare: &store.CloudflareConfig{AccountID: "abc"}}, 1)
	if cf.CloudflareKeyConfig == nil || cf.CloudflareKeyConfig.AccountID.GetValue() != "abc" || cf.Value.GetValue() != "tok" {
		t.Fatalf("cloudflare key: %+v", cf)
	}
	t.Setenv("GW_TEST_SECRET", "host-secret")
	ek := bifrostKey("openai", store.ProviderKey{ID: "e", Value: "env.GW_TEST_SECRET"}, 1)
	if ek.Value.GetValue() != "env.GW_TEST_SECRET" {
		t.Fatalf("stored key values must stay literal, got %q", ek.Value.GetValue())
	}
	ol := store.Provider{ID: "ol", Type: "ollama", BaseURL: "http://10.0.0.5:11434", Enabled: true}
	snap.Providers[ol.ID], snap.byBifrost[BifrostKey(ol)] = ol, ol.ID
	if ok := keys(ol); len(ok) != 1 || ok[0].OllamaKeyConfig.URL.GetValue() != ol.BaseURL {
		t.Fatalf("keyless ollama needs one implicit key with its URL: %+v", ok)
	}
	vl := store.Provider{ID: "vl", Type: "vllm", BaseURL: "http://10.0.0.6:8000", Enabled: true, Keys: []store.ProviderKey{{ID: "v", Value: "tok"}}}
	snap.Providers[vl.ID], snap.byBifrost[BifrostKey(vl)] = vl, vl.ID
	if vk := keys(vl); len(vk) != 1 || vk[0].VLLMKeyConfig.URL.GetValue() != vl.BaseURL || vk[0].Value.GetValue() != "tok" {
		t.Fatalf("vllm keys carry the server URL: %+v", vk)
	}
	cfg, err := a.GetConfigForProvider(BifrostKey(br))
	if err != nil || cfg.CustomProviderConfig == nil || cfg.CustomProviderConfig.BaseProviderType != schemas.Bedrock {
		t.Fatalf("bedrock is a custom-base provider: %+v %v", cfg, err)
	}
	if cfg, _ := a.GetConfigForProvider(BifrostKey(vx)); cfg.CustomProviderConfig != nil {
		t.Fatalf("vertex is a standard provider: %+v", cfg.CustomProviderConfig)
	}
}

func TestSnapshotHidesModelsWithoutLiveProvider(t *testing.T) {
	on := store.Provider{ID: "on", Type: "openai", Enabled: true}
	snap := &Snapshot{Providers: map[string]store.Provider{"on": on}, Models: map[string]store.Model{
		"_|a/live":   {ID: "a/live", Enabled: true, Targets: []store.Target{{ProviderID: "off"}, {ProviderID: "on"}}},
		"_|a/dark":   {ID: "a/dark", Enabled: true, Targets: []store.Target{{ProviderID: "off"}}},
		"o1|a/dark":  {ID: "a/dark", OrgID: "o1", Enabled: true, Targets: []store.Target{{ProviderID: "on"}}},
		"o1|a/ghost": {ID: "a/ghost", OrgID: "o1", Enabled: true, Targets: []store.Target{{ProviderID: "gone"}}},
	}}
	var ids []string
	for _, m := range snap.Visible("", "") {
		ids = append(ids, m.ID)
	}
	if len(ids) != 1 || ids[0] != "a/live" {
		t.Fatalf("public visible = %v, want [a/live]", ids)
	}
	if _, ok := snap.Model("", "", "a/dark"); ok {
		t.Fatal("a model whose providers are all disabled must not resolve")
	}
	if m, ok := snap.Model("o1", "", "a/dark"); !ok || m.OrgID != "o1" {
		t.Fatalf("org model with a live provider must resolve: %+v %v", m, ok)
	}
	if _, ok := snap.Model("o1", "", "a/ghost"); ok {
		t.Fatal("org model without a live provider must not resolve")
	}
	if n := len(snap.Visible("o1", "")); n != 2 {
		t.Fatalf("org o1 sees %d models, want 2 (a/dark own, a/live public)", n)
	}
}
