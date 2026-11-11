package gateway

import (
	"context"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/maximhq/bifrost/core/schemas"
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
		ks, err := a.GetKeysForProvider(&ctx, BifrostKey(p))
		if err != nil {
			t.Fatal(err)
		}
		return ks
	}

	k := keys(az)[0]
	if k.Value != "az-key" || k.AzureKeyConfig.Endpoint != "https://r.openai.azure.com" || *k.AzureKeyConfig.APIVersion != "2024-10-21" || k.AzureKeyConfig.Deployments["gpt-4o"] != "prod-4o" {
		t.Fatalf("azure key: %+v %+v", k, k.AzureKeyConfig)
	}
	if BifrostKey(az) != schemas.Azure || BifrostKey(vx) != schemas.Vertex || BifrostKey(br) != "p-br" {
		t.Fatalf("bifrost keys: %s %s %s", BifrostKey(az), BifrostKey(vx), BifrostKey(br))
	}
	bk := keys(br)
	if len(bk) != 2 || bk[0].BedrockKeyConfig.AccessKey != "" || *bk[0].BedrockKeyConfig.Region != "us-west-2" || bk[0].BedrockKeyConfig.SessionToken != nil {
		t.Fatalf("bedrock IAM key: %+v", bk[0].BedrockKeyConfig)
	}
	if c := bk[1].BedrockKeyConfig; c.AccessKey != "AK" || c.SecretKey != "SK" || *c.SessionToken != "ST" || *c.ARN != "arn:aws:x" {
		t.Fatalf("bedrock signed key: %+v", c)
	}
	if tk := keys(tbr); len(tk) != 1 || tk[0].ID != "api" {
		t.Fatalf("tenant bedrock must not use the host IAM identity: %+v", tk)
	}
	if v := keys(vx)[0].VertexKeyConfig; v.ProjectID != "p" || v.Region != "us-central1" || v.AuthCredentials != "" {
		t.Fatalf("vertex IAM key: %+v", v)
	}
	cfg, err := a.GetConfigForProvider(BifrostKey(br))
	if err != nil || cfg.CustomProviderConfig == nil || cfg.CustomProviderConfig.BaseProviderType != schemas.Bedrock {
		t.Fatalf("bedrock is a custom-base provider: %+v %v", cfg, err)
	}
	if cfg, _ := a.GetConfigForProvider(BifrostKey(vx)); cfg.CustomProviderConfig != nil {
		t.Fatalf("vertex is a standard provider: %+v", cfg.CustomProviderConfig)
	}
}
