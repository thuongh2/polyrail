package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

// modelsUpstream answers GET /v1/models like an OpenAI-compatible API and counts the keys it saw.
func modelsUpstream(t *testing.T, auth *atomic.Value) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		auth.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-up","object":"model","created":1,"owned_by":"x","context_window":8192},{"id":"emb-up","object":"model","created":1,"owned_by":"x"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverAndImportModels(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true}) // the upstream is on 127.0.0.1
	var auth atomic.Value
	up := modelsUpstream(t, &auth)
	ctx := context.Background()

	for _, c := range []struct{ as, base string }{{"op", "/api/admin/providers"}, {"admin", "/api/orgs/" + f.orgA.ID + "/providers"}} {
		var p store.Provider
		r := f.do(t, c.as, "POST", c.base, `{"name":"up-`+c.as+`","type":"openai","enabled":true,"base_url":"`+up.URL+`","keys":[{"value":"sk-up-`+c.as+`"}]}`)
		expect(t, r, 200, "create provider")
		r.json(t, &p)

		var disc struct {
			Models []discoveredModel `json:"models"`
		}
		r = f.do(t, c.as, "POST", c.base+"/"+p.ID+"/discover", "")
		expect(t, r, 200, "discover")
		r.json(t, &disc)
		i := slices.IndexFunc(disc.Models, func(d discoveredModel) bool { return d.Upstream == "gpt-up" })
		if len(disc.Models) != 2 || i < 0 || disc.Models[i].ContextLength != 8192 || auth.Load() != "Bearer sk-up-"+c.as {
			t.Fatalf("%s discover: %s (auth %v)", c.as, r.body, auth.Load())
		}

		// a bad entry rejects the whole import; then a good import creates routed, priced models
		bad := `{"models":[{"upstream":"gpt-up","id":"up/chat"},{"upstream":"emb-up","id":"bad id"}]}`
		expect(t, f.do(t, c.as, "POST", c.base+"/"+p.ID+"/import", bad), 400, "invalid id")
		bad = `{"models":[{"upstream":"gpt-up","id":"up/chat"},{"upstream":"emb-up","id":"up/emb","kind":"nonsense"}]}`
		expect(t, f.do(t, c.as, "POST", c.base+"/"+p.ID+"/import", bad), 400, "invalid kind")
		good := `{"models":[{"upstream":"gpt-up","id":"up/chat","name":"Up Chat","context_length":8192,"input_price_micros":1000000,"output_price_micros":2000000},{"upstream":"emb-up","id":"up/emb","kind":"embedding"}]}`
		r = f.do(t, c.as, "POST", c.base+"/"+p.ID+"/import", good)
		expect(t, r, 200, "import")
		org := ""
		if c.as == "admin" {
			org = f.orgA.ID
		}
		m, err := f.s.GetModel(ctx, org, "up/chat")
		if err != nil || m.Name != "Up Chat" || m.InputPrice != 1_000_000 || !m.Enabled || m.Targets[0].ProviderID != p.ID || m.Targets[0].Model != "gpt-up" {
			t.Fatalf("%s imported model: %+v %v", c.as, m, err)
		}
		if e, err := f.s.GetModel(ctx, org, "up/emb"); err != nil || e.Kind != "embedding" || e.Name != "up/emb" {
			t.Fatalf("%s imported embedding: %+v %v", c.as, e, err)
		}
		expect(t, f.do(t, c.as, "POST", c.base+"/"+p.ID+"/import", good), 409, "re-import")
		audit := f.do(t, c.as, "GET", strings.Replace(c.base, "/providers", "/audit", 1), "")
		if c.as == "admin" && !strings.Contains(audit.body, "model.import") {
			t.Fatalf("import not audited: %s", audit.body)
		}
	}

	// org scope: admin role, own providers only
	var pub []store.Provider
	f.do(t, "op", "GET", "/api/admin/providers", "").json(t, &pub)
	A := "/api/orgs/" + f.orgA.ID + "/providers/"
	expect(t, f.do(t, "admin", "POST", A+pub[0].ID+"/discover", ""), 404, "org discovering a platform provider")
	expect(t, f.do(t, "member", "POST", A+pub[0].ID+"/discover", ""), 403, "member")
	expect(t, f.do(t, "outsider", "POST", "/api/admin/providers/"+pub[0].ID+"/discover", ""), 403, "non-operator")
}

func TestNewProviderTypes(t *testing.T) {
	f := setup(t, Config{})
	P := "/api/admin/providers"
	for _, typ := range []string{"deepseek", "xai", "fireworks", "huggingface", "replicate", "minimax", "opencode-zen", "typesafe"} {
		expect(t, f.do(t, "op", "POST", P, `{"name":"`+typ+`","type":"`+typ+`","keys":[{"value":"k"}]}`), 200, typ)
	}
	expect(t, f.do(t, "op", "POST", P, `{"name":"v","type":"vllm"}`), 400, "vllm without base_url")
	expect(t, f.do(t, "op", "POST", P, `{"name":"v","type":"vllm","base_url":"http://10.0.0.6:8000"}`), 200, "vllm")
	expect(t, f.do(t, "op", "POST", P, `{"name":"d","type":"databricks","keys":[{"value":"k"}]}`), 400, "databricks without workspace")
	expect(t, f.do(t, "op", "POST", P, `{"name":"d","type":"databricks","base_url":"https://dbc-1.cloud.databricks.com","keys":[{"value":"k"}]}`), 200, "databricks")
	for _, typ := range []string{"github-copilot", "sagemaker", "cloudflare", "bedrock_mantle"} {
		expect(t, f.do(t, "op", "POST", P, `{"name":"x","type":"`+typ+`","keys":[{"value":"k"}]}`), 400, typ)
	}
	A := "/api/orgs/" + f.orgA.ID + "/providers"
	expect(t, f.do(t, "admin", "POST", A, `{"name":"hf","type":"huggingface","keys":[{"value":"k"}]}`), 200, "tenant huggingface (multi-instance)")
	expect(t, f.do(t, "admin", "POST", A, `{"name":"ds","type":"deepseek","keys":[{"value":"k"}]}`), 400, "tenant deepseek (platform-only)")
	var cfg struct {
		ProviderTypes map[string][]string `json:"provider_types"`
	}
	f.do(t, "", "GET", "/api/auth/config", "").json(t, &cfg)
	if !slices.Contains(cfg.ProviderTypes["platform"], "xai") || !slices.Contains(cfg.ProviderTypes["org"], "replicate") || slices.Contains(cfg.ProviderTypes["org"], "vllm") {
		t.Fatalf("provider types: %+v", cfg.ProviderTypes)
	}
}

func TestPerTokenMicros(t *testing.T) {
	s := func(v string) *string { return &v }
	if p := perTokenMicros(s("0.000003")); p == nil || *p != 3_000_000 {
		t.Fatalf("$3/1M: %v", p)
	}
	if perTokenMicros(s("-1")) != nil || perTokenMicros(s("x")) != nil || perTokenMicros(nil) != nil {
		t.Fatal("variable or bad prices must be omitted")
	}
}
