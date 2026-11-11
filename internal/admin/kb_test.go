package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/kb"
	"github.com/agent-gateway/agent-gateway/internal/store"
)

// setupKB adds an embedding model (one fixed vector per input: ranking falls to BM25) and the
// KB engine to the admin fixture.
func setupKB(t *testing.T) *fixture {
	f := setup(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input any }
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := 1
		if l, ok := req.Input.([]any); ok {
			n = len(l)
		}
		var data []string
		for i := range n {
			data = append(data, fmt.Sprintf(`{"object":"embedding","index":%d,"embedding":[0.6,0.8]}`, i))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"list","model":"e","data":[%s],"usage":{"prompt_tokens":3,"total_tokens":3}}`, strings.Join(data, ","))
	}))
	t.Cleanup(up.Close)
	p := &store.Provider{Name: "up", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.s.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.Model{
		{ID: "t/emb", Kind: "embedding", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "e"}}},
		{ID: "t/chat", Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "c"}}},
	} {
		if err := f.s.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	f.g.KB = kb.New(f.s, f.g, kb.Config{})
	f.g.KB.Start(ctx)
	return f
}

func TestKBAdmin(t *testing.T) {
	f := setupKB(t)
	base := "/api/orgs/" + f.orgA.ID + "/kbs"

	// roles: admins create, members cannot; chat models are refused
	expect(t, f.do(t, "member", "POST", base, `{"name":"x","embedding_model":"t/emb"}`), 403, "member create")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"x","embedding_model":"t/chat"}`), 400, "chat model")
	expect(t, f.do(t, "admin", "POST", base, `{"name":"x","embedding_model":"t/emb","top_k":99}`), 400, "bad top_k")
	r := f.do(t, "admin", "POST", base, `{"name":"Handbook","embedding_model":"t/emb"}`)
	expect(t, r, 201, "create")
	var k store.KB
	r.json(t, &k)
	if k.ID == "" || k.ChunkChars != kb.DefaultChunkChars || k.TopK != kb.DefaultTopK {
		t.Fatalf("created: %+v", k)
	}
	kbPath := base + "/" + k.ID

	// viewers read: bare arrays
	r = f.do(t, "viewer", "GET", base, "")
	expect(t, r, 200, "list")
	var ks []store.KB
	r.json(t, &ks)
	if len(ks) != 1 || ks[0].ID != k.ID {
		t.Fatalf("list: %s", r.body)
	}
	expect(t, f.do(t, "viewer", "GET", kbPath, ""), 200, "get")
	expect(t, f.do(t, "viewer", "POST", kbPath+"/search", `{"query":"refunds"}`), 403, "viewer search")

	// other orgs cannot see it, not even through their own org path
	expect(t, f.do(t, "outsider", "GET", kbPath, ""), 404, "outsider")
	expect(t, f.do(t, "outsider", "GET", "/api/orgs/"+f.orgB.ID+"/kbs/"+k.ID, ""), 404, "cross-org id")

	// documents: JSON text, multipart with attributes, 202 + queued
	expect(t, f.do(t, "member", "POST", kbPath+"/docs", `{"filename":"a.md","text":"hello"}`), 403, "member upload")
	r = f.do(t, "admin", "POST", kbPath+"/docs", `{"filename":"billing.md","text":"# Billing\n\nRefunds are issued within 14 days."}`)
	expect(t, r, 202, "text doc")
	var d store.KBDoc
	r.json(t, &d)
	if d.Status != store.KBQueued || d.Source != "text" {
		t.Fatalf("doc: %s", r.body)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "office.txt")
	fmt.Fprint(fw, "The office opens at nine every weekday morning.")
	_ = mw.WriteField("attributes", `{"team":"ops"}`)
	mw.Close()
	req, _ := http.NewRequest("POST", f.srv.URL+kbPath+"/docs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-GW-CSRF", "1")
	req.AddCookie(f.cookie("admin"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 202 || !strings.Contains(string(raw), `"team":"ops"`) {
		t.Fatalf("multipart: %d %s", res.StatusCode, raw)
	}
	expect(t, f.do(t, "admin", "POST", kbPath+"/docs", `{"filename":"x.pdf","text":"%PDF"}`), 415, "pdf")
	expect(t, f.do(t, "admin", "POST", kbPath+"/docs", `{"url":"http://127.0.0.1/x"}`), 415, "url ingestion disabled without a dialer")

	deadline := time.Now().Add(10 * time.Second)
	for {
		var docs []store.KBDoc
		r = f.do(t, "viewer", "GET", kbPath+"/docs", "")
		r.json(t, &docs)
		if len(docs) == 2 && docs[0].Status == store.KBReady && docs[1].Status == store.KBReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("docs: %s", r.body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	expect(t, f.do(t, "viewer", "GET", kbPath+"/docs/"+d.ID, ""), 200, "get doc")

	// search: member, snake_case hits; attribute filters
	r = f.do(t, "member", "POST", kbPath+"/search", `{"query":"when are refunds issued","top_k":3}`)
	expect(t, r, 200, "search")
	var hits []map[string]any
	r.json(t, &hits)
	if len(hits) == 0 || hits[0]["doc_id"] != d.ID || hits[0]["filename"] != "billing.md" || hits[0]["section"] != "Billing" || hits[0]["seq"] != 0.0 {
		t.Fatalf("hits: %s", r.body)
	}
	r = f.do(t, "member", "POST", kbPath+"/search", `{"query":"when are refunds issued, office","filters":{"team":"ops"}}`)
	r.json(t, &hits)
	if len(hits) != 1 || hits[0]["filename"] != "office.txt" {
		t.Fatalf("filtered: %s", r.body)
	}

	// key kb_ids: admins only, validated, max 3
	keyID, _ := f.createKey(t, "admin", f.orgA.ID)
	expect(t, f.do(t, "member", "POST", "/api/orgs/"+f.orgA.ID+"/keys", `{"kb_ids":["`+k.ID+`"]}`), 403, "member kb_ids")
	expect(t, f.do(t, "admin", "PATCH", "/api/orgs/"+f.orgA.ID+"/keys/"+keyID, `{"kb_ids":["vs_000000000000000000000000"]}`), 400, "unknown kb")
	expect(t, f.do(t, "admin", "PATCH", "/api/orgs/"+f.orgA.ID+"/keys/"+keyID, `{"kb_ids":["a","b","c","d"]}`), 400, "4 kbs")
	r = f.do(t, "admin", "PATCH", "/api/orgs/"+f.orgA.ID+"/keys/"+keyID, `{"kb_ids":["`+k.ID+`","`+k.ID+`"]}`)
	expect(t, r, 200, "kb_ids")
	var key store.APIKey
	r.json(t, &key)
	if len(key.KBIDs) != 1 || key.KBIDs[0] != k.ID {
		t.Fatalf("key: %s", r.body)
	}
	expect(t, f.do(t, "admin", "POST", "/api/orgs/"+f.orgA.ID+"/keys", `{"name":"g","kb_ids":["`+k.ID+`"]}`), 201, "create key with kb_ids")

	// update re-embeds; reindex; deletes
	r = f.do(t, "admin", "PATCH", kbPath, `{"top_k":7}`)
	expect(t, r, 200, "patch")
	if r.json(t, &k); k.TopK != 7 {
		t.Fatalf("patch: %s", r.body)
	}
	expect(t, f.do(t, "member", "POST", kbPath+"/reindex", ""), 403, "member reindex")
	expect(t, f.do(t, "admin", "POST", kbPath+"/reindex", ""), 204, "reindex")
	expect(t, f.do(t, "admin", "DELETE", kbPath+"/docs/"+d.ID, ""), 204, "delete doc")
	expect(t, f.do(t, "viewer", "GET", kbPath+"/docs/"+d.ID, ""), 404, "deleted doc")
	expect(t, f.do(t, "admin", "DELETE", kbPath, ""), 204, "delete kb")
	expect(t, f.do(t, "viewer", "GET", kbPath, ""), 404, "deleted kb")

	audit, _ := f.s.ListAudit(context.Background(), f.orgA.ID, 100)
	seen := map[string]bool{}
	for _, e := range audit {
		seen[e.Action] = true
	}
	for _, a := range []string{"kb.create", "kb.update", "kb.reindex", "kb.doc.add", "kb.doc.delete", "kb.delete"} {
		if !seen[a] {
			t.Errorf("audit lacks %s", a)
		}
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	f := setup(t, Config{})
	mux := http.NewServeMux()
	f.a.Register(mux)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "<html>app</html>") }))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, c := range []struct {
		path string
		code int
		body string
	}{
		{"/api/nope", 404, `{"error":{"message":"not found"}}`},
		{"/api/orgs/x/kbs/y/unknown", 404, `{"error":{"message":"not found"}}`},
		{"/app/kbs", 200, "<html>app</html>"},
		{"/models/a/b", 200, "<html>app</html>"},
	} {
		res, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != c.code || strings.TrimSpace(string(raw)) != c.body {
			t.Fatalf("%s: %d %s", c.path, res.StatusCode, raw)
		}
	}
}
