package gateway

// Google GenAI (Gemini) SDK drop-in under /genai/v1beta (and /genai/v1):
//   POST models/{model}:generateContent | :streamGenerateContent (?alt=sse) | :countTokens
//   POST models/{model}:embedContent | :batchEmbedContents
//   GET  models, GET models/{model}
// {model} is a catalog id; its "/" may be sent raw (what the SDKs do with "vendor/name") or as
// %2F, and a leading "models/" is dropped. Requests are converted with Bifrost core's Gemini
// converters into Responses / Embedding requests, so any provider can serve them; replies, stream
// chunks and errors are converted back to the Gemini format.

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
)

// genaiWriter marks a response that speaks the Gemini format (errors as {"error":{code,message,status}}).
type genaiWriter struct{ http.ResponseWriter }

func (w genaiWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// genaiStatus maps HTTP statuses to google.rpc.Code names.
var genaiStatus = map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 402: "FAILED_PRECONDITION", 403: "PERMISSION_DENIED",
	404: "NOT_FOUND", 413: "INVALID_ARGUMENT", 429: "RESOURCE_EXHAUSTED", 499: "CANCELLED", 501: "UNIMPLEMENTED", 502: "UNAVAILABLE",
	503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED"}

func genaiError(e *apiError) map[string]any {
	st, ok := genaiStatus[e.status]
	if !ok {
		st = "INTERNAL"
	}
	return map[string]any{"error": map[string]any{"code": e.status, "message": e.msg, "status": st}}
}

// genaiAuth lets the Gemini SDKs' x-goog-api-key header or ?key= query carry our API key.
func genaiAuth(r *http.Request) {
	if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
		return
	}
	k := r.Header.Get("x-goog-api-key")
	if k == "" {
		k = r.URL.Query().Get("key")
	}
	r.Header.Set("x-api-key", k)
}

var genaiMethods = []string{"streamGenerateContent", "generateContent", "batchEmbedContents", "embedContent", "countTokens"}

// genaiPath splits "models/vendor/name:method" (the part after /models/) into model id and method.
func genaiPath(rest string) (model, method string) {
	for _, m := range genaiMethods {
		if s, ok := strings.CutSuffix(rest, ":"+m); ok && s != "" {
			return strings.TrimPrefix(s, "models/"), m
		}
	}
	return "", ""
}

// GenAI serves POST /genai/v1beta/models/{rest...}.
func (g *Gateway) GenAI(rw http.ResponseWriter, r *http.Request) {
	w := genaiWriter{rw}
	start := time.Now()
	model, method := genaiPath(r.PathValue("rest"))
	if method == "" {
		writeError(w, errf(404, "invalid_request_error", "not_found", "use models/{model}:generateContent, :streamGenerateContent, :countTokens, :embedContent or :batchEmbedContents"))
		return
	}
	genaiAuth(r)
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
	switch method {
	case "generateContent", "streamGenerateContent":
		g.genaiGenerate(w, r, p, model, method == "streamGenerateContent", body, start)
	case "countTokens":
		g.genaiCountTokens(w, r, p, model, body, start)
	default:
		g.genaiEmbed(w, r, p, model, method == "batchEmbedContents", body, start)
	}
}

func (g *Gateway) genaiGenerate(w http.ResponseWriter, r *http.Request, p *principal, model string, stream bool, body []byte, start time.Time) {
	var req gemini.GeminiGenerationRequest
	if err := sonic.Unmarshal(body, &req); err != nil || len(req.Contents) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a generateContent request with contents"))
		return
	}
	if stream && r.URL.Query().Get("alt") != "sse" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "streamGenerateContent is served as server-sent events only; add ?alt=sse (the Gemini SDKs do)"))
		return
	}
	m, ts, snap, ok := g.target(w, p, model, "chat")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	req.Model, req.Stream = model, stream
	breq := req.ToBifrostResponsesRequest(bctx)
	if p = g.prepareResponses(w, r, p, m, ts, snap, breq, start); p == nil {
		return
	}
	if stream {
		ch, berr := g.BF.ResponsesStreamRequest(bctx, breq)
		if berr != nil {
			g.upstreamFailed(w, snap, p, m, ts, berr, start)
			return
		}
		state := gemini.NewBifrostToGeminiStreamState()
		g.pump(w, snap, p, m, ts, ch, start, func(c *schemas.BifrostStreamChunk) (string, any, schemas.ModelProvider, *usagePair) {
			ev := c.BifrostResponsesStreamResponse
			if ev == nil {
				return "", sseFrames{}, "", nil // always frames: Gemini streams end without [DONE]
			}
			var u *usagePair
			if ev.Response != nil && ev.Response.Usage != nil {
				u = &usagePair{ev.Response.Usage.InputTokens, ev.Response.Usage.OutputTokens}
			}
			out := sseFrames{}
			if gr := gemini.ToGeminiResponsesStreamResponse(ev, state); gr != nil {
				gr.ModelVersion = m.ID
				if data, err := sonic.Marshal(gr); err == nil {
					out = append(append(append(out, "data: "...), data...), "\n\n"...)
				}
			}
			return "", out, ev.ExtraFields.RoutingInfo.Provider, u
		})
		return
	}
	resp, berr := g.BF.ResponsesRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts, berr, start)
		return
	}
	l := store.RequestLog{Status: 200, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if resp.Usage != nil {
		l.InTokens, l.OutTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
	}
	l.Provider = g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, l.OutTokens)
	g.record(p, m, l, start)
	out := gemini.ToGeminiResponsesResponse(resp)
	out.ModelVersion = m.ID
	writeJSONHidden(w, l.Provider, out)
}

// genaiCountTokens counts with the serving provider; it is logged but not billed and does not
// feed provider health (it is not a generation).
func (g *Gateway) genaiCountTokens(w http.ResponseWriter, r *http.Request, p *principal, model string, body []byte, start time.Time) {
	var req gemini.GeminiCountTokensRequest
	if err := sonic.Unmarshal(body, &req); err != nil {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a countTokens request"))
		return
	}
	m, ts, snap, ok := g.target(w, p, model, "chat")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	req.Model = model
	breq := req.ToGeminiGenerationRequest().ToBifrostResponsesRequest(bctx)
	if len(breq.Input) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must have contents"))
		return
	}
	if p = g.prepareResponses(w, nil, p, m, ts, snap, breq, start); p == nil {
		return
	}
	resp, berr := g.BF.CountTokensRequest(bctx, breq)
	if berr != nil {
		ae := fromBifrost(berr, m.OrgID != "")
		g.record(p, m, store.RequestLog{Status: ae.status, Error: ae.msg}, start)
		writeError(w, ae)
		return
	}
	prov := snap.Providers[snap.byBifrost[resp.ExtraFields.RoutingInfo.Provider]].Name
	free := m
	free.InputPrice, free.OutputPrice, free.RequestPrice = 0, 0, 0 // counting is not billed
	g.record(p, free, store.RequestLog{Status: 200, Provider: prov, Upstream: resp.ExtraFields.RoutingInfo.Model}, start)
	writeJSONHidden(w, prov, gemini.ToGeminiCountTokensResponse(resp))
}

func (g *Gateway) genaiEmbed(w http.ResponseWriter, r *http.Request, p *principal, model string, batch bool, body []byte, start time.Time) {
	bctx := newBifrostCtx(r)
	var breq *schemas.BifrostEmbeddingRequest
	var err error
	if batch {
		var req gemini.GeminiBatchEmbeddingRequest
		if err = sonic.Unmarshal(body, &req); err == nil && len(req.Requests) > 0 {
			req.Model = model
			breq, err = req.ToBifrostEmbeddingRequest(bctx)
		}
	} else {
		var req gemini.GeminiEmbeddingRequest
		if err = sonic.Unmarshal(body, &req); err == nil && req.Content != nil {
			req.Model = model
			breq, err = req.ToBifrostEmbeddingRequest(bctx)
		}
	}
	if err != nil || breq == nil || len(breq.Input) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be an embedContent request with content (batchEmbedContents: requests)"))
		return
	}
	m, ts, snap, ok := g.target(w, p, model, "embedding")
	if !ok {
		return
	}
	var segs []*string
	for i := range breq.Input {
		for j := range breq.Input[i].Content {
			if breq.Input[i].Content[j].Text != nil {
				segs = append(segs, breq.Input[i].Content[j].Text)
			}
		}
	}
	if !g.guard(w, p, m, segs, start) {
		return
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.EmbeddingRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts, berr, start)
		return
	}
	l := store.RequestLog{Status: 200, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if resp.Usage != nil {
		l.InTokens = resp.Usage.PromptTokens
	}
	l.Provider = g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0)
	g.record(p, m, l, start)
	if batch {
		writeJSONHidden(w, l.Provider, gemini.ToGeminiEmbeddingResponse(resp))
		return
	}
	writeJSONHidden(w, l.Provider, gemini.ToGeminiEmbedContentResponse(resp))
}

func genaiModel(m store.Model) gemini.GeminiModel {
	methods := []string{"generateContent", "streamGenerateContent", "countTokens"}
	if m.Kind == "embedding" {
		methods = []string{"embedContent", "batchEmbedContents"}
	}
	return gemini.GeminiModel{Name: "models/" + m.ID, BaseModelID: m.ID, DisplayName: m.Name, Description: m.Description,
		InputTokenLimit: m.ContextLength, OutputTokenLimit: m.MaxOutput, SupportedGenerationMethods: methods}
}

// genaiVisible lists the chat and embedding models this key may use.
func (g *Gateway) genaiVisible(p *principal) []store.Model {
	return slices.DeleteFunc(g.snap.Load().Visible(p.Org.ID), func(m store.Model) bool {
		return (m.Kind != "chat" && m.Kind != "embedding") || (len(p.Key.Models) > 0 && !slices.Contains(p.Key.Models, m.ID))
	})
}

// GenAIModels serves GET /genai/v1beta/models in the Gemini list format (one page).
func (g *Gateway) GenAIModels(rw http.ResponseWriter, r *http.Request) {
	w := genaiWriter{rw}
	genaiAuth(r)
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	out := gemini.GeminiListModelsResponse{Models: []gemini.GeminiModel{}}
	for _, m := range g.genaiVisible(p) {
		out.Models = append(out.Models, genaiModel(m))
	}
	writeJSONHidden(w, "", out)
}

// GenAIModel serves GET /genai/v1beta/models/{rest...}.
func (g *Gateway) GenAIModel(rw http.ResponseWriter, r *http.Request) {
	w := genaiWriter{rw}
	genaiAuth(r)
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	id := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, m := range g.genaiVisible(p) {
		if m.ID == id {
			writeJSONHidden(w, "", genaiModel(m))
			return
		}
	}
	writeError(w, errf(404, "invalid_request_error", "model_not_found", "model %q not found", id))
}
