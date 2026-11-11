package gateway

// Async APIs: files, batches and videos (OpenAI-compatible) on Bifrost core.
//
// Tenant isolation: upstream ids are global per provider account, and many orgs share a public
// provider, so clients never see or send upstream ids. Every object is a store.Job under
// <kind>/<org>/<id>; the gateway id ("file-gw-…", "batch-gw-…", "video-gw-…") is looked up under
// the caller's org only, so another org's id is a 404. Upstream ids inside responses (batch
// input/output/error files, remix sources) are mapped to gateway ids or dropped.
//
// Billing: a batch is billed once, when it is first seen finished with an output file (on
// retrieve or list): usage of every result is summed and priced at the model's token prices
// copied at creation (full price, no batch discount). A video is billed once, when first seen
// completed (create, retrieve or download): seconds × video_second_price_micros. store.ClaimBilling
// makes the charge exactly-once across concurrent requests and nodes.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/sjson"
)

const (
	filePrefix  = "file-gw-"
	batchPrefix = "batch-gw-"
	videoPrefix = "video-gw-"
)

var jobPrefix = map[string]string{store.JobFile: filePrefix, store.JobBatch: batchPrefix, store.JobVideo: videoPrefix}

// batchEndpoints maps each batch endpoint to the model kind it serves.
var batchEndpoints = map[string]string{
	"/v1/chat/completions": "chat", "/v1/completions": "chat", "/v1/responses": "chat", "/v1/embeddings": "embedding",
}

var filePurposes = []string{"batch", "user_data", "assistants", "vision", "fine-tune", "evals"}

func jobNotFound(kind, id string) *apiError {
	return errf(404, "invalid_request_error", "not_found", "no such %s: %s", strings.TrimSuffix(kind, "s"), id)
}

// gwID strips a gateway id prefix; "" when the id is not a gateway id of this kind.
func gwID(kind, id string) string {
	raw, ok := strings.CutPrefix(id, jobPrefix[kind])
	if !ok || !store.ValidJobID(raw) {
		return ""
	}
	return raw
}

// loadJob returns the caller's job named by the {id} path value, or writes a 404.
func (g *Gateway) loadJob(w http.ResponseWriter, r *http.Request, p *principal, kind string) (*store.Job, bool) {
	id := r.PathValue("id")
	j, err := g.S.GetJob(r.Context(), kind, p.Org.ID, gwID(kind, id))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, jobNotFound(kind, id))
		return nil, false
	}
	if err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return nil, false
	}
	return j, true
}

// jobConn is a Bifrost context pinned to one key of the provider holding a job. Upstream objects
// belong to one provider account, so every call about a job uses the key that created it.
type jobConn struct {
	ctx     *schemas.BifrostContext
	bp      schemas.ModelProvider
	name    string
	private bool
	keyID   string
}

// connect pins a request to provider providerID and its key keyID ("" = pick one by weight among
// the keys serving upstream model). The provider is re-checked like any request: enabled, and for
// org providers a public address unless private URLs are allowed.
func (g *Gateway) connect(r *http.Request, providerID, keyID, upstream string) (*jobConn, *apiError) {
	unavailable := errf(503, "api_error", "no_provider", "the provider holding this object is not available")
	prov, ok := g.snap.Load().Providers[providerID]
	if !ok || (prov.OrgID != "" && !g.cfg.AllowPrivateURLs && !publicURLCached(prov.BaseURL)) {
		return nil, unavailable
	}
	bp := BifrostKey(prov)
	keys, err := (&account{snap: g.snap.Load}).GetKeysForProvider(r.Context(), bp)
	if err != nil {
		return nil, unavailable
	}
	var pick *schemas.Key
	var total float64
	for i := range keys {
		k := &keys[i]
		if keyID != "" && k.ID == keyID {
			pick = k
			break
		}
		if keyID == "" && (slices.Contains(k.Models, "*") || slices.Contains(k.Models, upstream)) {
			if total += k.Weight; rand.Float64()*total < k.Weight {
				pick = k // weighted reservoir choice
			}
		}
	}
	if pick == nil {
		return nil, unavailable
	}
	c := &jobConn{ctx: newBifrostCtx(r), bp: bp, name: prov.Name, private: prov.OrgID != "", keyID: pick.ID}
	c.ctx.SetValue(schemas.BifrostContextKeyDirectKey, *pick)
	return c, nil
}

func (g *Gateway) jobConnect(r *http.Request, j *store.Job) (*jobConn, *apiError) {
	return g.connect(r, j.ProviderID, j.ProvKeyID, j.Upstream)
}

// jobModel is the model as priced when the job was created (for record and cost).
func jobModel(j *store.Job) store.Model {
	return store.Model{ID: j.Model, OrgID: j.ModelOrg, InputPrice: j.InputPrice, OutputPrice: j.OutputPrice, VideoSecondPrice: j.VideoSecondPrice}
}

// bill charges a finished job once, to the key that created it.
func (g *Gateway) bill(ctx context.Context, p *principal, kind string, j *store.Job, provName string, l store.RequestLog) {
	err := g.S.ClaimBilling(ctx, kind, j.OrgID, j.ID, l.Cost)
	j.Billed = err == nil || errors.Is(err, store.ErrBilled)
	if err != nil {
		if !j.Billed {
			slog.Error("async billing claim failed", "kind", kind, "job", j.ID, "err", err)
		}
		return
	}
	l.Status, l.Provider, l.Upstream = 200, provName, j.Upstream
	owner := &principal{Key: store.APIKey{ID: j.KeyID, OrgID: j.OrgID}, Org: p.Org}
	g.record(owner, jobModel(j), l, time.Now())
}

func writeObj(w http.ResponseWriter, v any) {
	out, err := hideExtra(v)
	if err != nil {
		writeError(w, errf(500, "api_error", "encode", "cannot encode response"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// listPage reads ?limit (1..100, default 20) and ?after (a gateway id) and lists a page of jobs.
func (g *Gateway) listPage(w http.ResponseWriter, r *http.Request, p *principal, kind string) ([]store.Job, bool, bool) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, errf(400, "invalid_request_error", "bad_request", "limit must be 1..100"))
			return nil, false, false
		}
		limit = n
	}
	after := ""
	if v := r.URL.Query().Get("after"); v != "" {
		if after = gwID(kind, v); after == "" {
			writeError(w, errf(400, "invalid_request_error", "bad_request", "after must be an id from this list"))
			return nil, false, false
		}
	}
	js, err := g.S.ListJobs(r.Context(), kind, p.Org.ID, after, limit+1)
	if err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return nil, false, false
	}
	more := len(js) > limit
	if more {
		js = js[:limit]
	}
	return js, more, true
}

func listOut(data []any, more bool) map[string]any {
	out := map[string]any{"object": "list", "data": data, "has_more": more}
	if len(data) > 0 {
		out["first_id"] = data[0].(map[string]any)["id"]
		out["last_id"] = data[len(data)-1].(map[string]any)["id"]
	}
	return out
}

// ---- files ----

func fileOut(j *store.Job, status string) map[string]any {
	out := map[string]any{"id": filePrefix + j.ID, "object": "file", "bytes": j.Bytes, "created_at": j.CreatedAt.Unix(),
		"filename": j.Filename, "purpose": j.Purpose, "model": j.Model}
	if status != "" {
		out["status"] = status
	}
	return out
}

// rewriteBatchFile checks every JSONL line of a batch input file: its url must be an endpoint of
// the model's kind and body.model the catalog model id, which is replaced by the upstream model
// name. This pins what a batch may run (and how it is priced) to the model the file was made for.
func rewriteBatchFile(data []byte, m store.Model, upstream string) ([]byte, error) {
	var out bytes.Buffer
	for i, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var item struct {
			CustomID string `json:"custom_id"`
			URL      string `json:"url"`
			Body     struct {
				Model string `json:"model"`
			} `json:"body"`
		}
		if err := json.Unmarshal(line, &item); err != nil || item.CustomID == "" {
			return nil, fmt.Errorf("line %d: not a batch request object with custom_id", i+1)
		}
		if batchEndpoints[item.URL] != m.Kind {
			return nil, fmt.Errorf("line %d: url %q is not a batch endpoint for %s model %q", i+1, item.URL, m.Kind, m.ID)
		}
		if item.Body.Model != m.ID {
			return nil, fmt.Errorf("line %d: body.model must be %q (the file's x-gateway-model)", i+1, m.ID)
		}
		line, err := sjson.SetBytes(line, "body.model", upstream)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", i+1, err)
		}
		out.Write(bytes.TrimRight(line, "\r"))
		out.WriteByte('\n')
	}
	if out.Len() == 0 {
		return nil, errors.New("batch file has no requests")
	}
	return out.Bytes(), nil
}

// UploadFile: POST /v1/files (multipart purpose, file; model in form field or header x-gateway-model).
func (g *Gateway) UploadFile(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxBodyBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, errf(413, "invalid_request_error", "body_too_large", "request body exceeds %d bytes", g.cfg.MaxBodyBytes))
			return
		}
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be multipart/form-data with purpose and file"))
		return
	}
	defer r.MultipartForm.RemoveAll()
	modelID := r.FormValue("x-gateway-model")
	if modelID == "" {
		modelID = r.Header.Get("x-gateway-model")
	}
	purpose := r.FormValue("purpose")
	f, hdr, err := r.FormFile("file")
	if err != nil || modelID == "" || !slices.Contains(filePurposes, purpose) {
		writeError(w, errf(400, "invalid_request_error", "bad_request",
			"form fields file and purpose (%s) and the model (form field or header x-gateway-model) are required", strings.Join(filePurposes, ", ")))
		return
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || len(data) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "cannot read the file or it is empty"))
		return
	}
	m, ok := g.snap.Load().Model(p.Org.ID, modelID)
	kind := m.Kind
	if !ok {
		kind = "chat" // resolve reports model_not_found
	}
	m, ts, snap, ok := g.target(w, p, modelID, kind)
	if !ok {
		return
	}
	t := ts[0] // the file lives at the primary target; batches using it follow it there
	if purpose == "batch" {
		if data, err = rewriteBatchFile(data, m, t.Model); err != nil {
			writeError(w, errf(400, "invalid_request_error", "bad_batch_file", "%v", err))
			return
		}
	}
	c, e := g.connect(r, t.ProviderID, "", t.Model)
	if e != nil {
		writeError(w, e)
		return
	}
	resp, berr := g.BF.FileUploadRequest(c.ctx, &schemas.BifrostFileUploadRequest{
		Provider: c.bp, Model: &t.Model, File: data, Filename: hdr.Filename, Purpose: schemas.FilePurpose(purpose)})
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts[:1], berr, start)
		return
	}
	j := &store.Job{ID: store.NewJobID(), OrgID: p.Org.ID, KeyID: p.Key.ID, ProviderID: t.ProviderID, ProvKeyID: c.keyID, UpstreamID: resp.ID,
		Model: m.ID, ModelOrg: m.OrgID, Upstream: t.Model, Purpose: purpose, Filename: hdr.Filename, Bytes: int64(len(data))}
	if err := g.S.PutJob(r.Context(), store.JobFile, j); err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return
	}
	g.record(p, m, store.RequestLog{Status: 200, Provider: c.name, Upstream: t.Model}, start)
	writeObj(w, fileOut(j, string(resp.Status)))
}

// ListFiles: GET /v1/files (the caller's org only, newest first).
func (g *Gateway) ListFiles(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	js, more, ok := g.listPage(w, r, p, store.JobFile)
	if !ok {
		return
	}
	data := make([]any, 0, len(js))
	for i := range js {
		data = append(data, fileOut(&js[i], ""))
	}
	writeObj(w, listOut(data, more))
}

// GetFile: GET /v1/files/{id}.
func (g *Gateway) GetFile(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobFile)
	if !ok {
		return
	}
	c, e := g.jobConnect(r, j)
	if e != nil {
		writeError(w, e)
		return
	}
	resp, berr := g.BF.FileRetrieveRequest(c.ctx, &schemas.BifrostFileRetrieveRequest{Provider: c.bp, Model: &j.Upstream, FileID: j.UpstreamID})
	if berr != nil {
		writeError(w, fromBifrost(berr, c.private))
		return
	}
	writeObj(w, fileOut(j, string(resp.Status)))
}

// FileContent: GET /v1/files/{id}/content.
func (g *Gateway) FileContent(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobFile)
	if !ok {
		return
	}
	c, e := g.jobConnect(r, j)
	if e != nil {
		writeError(w, e)
		return
	}
	resp, berr := g.BF.FileContentRequest(c.ctx, &schemas.BifrostFileContentRequest{Provider: c.bp, Model: &j.Upstream, FileID: j.UpstreamID})
	if berr != nil {
		writeError(w, fromBifrost(berr, c.private))
		return
	}
	ctype := resp.ContentType
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(resp.Content)
}

// DeleteFile: DELETE /v1/files/{id}.
func (g *Gateway) DeleteFile(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobFile)
	if !ok {
		return
	}
	c, e := g.jobConnect(r, j)
	if e != nil {
		writeError(w, e)
		return
	}
	_, berr := g.BF.FileDeleteRequest(c.ctx, &schemas.BifrostFileDeleteRequest{Provider: c.bp, Model: &j.Upstream, FileID: j.UpstreamID})
	if berr != nil && (berr.StatusCode == nil || *berr.StatusCode != 404) { // gone upstream: drop the mapping too
		writeError(w, fromBifrost(berr, c.private))
		return
	}
	if err := g.S.DeleteJob(r.Context(), store.JobFile, p.Org.ID, j.ID); err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return
	}
	writeObj(w, map[string]any{"id": filePrefix + j.ID, "object": "file", "deleted": true})
}

// ---- batches ----

func terminalBatch(s string) bool {
	return s == "completed" || s == "failed" || s == "expired" || s == "cancelled" || s == "ended"
}

// CreateBatch: POST /v1/batches {input_file_id, endpoint, completion_window, metadata, model?}.
func (g *Gateway) CreateBatch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	body, e := g.readBody(w, r)
	if e != nil {
		writeError(w, e)
		return
	}
	var req struct {
		InputFileID      string            `json:"input_file_id"`
		Endpoint         string            `json:"endpoint"`
		CompletionWindow string            `json:"completion_window"`
		Metadata         map[string]string `json:"metadata"`
		Model            string            `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.InputFileID == "" || batchEndpoints[req.Endpoint] == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body needs input_file_id and endpoint (/v1/chat/completions, /v1/completions, /v1/responses or /v1/embeddings)"))
		return
	}
	file, err := g.S.GetJob(r.Context(), store.JobFile, p.Org.ID, gwID(store.JobFile, req.InputFileID))
	if err != nil {
		writeError(w, jobNotFound(store.JobFile, req.InputFileID))
		return
	}
	if file.Purpose != "batch" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "input file purpose must be batch"))
		return
	}
	if req.Model != "" && req.Model != file.Model {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "model must be the input file's model %q", file.Model))
		return
	}
	m, _, _, ok := g.target(w, p, file.Model, batchEndpoints[req.Endpoint])
	if !ok {
		return
	}
	if !slices.ContainsFunc(m.Targets, func(t store.Target) bool { return t.ProviderID == file.ProviderID }) {
		writeError(w, errf(503, "api_error", "no_provider", "the provider holding the input file no longer serves model %q", m.ID))
		return
	}
	c, e := g.jobConnect(r, file)
	if e != nil {
		writeError(w, e)
		return
	}
	resp, berr := g.BF.BatchCreateRequest(c.ctx, &schemas.BifrostBatchCreateRequest{Provider: c.bp, Model: &file.Upstream,
		InputFileID: file.UpstreamID, Endpoint: schemas.BatchEndpoint(req.Endpoint), CompletionWindow: req.CompletionWindow, Metadata: req.Metadata})
	if berr != nil {
		ae := fromBifrost(berr, c.private)
		g.record(p, m, store.RequestLog{Status: ae.status, Error: ae.msg, Provider: c.name}, start)
		writeError(w, ae)
		return
	}
	j := &store.Job{ID: store.NewJobID(), OrgID: p.Org.ID, KeyID: p.Key.ID, ProviderID: file.ProviderID, ProvKeyID: file.ProvKeyID, UpstreamID: resp.ID,
		Model: m.ID, ModelOrg: m.OrgID, Upstream: file.Upstream, InputPrice: m.InputPrice, OutputPrice: m.OutputPrice,
		InputFileID: file.ID, Purpose: req.Endpoint, Status: string(resp.Status)}
	if err := g.S.PutJob(r.Context(), store.JobBatch, j); err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return
	}
	g.record(p, m, store.RequestLog{Status: 200, Provider: c.name, Upstream: file.Upstream}, start)
	writeObj(w, batchOut(j, &schemas.BifrostBatchRetrieveResponse{Status: resp.Status, Endpoint: resp.Endpoint,
		CompletionWindow: resp.CompletionWindow, RequestCounts: resp.RequestCounts, Metadata: resp.Metadata, CreatedAt: resp.CreatedAt, ExpiresAt: resp.ExpiresAt}))
}

// batchOut is the OpenAI batch object with gateway ids. up may be nil (stored fields only).
func batchOut(j *store.Job, up *schemas.BifrostBatchRetrieveResponse) map[string]any {
	if up == nil {
		up = &schemas.BifrostBatchRetrieveResponse{Status: schemas.BatchStatus(j.Status), Endpoint: j.Purpose}
	}
	v := *up
	v.ID, v.Object, v.InputFileID, v.OutputFileID, v.ErrorFileID = batchPrefix+j.ID, "batch", filePrefix+j.InputFileID, nil, nil
	v.ResultsURL, v.OperationName, v.InputBlob, v.OutputBlob, v.ErrorBlob = nil, nil, nil, nil, nil
	if v.CreatedAt == 0 {
		v.CreatedAt = j.CreatedAt.Unix()
	}
	if j.OutputFileID != "" {
		v.OutputFileID = schemas.Ptr(filePrefix + j.OutputFileID)
	}
	if j.ErrorFileID != "" {
		v.ErrorFileID = schemas.Ptr(filePrefix + j.ErrorFileID)
	}
	raw, _ := hideExtra(v)
	out := map[string]any{}
	_ = sonic.Unmarshal(raw, &out)
	out["model"] = j.Model
	return out
}

// mapFile gives an upstream output/error file of a batch a gateway id, once.
func (g *Gateway) mapFile(ctx context.Context, b *store.Job, upstreamID string, field func(*store.Job) *string) error {
	if upstreamID == "" || *field(b) != "" {
		return nil
	}
	f := &store.Job{ID: store.NewJobID(), OrgID: b.OrgID, KeyID: b.KeyID, ProviderID: b.ProviderID, ProvKeyID: b.ProvKeyID, UpstreamID: upstreamID,
		Model: b.Model, ModelOrg: b.ModelOrg, Upstream: b.Upstream, Purpose: "batch_output", Filename: upstreamID + ".jsonl"}
	if err := g.S.PutJob(ctx, store.JobFile, f); err != nil {
		return err
	}
	nb, err := g.S.UpdateJob(ctx, store.JobBatch, b.OrgID, b.ID, func(j *store.Job) error {
		if *field(j) == "" {
			*field(j) = f.ID
		}
		return nil
	})
	if err != nil {
		return err
	}
	if *field(nb) != f.ID { // a concurrent request mapped it first
		_ = g.S.DeleteJob(ctx, store.JobFile, b.OrgID, f.ID)
	}
	*field(b) = *field(nb)
	return nil
}

// batchUsage sums token usage over successful results (chat, completions, responses, embeddings).
func batchUsage(results []schemas.BatchResultItem) (in, out int) {
	num := func(u map[string]any, keys ...string) int {
		for _, k := range keys {
			switch v := u[k].(type) {
			case float64:
				return int(v)
			case int64:
				return int(v)
			case json.Number:
				n, _ := v.Int64()
				return int(n)
			}
		}
		return 0
	}
	for _, r := range results {
		if r.Failed() || r.Response == nil {
			continue
		}
		u, _ := r.Response.Body["usage"].(map[string]any)
		in += num(u, "prompt_tokens", "input_tokens")
		out += num(u, "completion_tokens", "output_tokens")
	}
	return in, out
}

// refreshBatch fetches a batch upstream, maps its output files and bills it once when finished.
func (g *Gateway) refreshBatch(r *http.Request, p *principal, j *store.Job) (map[string]any, *apiError) {
	ctx := r.Context()
	c, e := g.jobConnect(r, j)
	if e != nil {
		return nil, e
	}
	up, berr := g.BF.BatchRetrieveRequest(c.ctx, &schemas.BifrostBatchRetrieveRequest{Provider: c.bp, Model: &j.Upstream, BatchID: j.UpstreamID})
	if berr != nil {
		return nil, fromBifrost(berr, c.private)
	}
	status := string(up.Status)
	if status != j.Status {
		if nj, err := g.S.UpdateJob(ctx, store.JobBatch, j.OrgID, j.ID, func(x *store.Job) error { x.Status = status; return nil }); err == nil {
			j = nj
		}
	}
	if !terminalBatch(status) {
		return batchOut(j, up), nil
	}
	if !j.Billed {
		if err := g.billBatch(r, p, j, c, up); err != nil {
			slog.Warn("batch billing deferred", "batch", j.ID, "err", err)
			out := batchOut(j, up) // results stay hidden until the batch is billed
			delete(out, "output_file_id")
			delete(out, "error_file_id")
			return out, nil
		}
	}
	for _, f := range []struct {
		id    *string
		field func(*store.Job) *string
	}{{up.OutputFileID, func(x *store.Job) *string { return &x.OutputFileID }}, {up.ErrorFileID, func(x *store.Job) *string { return &x.ErrorFileID }}} {
		if f.id != nil {
			if err := g.mapFile(ctx, j, *f.id, f.field); err != nil {
				return nil, errf(503, "api_error", "store_unavailable", "temporarily unavailable")
			}
		}
	}
	return batchOut(j, up), nil
}

func (g *Gateway) billBatch(r *http.Request, p *principal, j *store.Job, c *jobConn, up *schemas.BifrostBatchRetrieveResponse) error {
	var in, out int
	if up.OutputFileID != nil && *up.OutputFileID != "" {
		res, berr := g.BF.BatchResultsRequest(c.ctx, &schemas.BifrostBatchResultsRequest{Provider: c.bp, Model: &j.Upstream, BatchID: j.UpstreamID})
		if berr != nil {
			return fmt.Errorf("batch results: %s", fromBifrost(berr, false).msg)
		}
		in, out = batchUsage(res.Results)
	}
	g.bill(r.Context(), p, store.JobBatch, j, c.name, store.RequestLog{InTokens: in, OutTokens: out, Cost: cost(jobModel(j), in, out)})
	return nil
}

// GetBatch: GET /v1/batches/{id}.
func (g *Gateway) GetBatch(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobBatch)
	if !ok {
		return
	}
	out, e := g.refreshBatch(r, p, j)
	if e != nil {
		writeError(w, e)
		return
	}
	writeObj(w, out)
}

// ListBatches: GET /v1/batches. Batches not yet finished and billed are refreshed upstream.
func (g *Gateway) ListBatches(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	js, more, ok := g.listPage(w, r, p, store.JobBatch)
	if !ok {
		return
	}
	data := make([]any, 0, len(js))
	for i := range js {
		j := &js[i]
		if !j.Billed {
			if out, e := g.refreshBatch(r, p, j); e == nil {
				data = append(data, out)
				continue
			}
		}
		data = append(data, batchOut(j, nil))
	}
	writeObj(w, listOut(data, more))
}

// CancelBatch: POST /v1/batches/{id}/cancel.
func (g *Gateway) CancelBatch(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobBatch)
	if !ok {
		return
	}
	c, e := g.jobConnect(r, j)
	if e != nil {
		writeError(w, e)
		return
	}
	up, berr := g.BF.BatchCancelRequest(c.ctx, &schemas.BifrostBatchCancelRequest{Provider: c.bp, Model: &j.Upstream, BatchID: j.UpstreamID})
	if berr != nil {
		writeError(w, fromBifrost(berr, c.private))
		return
	}
	writeObj(w, batchOut(j, &schemas.BifrostBatchRetrieveResponse{Status: up.Status, Endpoint: j.Purpose, RequestCounts: up.RequestCounts,
		CancellingAt: up.CancellingAt, CancelledAt: up.CancelledAt}))
}

// ---- videos ----

// CreateVideo: POST /v1/videos, JSON or multipart {model, prompt, seconds, size, input_reference}.
func (g *Gateway) CreateVideo(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	var req openai.OpenAIVideoGenerationRequest
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxBodyBytes)
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeError(w, errf(400, "invalid_request_error", "bad_request", "invalid multipart body (max %d bytes)", g.cfg.MaxBodyBytes))
			return
		}
		defer r.MultipartForm.RemoveAll()
		req.Model, req.Prompt, req.Size = r.FormValue("model"), r.FormValue("prompt"), r.FormValue("size")
		if v := r.FormValue("seconds"); v != "" {
			req.Seconds = &v
		}
		if f, _, err := r.FormFile("input_reference"); err == nil {
			req.InputReference, err = io.ReadAll(f)
			f.Close()
			if err != nil {
				writeError(w, errf(400, "invalid_request_error", "bad_request", "cannot read input_reference"))
				return
			}
		}
	} else {
		body, e := g.readBody(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		if err := sonic.Unmarshal(body, &req); err != nil {
			req.Model = ""
		}
	}
	if req.Model == "" || req.Prompt == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a video request with model and prompt"))
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "video")
	if !ok {
		return
	}
	if !g.guard(w, p, m, []*string{&req.Prompt}, start) {
		return
	}
	breq := &schemas.BifrostVideoGenerationRequest{Input: &schemas.VideoGenerationInput{Prompt: req.Prompt}, Params: &req.VideoGenerationParameters}
	if len(req.InputReference) > 0 {
		ref := "data:" + http.DetectContentType(req.InputReference) + ";base64," + base64.StdEncoding.EncodeToString(req.InputReference)
		breq.Input.InputReference = &ref
	}
	t := ts[0] // the video job lives at one provider account; no fallbacks across accounts
	c, e := g.connect(r, t.ProviderID, "", t.Model)
	if e != nil {
		writeError(w, e)
		return
	}
	breq.Provider, breq.Model = c.bp, t.Model
	resp, berr := g.BF.VideoGenerationRequest(c.ctx, breq)
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts[:1], berr, start)
		return
	}
	g.settle(snap, m, ts[:1], c.bp, false, start, 0)
	j := &store.Job{ID: store.NewJobID(), OrgID: p.Org.ID, KeyID: p.Key.ID, ProviderID: t.ProviderID, ProvKeyID: c.keyID, UpstreamID: resp.ID,
		Model: m.ID, ModelOrg: m.OrgID, Upstream: t.Model, VideoSecondPrice: m.VideoSecondPrice, Status: string(resp.Status)}
	if err := g.S.PutJob(r.Context(), store.JobVideo, j); err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return
	}
	g.record(p, m, store.RequestLog{Status: 200, Provider: c.name, Upstream: t.Model}, start)
	g.billVideo(r.Context(), p, j, c.name, resp)
	writeObj(w, videoOut(j, resp))
}

func videoOut(j *store.Job, v *schemas.BifrostVideoGenerationResponse) *schemas.BifrostVideoGenerationResponse {
	out := *v
	out.ID, out.Object, out.Model, out.RemixedFromVideoID = videoPrefix+j.ID, "video", j.Model, nil
	return &out
}

// billVideo charges a completed video once: seconds × video_second_price_micros.
func (g *Gateway) billVideo(ctx context.Context, p *principal, j *store.Job, provName string, v *schemas.BifrostVideoGenerationResponse) {
	if j.Billed || v.Status != schemas.VideoStatusCompleted {
		return
	}
	var seconds float64
	if v.Seconds != nil {
		seconds, _ = strconv.ParseFloat(*v.Seconds, 64)
	}
	g.bill(ctx, p, store.JobVideo, j, provName, store.RequestLog{Cost: int64(math.Round(seconds * float64(j.VideoSecondPrice)))})
}

// refreshVideo retrieves a video upstream and bills it if it just completed.
func (g *Gateway) refreshVideo(r *http.Request, p *principal, j *store.Job) (*schemas.BifrostVideoGenerationResponse, *apiError) {
	c, e := g.jobConnect(r, j)
	if e != nil {
		return nil, e
	}
	v, berr := g.BF.VideoRetrieveRequest(c.ctx, &schemas.BifrostVideoRetrieveRequest{Provider: c.bp, ID: j.UpstreamID})
	if berr != nil {
		return nil, fromBifrost(berr, c.private)
	}
	if string(v.Status) != j.Status {
		_, _ = g.S.UpdateJob(r.Context(), store.JobVideo, j.OrgID, j.ID, func(x *store.Job) error { x.Status = string(v.Status); return nil })
	}
	g.billVideo(r.Context(), p, j, c.name, v)
	return v, nil
}

// GetVideo: GET /v1/videos/{id}.
func (g *Gateway) GetVideo(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobVideo)
	if !ok {
		return
	}
	v, e := g.refreshVideo(r, p, j)
	if e != nil {
		writeError(w, e)
		return
	}
	writeObj(w, videoOut(j, v))
}

// VideoContent: GET /v1/videos/{id}/content. An unbilled video is retrieved (and billed) first,
// so content is never served without the charge.
func (g *Gateway) VideoContent(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobVideo)
	if !ok {
		return
	}
	if !j.Billed {
		v, e := g.refreshVideo(r, p, j)
		if e != nil {
			writeError(w, e)
			return
		}
		if v.Status != schemas.VideoStatusCompleted {
			writeError(w, errf(409, "invalid_request_error", "video_not_ready", "video is %s", v.Status))
			return
		}
		if !j.Billed { // never serve content the charge for which was not stored
			writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
			return
		}
	}
	c, e := g.jobConnect(r, j)
	if e != nil {
		writeError(w, e)
		return
	}
	dreq := &schemas.BifrostVideoDownloadRequest{Provider: c.bp, ID: j.UpstreamID}
	if v := r.URL.Query().Get("variant"); v != "" {
		dreq.Variant = schemas.Ptr(schemas.VideoDownloadVariant(v))
	}
	resp, berr := g.BF.VideoDownloadRequest(c.ctx, dreq)
	if berr != nil {
		writeError(w, fromBifrost(berr, c.private))
		return
	}
	w.Header().Set("Content-Type", resp.ContentType)
	_, _ = w.Write(resp.Content)
}

// DeleteVideo: DELETE /v1/videos/{id}.
func (g *Gateway) DeleteVideo(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	j, ok := g.loadJob(w, r, p, store.JobVideo)
	if !ok {
		return
	}
	c, e := g.jobConnect(r, j)
	if e != nil {
		writeError(w, e)
		return
	}
	_, berr := g.BF.VideoDeleteRequest(c.ctx, &schemas.BifrostVideoDeleteRequest{Provider: c.bp, ID: j.UpstreamID})
	if berr != nil && (berr.StatusCode == nil || *berr.StatusCode != 404) {
		writeError(w, fromBifrost(berr, c.private))
		return
	}
	if err := g.S.DeleteJob(r.Context(), store.JobVideo, p.Org.ID, j.ID); err != nil {
		writeError(w, errf(503, "api_error", "store_unavailable", "temporarily unavailable"))
		return
	}
	writeObj(w, map[string]any{"id": videoPrefix + j.ID, "object": "video.deleted", "deleted": true})
}
