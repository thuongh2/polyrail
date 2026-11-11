package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var reWord = regexp.MustCompile(`[a-z0-9]+`)

// kbUpstream embeds like tools/mockprovider (hashed bag of words) and remembers the last chat
// request body it received.
func kbUpstream(t *testing.T) (*httptest.Server, func() string) {
	var mu sync.Mutex
	var last []byte
	embed := func(text string, dims int) []float64 {
		v := make([]float64, dims)
		for _, w := range reWord.FindAllString(strings.ToLower(text), -1) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			x := h.Sum32()
			if x&(1<<31) != 0 {
				v[x%uint32(dims)]--
			} else {
				v[x%uint32(dims)]++
			}
		}
		var n float64
		for _, x := range v {
			n += x * x
		}
		for i := range v {
			if n > 0 {
				v[i] /= math.Sqrt(n)
			}
		}
		return v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			var req struct {
				Input      any `json:"input"`
				Dimensions int `json:"dimensions"`
			}
			_ = json.Unmarshal(body, &req)
			in, ok := req.Input.([]any)
			if !ok {
				in = []any{req.Input}
			}
			dims := req.Dimensions
			if dims == 0 {
				dims = 64
			}
			var data []map[string]any
			for i, s := range in {
				data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": embed(fmt.Sprint(s), dims)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": "emb", "data": data, "usage": map[string]int{"prompt_tokens": 9, "total_tokens": 9}})
			return
		}
		mu.Lock()
		last = body
		mu.Unlock()
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","created":1,"model":"up","choices":[{"index":0,"message":{"role":"assistant","content":"see [1]"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string { mu.Lock(); defer mu.Unlock(); return string(last) }
}

// startGateway runs the built binary and returns a logged-in operator client and the org id.
func startGateway(t *testing.T, env ...string) (*client, string) {
	bin := buildBinary(t)
	addr := freeAddr(t)
	var logs bytes.Buffer
	cmd := exec.Command(bin)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "MASTER_KEY=e2e-master-key-0123456789abcdef",
		"ADMIN_EMAIL=" + adminEmail, "ADMIN_PASSWORD=" + adminPassword, "DATA_DIR=" + t.TempDir(), "LISTEN_ADDR=" + addr, "SEED_CATALOG=0"}, env...)
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("gateway output:\n%s", logs.String())
		}
	})
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: "http://" + addr, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	eventually(t, 20*time.Second, "/healthz", func() bool {
		res, err := http.Get(c.base + "/healthz")
		if err != nil {
			return false
		}
		res.Body.Close()
		return res.StatusCode == 200
	})
	var me struct {
		Orgs []struct {
			ID string `json:"id"`
		} `json:"orgs"`
	}
	c.must(200, "POST", "/api/auth/login", `{"email":"`+adminEmail+`","password":"`+adminPassword+`"}`, &me)
	return c, me.Orgs[0].ID
}

// TestE2EKnowledgeBase: create KB → upload markdown → ready → search → grounded chat (the
// upstream receives the excerpts) → the embedding requests show up in the request logs.
func TestE2EKnowledgeBase(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e skipped in -short mode")
	}
	up, lastChat := kbUpstream(t)
	c, org := startGateway(t)
	var prov struct {
		ID string `json:"id"`
	}
	c.must(200, "POST", "/api/admin/providers", `{"name":"mock","type":"openai","base_url":"`+up.URL+`","enabled":true,"keys":[{"name":"k","value":"x","weight":1}]}`, &prov)
	c.must(200, "POST", "/api/admin/models", `{"id":"mock/chat","name":"Chat","enabled":true,"targets":[{"provider_id":"`+prov.ID+`","model":"up"}]}`, nil)
	c.must(200, "POST", "/api/admin/models", `{"id":"mock/emb","name":"Embed","kind":"embedding","input_price_micros":100000,"enabled":true,"targets":[{"provider_id":"`+prov.ID+`","model":"emb"}]}`, nil)
	var key struct {
		Secret string `json:"secret"`
	}
	c.must(201, "POST", "/api/orgs/"+org+"/keys", `{"name":"kb"}`, &key)
	bearer := []string{"Authorization", "Bearer " + key.Secret}
	eventually(t, 6*time.Second, "mock/emb in the catalog", func() bool {
		_, _, raw := c.do("GET", "/v1/models", "", bearer...)
		return bytes.Contains(raw, []byte(`"mock/emb"`))
	})

	var kb struct {
		ID string `json:"id"`
	}
	c.must(201, "POST", "/api/orgs/"+org+"/kbs", `{"name":"Handbook","embedding_model":"mock/emb","dimensions":512}`, &kb)
	base := "/api/orgs/" + org + "/kbs/" + kb.ID

	// multipart upload of a markdown handbook, plus an unrelated pasted note
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "handbook.md")
	fmt.Fprint(fw, "# Handbook\n\n## Billing\n\n### Refunds\n\nRefunds are paid back to the original card within 14 days of the request.\n\n## Travel\n\nBook flights through the travel portal at least two weeks ahead.\n")
	mw.Close()
	req, _ := http.NewRequest("POST", c.base+base+"/docs", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-GW-CSRF", "1")
	res, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.NewDecoder(res.Body).Decode(&doc)
	res.Body.Close()
	if res.StatusCode != 202 || doc.Status != "queued" {
		t.Fatalf("upload: %d %+v", res.StatusCode, doc)
	}
	c.must(202, "POST", base+"/docs", `{"filename":"note.txt","text":"The cafeteria serves soup on Mondays."}`, nil)
	eventually(t, 15*time.Second, "documents ready", func() bool {
		var docs []struct {
			Status, Error string
		}
		c.must(200, "GET", base+"/docs", "", &docs)
		ready := 0
		for _, d := range docs {
			if d.Status == "failed" {
				t.Fatalf("doc failed: %s", d.Error)
			}
			if d.Status == "ready" {
				ready++
			}
		}
		return ready == 2
	})

	var hits []struct {
		DocID    string `json:"doc_id"`
		Section  string `json:"section"`
		Filename string `json:"filename"`
	}
	c.must(200, "POST", base+"/search", `{"query":"how long do refunds take"}`, &hits)
	if len(hits) == 0 || hits[0].DocID != doc.ID || hits[0].Section != "Handbook > Billing > Refunds" {
		t.Fatalf("search: %+v", hits)
	}

	h := c.must(200, "POST", "/v1/chat/completions", `{"model":"mock/chat","messages":[{"role":"user","content":"How long do refunds take?"}]}`, nil, append(bearer, "X-GW-KB", kb.ID)...)
	if h.Get("X-GW-KB") != kb.ID || !strings.HasPrefix(h.Get("X-GW-KB-Sources"), doc.ID+"#") {
		t.Fatalf("headers: %q %q", h.Get("X-GW-KB"), h.Get("X-GW-KB-Sources"))
	}
	var sent struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	_ = json.Unmarshal([]byte(lastChat()), &sent)
	if n := len(sent.Messages); n != 2 || sent.Messages[1].Role != "system" ||
		!strings.Contains(sent.Messages[1].Content, "<kb_context>\n[1] handbook.md § Handbook > Billing > Refunds\nRefunds are paid back") {
		t.Fatalf("upstream did not receive the excerpts: %s", lastChat())
	}

	// embedding requests (ingestion, console search, grounding) are billed and logged
	eventually(t, 5*time.Second, "embedding logs", func() bool {
		var logs []struct {
			Model    string `json:"model"`
			KeyID    string `json:"key_id"`
			Cost     int64  `json:"cost_micros"`
			KBChunks int    `json:"kb_chunks"`
		}
		c.must(200, "GET", "/api/orgs/"+org+"/logs", "", &logs)
		var console, keyed, grounded bool
		for _, l := range logs {
			console = console || (l.Model == "mock/emb" && l.KeyID == "" && l.Cost > 0)
			keyed = keyed || (l.Model == "mock/emb" && l.KeyID != "")
			grounded = grounded || (l.Model == "mock/chat" && l.KBChunks > 0)
		}
		return console && keyed && grounded
	})
	// unknown API routes are JSON 404s, not the UI page
	if code, _, raw := c.do("GET", "/api/orgs/"+org+"/nope", ""); code != 404 || !bytes.Contains(raw, []byte(`"not found"`)) {
		t.Fatalf("unknown api route: %d %s", code, raw)
	}
	// the vector store API sees the console KB
	var stores struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	c.must(200, "GET", "/openai/v1/vector_stores", "", &stores, bearer...)
	if len(stores.Data) != 1 || stores.Data[0].ID != kb.ID {
		t.Fatalf("vector stores: %+v", stores)
	}
}
