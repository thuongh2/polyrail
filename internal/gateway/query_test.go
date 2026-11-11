package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

func TestModelsQuery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := &store.Org{Name: "other"}
	if err := f.g.S.CreateOrg(ctx, other, ""); err != nil {
		t.Fatal(err)
	}
	priv := &store.Provider{OrgID: other.ID, Name: "mine", Type: "openai", BaseURL: "https://example.com", Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.g.S.PutProvider(ctx, priv); err != nil {
		t.Fatal(err)
	}
	ok := f.g.Snapshot().Models["_|test/ok"].Targets
	for _, m := range []*store.Model{
		{ID: "vend/big", Name: "Big Vision", Enabled: true, ContextLength: 200_000, MaxOutput: 16_000, Modalities: []string{"text", "image"},
			InputPrice: 3_000_000, OutputPrice: 15_000_000, Targets: ok},
		{ID: "vend/small", Name: "Small", Description: "cheap and FAST", Enabled: true, ContextLength: 32_000, MaxOutput: 4_000, Modalities: []string{"text"},
			InputPrice: 100_000, OutputPrice: 400_000, Targets: ok},
		{ID: "vend/secret", OrgID: other.ID, Name: "Secret", Enabled: true, ContextLength: 1_000_000, Targets: []store.Target{{ProviderID: priv.ID, Model: "x"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/models/query", f.g.ModelsQuery)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	ids := func(body string) (int, []string) {
		t.Helper()
		r := f.post(t, "/v1/models/query", f.key, body)
		m := readJSON(t, r)
		var out []string
		if d, ok := m["data"].([]any); ok {
			for _, x := range d {
				out = append(out, x.(map[string]any)["id"].(string))
			}
		}
		return r.StatusCode, out
	}
	cases := []struct{ body, want string }{
		{`{}`, "test/chat,test/emb,test/ok,vend/big,vend/small"},
		{`{"q":"fast"}`, "vend/small"},
		{`{"q":"VISION"}`, "vend/big"},
		{`{"kind":"embedding"}`, "test/emb"},
		{`{"modalities":["image","text"]}`, "vend/big"},
		{`{"min_context":100000}`, "vend/big"},
		{`{"min_output":5000}`, "vend/big"},
		{`{"max_input_price":0.5,"owned_by":"vend"}`, "vend/small"},
		{`{"max_output_price":1,"owned_by":"vend"}`, "vend/small"},
		{`{"owned_by":"vend","sort":"context"}`, "vend/big,vend/small"},
		{`{"owned_by":"vend","sort":"price"}`, "vend/small,vend/big"},
		{`{"exclude":["test/chat","test/emb"],"limit":2}`, "test/ok,vend/big"},
		{`{"q":"secret"}`, ""},
	}
	for _, c := range cases {
		st, got := ids(c.body)
		if st != 200 || strings.Join(got, ",") != c.want {
			t.Errorf("%s: %d %v, want %s", c.body, st, got, c.want)
		}
	}
	for _, bad := range []string{`{"bogus":1}`, `{"sort":"name"}`, `{"limit":-1}`, `not json`} {
		if st, _ := ids(bad); st != 400 {
			t.Errorf("%s: status %d, want 400", bad, st)
		}
	}
	r := f.post(t, "/v1/models/query", "sk-gw-nope", `{}`)
	if r.Body.Close(); r.StatusCode != 401 {
		t.Fatalf("auth: %d", r.StatusCode)
	}
}
