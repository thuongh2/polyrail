package gateway

// Images (generations, edits, variations), rerank, OCR and token counting.
// Same pipeline as chat: authenticate → resolve model kind → admit → guardrails on text inputs →
// Bifrost core → settle/record. Requests are parsed into Bifrost core's own request types.

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/cohere"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

func badRequest(w http.ResponseWriter, msg string) {
	writeError(w, errf(400, "invalid_request_error", "bad_request", "%s", msg))
}

// imageDone settles, records (per-image price plus tokens) and writes an image response.
func (g *Gateway) imageDone(w http.ResponseWriter, bctx *schemas.BifrostContext, snap *Snapshot, p *principal, m store.Model, ts []store.Target, resp *schemas.BifrostImageGenerationResponse, start time.Time) {
	provName := g.settle(bctx, snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0, 0)
	l := store.RequestLog{Status: 200, Provider: provName, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if u := resp.Usage; u != nil {
		l.InTokens, l.OutTokens = u.InputTokens, u.OutputTokens
	}
	l.Cost = int64(len(resp.Data))*m.ImagePrice + cost(m, l.InTokens, l.OutTokens)
	setCost(w, g.record(p, m, l, start))
	resp.Model = m.ID
	writeJSONHidden(w, provName, resp)
}

// ---- POST /v1/images/generations (JSON) ----

func (g *Gateway) ImageGenerations(w http.ResponseWriter, r *http.Request) {
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
	var req openai.OpenAIImageGenerationRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || req.Prompt == "" {
		badRequest(w, "body must be an image generation request with model and prompt")
		return
	}
	if req.Stream != nil && *req.Stream {
		badRequest(w, "stream is not supported for images; omit it to receive the images")
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "image")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostImageGenerationRequest(bctx)
	segs := []*string{&breq.Input.Prompt}
	if breq.Params.NegativePrompt != nil {
		segs = append(segs, breq.Params.NegativePrompt)
	}
	if !g.guard(w, p, m, segs, start) {
		return
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.ImageGenerationRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return
	}
	g.imageDone(w, bctx, snap, p, m, ts, resp, start)
}

// imageForm parses a multipart image request; it returns the form images (field image[] or image)
// and false after writing the error. The caller must RemoveAll the form.
func (g *Gateway) imageForm(w http.ResponseWriter, r *http.Request) ([]schemas.ImageInput, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxBodyBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil { // parts over 8 MB spill to temp files
		badRequest(w, "body must be multipart/form-data with model and image (max "+strconv.FormatInt(g.cfg.MaxBodyBytes, 10)+" bytes)")
		return nil, false
	}
	files := r.MultipartForm.File["image[]"]
	if len(files) == 0 {
		files = r.MultipartForm.File["image"]
	}
	if r.FormValue("model") == "" || len(files) == 0 {
		badRequest(w, "form fields model and image are required")
		return nil, false
	}
	if r.FormValue("stream") == "true" {
		badRequest(w, "stream is not supported for images; omit it to receive the images")
		return nil, false
	}
	imgs := make([]schemas.ImageInput, 0, len(files))
	for _, fh := range files {
		b, err := readPart(fh)
		if err != nil || len(b) == 0 {
			badRequest(w, "cannot read an image file")
			return nil, false
		}
		imgs = append(imgs, schemas.ImageInput{Image: b})
	}
	return imgs, true
}

func readPart(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// formOpt points dst at the form value key when it is set; formInt does the same for integers.
func formOpt(r *http.Request, dst **string, key string) {
	if v := r.FormValue(key); v != "" {
		*dst = &v
	}
}

func formInt(r *http.Request, dst **int, key string) bool {
	v := r.FormValue(key)
	if v == "" {
		return true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return false
	}
	*dst = &n
	return true
}

// ---- POST /v1/images/edits (multipart) ----

func (g *Gateway) ImageEdits(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	imgs, ok := g.imageForm(w, r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !ok {
		return
	}
	req := openai.OpenAIImageEditRequest{Model: r.FormValue("model"), Input: &schemas.ImageEditInput{Images: imgs, Prompt: r.FormValue("prompt")}}
	if req.Input.Prompt == "" {
		badRequest(w, "form field prompt is required")
		return
	}
	if !formInt(r, &req.N, "n") {
		badRequest(w, "n must be an integer")
		return
	}
	for key, dst := range map[string]**string{"size": &req.Size, "quality": &req.Quality, "response_format": &req.ResponseFormat,
		"background": &req.Background, "input_fidelity": &req.InputFidelity, "output_format": &req.OutputFormat, "user": &req.User} {
		formOpt(r, dst, key)
	}
	if fhs := r.MultipartForm.File["mask"]; len(fhs) > 0 {
		mask, err := readPart(fhs[0])
		if err != nil {
			badRequest(w, "cannot read the mask file")
			return
		}
		req.Mask = mask
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "image")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostImageEditRequest(bctx)
	if !g.guard(w, p, m, []*string{&breq.Input.Prompt}, start) {
		return
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.ImageEditRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return
	}
	g.imageDone(w, bctx, snap, p, m, ts, resp, start)
}

// ---- POST /v1/images/variations (multipart, no text input) ----

func (g *Gateway) ImageVariations(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	imgs, ok := g.imageForm(w, r)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !ok {
		return
	}
	req := openai.OpenAIImageVariationRequest{Model: r.FormValue("model"), Input: &schemas.ImageVariationInput{Image: imgs[0]}}
	if !formInt(r, &req.N, "n") {
		badRequest(w, "n must be an integer")
		return
	}
	formOpt(r, &req.Size, "size")
	formOpt(r, &req.ResponseFormat, "response_format")
	formOpt(r, &req.User, "user")
	m, ts, snap, ok := g.target(w, p, req.Model, "image")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostImageVariationRequest(bctx)
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.ImageVariationRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return
	}
	g.imageDone(w, bctx, snap, p, m, ts, resp, start)
}

// ---- POST /v1/rerank (Cohere / Jina format) ----

func (g *Gateway) Rerank(w http.ResponseWriter, r *http.Request) {
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
	var req cohere.CohereRerankRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || req.Query == "" || len(req.Documents) == 0 {
		badRequest(w, "body must be a rerank request with model, query and documents")
		return
	}
	if req.TopN != nil && *req.TopN < 1 {
		badRequest(w, "top_n must be at least 1")
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "rerank")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostRerankRequest(bctx)
	segs := []*string{&breq.Query}
	for i := range breq.Documents {
		segs = append(segs, &breq.Documents[i].Text)
	}
	if !g.guard(w, p, m, segs, start) {
		return
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.RerankRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return
	}
	provName := g.settle(bctx, snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0, 0)
	l := store.RequestLog{Status: 200, Provider: provName, Upstream: resp.ExtraFields.RoutingInfo.Model}
	chatTokens(resp.Usage).into(&l)
	g.record(p, m, l, start) // tokens + request price
	resp.Model = m.ID
	writeJSONHidden(w, provName, resp)
}

// ---- POST /v1/ocr (Mistral format: document {type document_url|image_url, ...}) ----

// ocrRequest is the request body Bifrost's own HTTP transport accepts for OCR (core has no
// provider-format request type with a converter for it).
type ocrRequest struct {
	Model    string              `json:"model"`
	ID       *string             `json:"id,omitempty"`
	Document schemas.OCRDocument `json:"document"`
	schemas.OCRParameters
}

func (g *Gateway) OCR(w http.ResponseWriter, r *http.Request) {
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
	var req ocrRequest
	err := sonic.Unmarshal(body, &req)
	d := req.Document
	switch {
	case err != nil || req.Model == "":
		badRequest(w, "body must be an OCR request with model and document")
		return
	case d.Type == schemas.OCRDocumentTypeDocumentURL && (d.DocumentURL == nil || *d.DocumentURL == ""),
		d.Type == schemas.OCRDocumentTypeImageURL && (d.ImageURL == nil || *d.ImageURL == ""),
		d.Type != schemas.OCRDocumentTypeDocumentURL && d.Type != schemas.OCRDocumentTypeImageURL:
		badRequest(w, "document must be {type: document_url, document_url} or {type: image_url, image_url}")
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "ocr")
	if !ok {
		return
	}
	breq := &schemas.BifrostOCRRequest{ID: req.ID, Document: req.Document, Params: &req.OCRParameters}
	if req.DocumentAnnotationPrompt != nil && !g.guard(w, p, m, []*string{req.DocumentAnnotationPrompt}, start) {
		return
	}
	bctx := newBifrostCtx(r)
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.OCRRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return
	}
	provName := g.settle(bctx, snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0, 0)
	g.record(p, m, store.RequestLog{Status: 200, Provider: provName, Upstream: resp.ExtraFields.RoutingInfo.Model}, start) // request price
	resp.Model = m.ID
	writeJSONHidden(w, provName, resp)
}

// ---- token counting: POST /v1/responses/input_tokens and /anthropic/v1/messages/count_tokens ----

// countTokens runs a count-tokens request for a chat model. It is admitted and rate limited like
// any call but recorded at cost 0. A nil result means the error response was already written.
func (g *Gateway) countTokens(w http.ResponseWriter, r *http.Request, p *principal, model string, toBifrost func(*schemas.BifrostContext) *schemas.BifrostResponsesRequest) *schemas.BifrostCountTokensResponse {
	start := time.Now()
	m, ts, snap, ok := g.target(w, p, model, "chat")
	if !ok {
		return nil
	}
	bctx := newBifrostCtx(r)
	breq := toBifrost(bctx)
	var maxOut *int
	if breq.Params != nil {
		maxOut = breq.Params.MaxOutputTokens
	}
	if g.prepareResponses(w, nil, p, m, ts, snap, breq, start) == nil { // counting is not grounded
		return nil
	}
	breq.Params.MaxOutputTokens = maxOut // the org's output cap does not apply to counting
	resp, berr := g.BF.CountTokensRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return nil
	}
	free := m
	free.InputPrice, free.OutputPrice, free.RequestPrice = 0, 0, 0 // not billed
	provName := g.settle(bctx, snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0, 0)
	g.record(p, free, store.RequestLog{Status: 200, Provider: provName, Upstream: resp.ExtraFields.RoutingInfo.Model}, start)
	w.Header().Set("x-gateway-provider", provName)
	return resp
}

func (g *Gateway) ResponsesInputTokens(w http.ResponseWriter, r *http.Request) {
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
	var req openai.OpenAIResponsesRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" {
		badRequest(w, "body must be a responses request with model and input")
		return
	}
	resp := g.countTokens(w, r, p, req.Model, req.ToBifrostResponsesRequest)
	if resp == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "response.input_tokens", "input_tokens": resp.InputTokens})
}

func (g *Gateway) AnthropicCountTokens(rw http.ResponseWriter, r *http.Request) {
	w := anthropicWriter{rw}
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
	var req anthropic.AnthropicMessageRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || len(req.Messages) == 0 {
		badRequest(w, "body must be a count tokens request with model and messages")
		return
	}
	resp := g.countTokens(w, r, p, req.Model, req.ToBifrostResponsesRequest)
	if resp == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(anthropic.ToAnthropicCountTokensResponse(resp))
}
