package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
	"github.com/polyrail/polyrail/tools/mockprovider/asyncmock"
)

type asyncFixture struct {
	*fixture
	srv  *httptest.Server
	keyB string
	orgB string
}

func setupAsync(t *testing.T) *asyncFixture {
	f := setup(t)
	ctx := context.Background()
	upMux := http.NewServeMux()
	asyncmock.Register(upMux)
	up := httptest.NewServer(upMux)
	t.Cleanup(up.Close)
	p := &store.Provider{Name: "async", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.Model{
		{ID: "test/batchchat", Kind: "chat", Enabled: true, InputPrice: 1_000_000_000, OutputPrice: 2_000_000_000, Targets: []store.Target{{ProviderID: p.ID, Model: "up-chat"}}},
		{ID: "test/video", Kind: "video", Enabled: true, VideoSecondPrice: 250_000, Targets: []store.Target{{ProviderID: p.ID, Model: "sora-up"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	orgB := &store.Org{Name: "other"}
	if err := f.g.S.CreateOrg(ctx, orgB, ""); err != nil {
		t.Fatal(err)
	}
	keyB := KeyPrefix + "orgbsecret"
	if err := f.g.S.CreateKey(ctx, &store.APIKey{OrgID: orgB.ID, Name: "b"}, HashKey(keyB)); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for pat, h := range map[string]http.HandlerFunc{
		"POST /v1/files": f.g.UploadFile, "GET /v1/files": f.g.ListFiles, "GET /v1/files/{id}": f.g.GetFile,
		"GET /v1/files/{id}/content": f.g.FileContent, "DELETE /v1/files/{id}": f.g.DeleteFile,
		"POST /v1/batches": f.g.CreateBatch, "GET /v1/batches": f.g.ListBatches, "GET /v1/batches/{id}": f.g.GetBatch,
		"POST /v1/batches/{id}/cancel": f.g.CancelBatch,
		"POST /v1/videos":              f.g.CreateVideo, "GET /v1/videos/{id}": f.g.GetVideo,
		"GET /v1/videos/{id}/content": f.g.VideoContent, "DELETE /v1/videos/{id}": f.g.DeleteVideo,
	} {
		mux.HandleFunc(pat, h)
	}
	af := &asyncFixture{fixture: f, srv: httptest.NewServer(mux), keyB: keyB, orgB: orgB.ID}
	t.Cleanup(af.srv.Close)
	return af
}

func (f *asyncFixture) do(t *testing.T, method, path, key string, body io.Reader, ctype string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+key)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (f *asyncFixture) upload(t *testing.T, key, model, purpose string, content []byte) *http.Response {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("purpose", purpose)
	_ = mw.WriteField("x-gateway-model", model)
	fw, _ := mw.CreateFormFile("file", "in.jsonl")
	_, _ = fw.Write(content)
	_ = mw.Close()
	return f.do(t, "POST", "/v1/files", key, &buf, mw.FormDataContentType())
}

func (f *asyncFixture) spent(t *testing.T) int64 {
	n, err := f.g.Usage.Spent(context.Background(), usage.OrgSpendKey(f.org.ID))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func body(t *testing.T, r *http.Response) string {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

const batchLines = `{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"test/batchchat","messages":[{"role":"user","content":"hi"}]}}
{"custom_id":"b","method":"POST","url":"/v1/chat/completions","body":{"model":"test/batchchat","messages":[{"role":"user","content":"yo"}]}}
`

func TestBatchLifecycleIsolationAndExactlyOnceBilling(t *testing.T) {
	f := setupAsync(t)

	// wrong model in a line is refused before anything reaches the provider
	r := f.upload(t, f.key, "test/batchchat", "batch", []byte(strings.Replace(batchLines, `"model":"test/batchchat"`, `"model":"gpt-4-expensive"`, 1)))
	if m := readJSON(t, r); r.StatusCode != 400 || errCode(m) != "bad_batch_file" {
		t.Fatalf("bad line: %d %v", r.StatusCode, m)
	}
	r = f.upload(t, f.key, "test/batchchat", "batch", []byte(batchLines))
	file := readJSON(t, r)
	fileID, _ := file["id"].(string)
	if r.StatusCode != 200 || !strings.HasPrefix(fileID, "file-gw-") || file["purpose"] != "batch" {
		t.Fatalf("upload: %d %v", r.StatusCode, file)
	}
	// content comes back with the upstream model name the file was rewritten to
	r = f.do(t, "GET", "/v1/files/"+fileID+"/content", f.key, nil, "")
	if c := body(t, r); r.StatusCode != 200 || !strings.Contains(c, `"model":"up-chat"`) || !strings.Contains(c, `"custom_id":"b"`) {
		t.Fatalf("content: %d %s", r.StatusCode, c)
	}

	r = f.do(t, "POST", "/v1/batches", f.key, strings.NewReader(`{"input_file_id":"`+fileID+`","endpoint":"/v1/chat/completions","completion_window":"24h"}`), "application/json")
	batch := readJSON(t, r)
	batchID, _ := batch["id"].(string)
	if r.StatusCode != 200 || !strings.HasPrefix(batchID, "batch-gw-") || batch["input_file_id"] != fileID || batch["status"] != "in_progress" {
		t.Fatalf("create batch: %d %v", r.StatusCode, batch)
	}

	// org B sees none of it
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/files/" + fileID}, {"GET", "/v1/files/" + fileID + "/content"}, {"DELETE", "/v1/files/" + fileID},
		{"GET", "/v1/batches/" + batchID}, {"POST", "/v1/batches/" + batchID + "/cancel"},
	} {
		r := f.do(t, c.method, c.path, f.keyB, nil, "")
		if m := readJSON(t, r); r.StatusCode != 404 || errCode(m) != "not_found" {
			t.Fatalf("org B %s %s: %d %v", c.method, c.path, r.StatusCode, m)
		}
	}
	r = f.do(t, "POST", "/v1/batches", f.keyB, strings.NewReader(`{"input_file_id":"`+fileID+`","endpoint":"/v1/chat/completions"}`), "application/json")
	if r.StatusCode != 404 {
		t.Fatalf("org B batch on A's file: %d %s", r.StatusCode, body(t, r))
	}
	for _, path := range []string{"/v1/files", "/v1/batches"} {
		r = f.do(t, "GET", path, f.keyB, nil, "")
		if m := readJSON(t, r); len(m["data"].([]any)) != 0 {
			t.Fatalf("org B list %s: %v", path, m)
		}
	}

	// concurrent retrieves: the batch completes on the first; billing happens exactly once
	before := f.spent(t)
	var wg sync.WaitGroup
	outs := make([]map[string]any, 8)
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do(t, "GET", "/v1/batches/"+batchID, f.key, nil, "")
			outs[i] = readJSON(t, r)
		}()
	}
	wg.Wait()
	// 2 results × (10 in, 5 out) at $1000 / $2000 per 1M tokens = 20 000 + 20 000 micro-USD
	const want = 40_000
	if got := f.spent(t) - before; got != want {
		t.Fatalf("billed %d, want %d", got, want)
	}
	r = f.do(t, "GET", "/v1/batches", f.key, nil, "")
	list := readJSON(t, r)
	if f.spent(t)-before != want || len(list["data"].([]any)) != 1 {
		t.Fatalf("list re-billed or wrong: %d %v", f.spent(t)-before, list)
	}
	done := list["data"].([]any)[0].(map[string]any)
	outID, _ := done["output_file_id"].(string)
	if done["status"] != "completed" || !strings.HasPrefix(outID, "file-gw-") {
		t.Fatalf("completed batch: %v", done)
	}
	for _, o := range outs {
		if id, ok := o["output_file_id"]; ok && id != outID {
			t.Fatalf("output file mapped twice: %v vs %s", id, outID)
		}
	}
	r = f.do(t, "GET", "/v1/files/"+outID+"/content", f.key, nil, "")
	if c := body(t, r); r.StatusCode != 200 || strings.Count(c, `"custom_id"`) != 2 {
		t.Fatalf("results: %d %s", r.StatusCode, c)
	}
	r = f.do(t, "GET", "/v1/files/"+outID+"/content", f.keyB, nil, "")
	if r.StatusCode != 404 {
		t.Fatalf("org B output file: %d", r.StatusCode)
	}

	r = f.do(t, "DELETE", "/v1/files/"+fileID, f.key, nil, "")
	if m := readJSON(t, r); r.StatusCode != 200 || m["deleted"] != true {
		t.Fatalf("delete: %d %v", r.StatusCode, m)
	}
	r = f.do(t, "GET", "/v1/files/"+fileID, f.key, nil, "")
	if r.StatusCode != 404 {
		t.Fatalf("deleted file still there: %d", r.StatusCode)
	}
}

func TestFileUploadSizeLimitAndValidation(t *testing.T) {
	f := setupAsync(t)
	r := f.upload(t, f.key, "test/batchchat", "user_data", bytes.Repeat([]byte("x"), 2<<20))
	if m := readJSON(t, r); r.StatusCode != 413 || errCode(m) != "body_too_large" {
		t.Fatalf("size limit: %d %v", r.StatusCode, m)
	}
	r = f.upload(t, f.key, "", "user_data", []byte("x"))
	if r.StatusCode != 400 {
		t.Fatalf("missing model: %d", r.StatusCode)
	}
	r = f.upload(t, f.key, "nope/model", "user_data", []byte("x"))
	if m := readJSON(t, r); r.StatusCode != 404 || errCode(m) != "model_not_found" {
		t.Fatalf("unknown model: %d %v", r.StatusCode, m)
	}
	r = f.do(t, "GET", "/v1/files/file-gw-../../org/x", f.key, nil, "")
	if r.StatusCode != 404 {
		t.Fatalf("path id: %d", r.StatusCode)
	}
}

func TestVideoLifecycleAndBilling(t *testing.T) {
	f := setupAsync(t)
	before := f.spent(t)
	r := f.do(t, "POST", "/v1/videos", f.key, strings.NewReader(`{"model":"test/video","prompt":"a cat surfing","seconds":"4"}`), "application/json")
	v := readJSON(t, r)
	id, _ := v["id"].(string)
	if r.StatusCode != 200 || !strings.HasPrefix(id, "video-gw-") || v["model"] != "test/video" || v["status"] != "completed" {
		t.Fatalf("create: %d %v", r.StatusCode, v)
	}
	const want = 1_000_000 // 4 s × $0.25
	if got := f.spent(t) - before; got != want {
		t.Fatalf("billed %d, want %d", got, want)
	}
	r = f.do(t, "GET", "/v1/videos/"+id, f.key, nil, "")
	if m := readJSON(t, r); r.StatusCode != 200 || m["id"] != id || fmt.Sprint(m["seconds"]) != "4" {
		t.Fatalf("retrieve: %d %v", r.StatusCode, m)
	}
	r = f.do(t, "GET", "/v1/videos/"+id+"/content", f.key, nil, "")
	if c := body(t, r); r.StatusCode != 200 || c != string(asyncmock.MP4) || r.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %d %q", r.StatusCode, c)
	}
	if got := f.spent(t) - before; got != want {
		t.Fatalf("re-billed: %d", got)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/v1/videos/" + id}, {"GET", "/v1/videos/" + id + "/content"}, {"DELETE", "/v1/videos/" + id}} {
		if r := f.do(t, c.method, c.path, f.keyB, nil, ""); r.StatusCode != 404 {
			t.Fatalf("org B %s %s: %d", c.method, c.path, r.StatusCode)
		}
	}
	r = f.do(t, "POST", "/v1/videos", f.key, strings.NewReader(`{"model":"test/batchchat","prompt":"x"}`), "application/json")
	if m := readJSON(t, r); r.StatusCode != 400 || errCode(m) != "wrong_model_kind" {
		t.Fatalf("wrong kind: %d %v", r.StatusCode, m)
	}
	r = f.do(t, "DELETE", "/v1/videos/"+id, f.key, nil, "")
	if m := readJSON(t, r); r.StatusCode != 200 || m["deleted"] != true {
		t.Fatalf("delete: %d %v", r.StatusCode, m)
	}
	if r := f.do(t, "GET", "/v1/videos/"+id, f.key, nil, ""); r.StatusCode != 404 {
		t.Fatalf("deleted video: %d", r.StatusCode)
	}
}
