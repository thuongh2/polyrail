package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// When every key fails with an account error, Bifrost answers a bare "all configured keys
// returned permanent per-key errors"; the gateway adds the provider's status and reason.
func TestExplainKeysOnCredentialsExhausted(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	t.Cleanup(up.Close)
	f := setup(t)
	ctx := context.Background()
	p := &store.Provider{Name: "up", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k1"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	m := &store.Model{ID: "x/m", Name: "m", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "m"}}}
	if err := f.g.S.PutModel(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"x/m","messages":[{"role":"user","content":"hi"}]}`)
	out := readJSON(t, resp)
	msg, _ := out["error"].(map[string]any)["message"].(string)
	if resp.StatusCode != 502 || !strings.Contains(msg, "HTTP 401") {
		t.Fatalf("%d: %v", resp.StatusCode, out)
	}
}
