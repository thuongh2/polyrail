package gateway

// Anthropic SDK drop-in: POST /anthropic/v1/messages and GET /anthropic/v1/models.
// A Messages request is converted to a Responses request with Bifrost core's Anthropic converters,
// runs the same pipeline as /v1/responses (any chat-kind model, any provider), and the reply,
// stream events and errors are converted back to the Anthropic format.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// anthropicWriter marks a response that speaks the Anthropic format; writeError and pump use it
// to shape errors. Unwrap keeps http.ResponseController (flush, write deadlines) working.
type anthropicWriter struct{ http.ResponseWriter }

func (w anthropicWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var anthropicErrTypes = map[int]string{400: "invalid_request_error", 401: "authentication_error", 402: "billing_error",
	403: "permission_error", 404: "not_found_error", 413: "request_too_large", 429: "rate_limit_error", 529: "overloaded_error"}

func anthropicError(e *apiError) anthropic.AnthropicMessageError {
	typ, ok := anthropicErrTypes[e.status]
	if !ok {
		typ = "api_error"
	}
	return anthropic.AnthropicMessageError{Type: "error", Error: anthropic.AnthropicMessageErrorStruct{Type: typ, Message: e.msg}}
}

func (g *Gateway) AnthropicMessages(rw http.ResponseWriter, r *http.Request) {
	w := anthropicWriter{rw}
	start := time.Now()
	p, e := g.authenticate(r.Context(), r) // SDKs send x-api-key; anthropic-version is ignored
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
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || req.MaxTokens <= 0 || len(req.Messages) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a messages request with model, max_tokens and messages"))
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "chat")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostResponsesRequest(bctx)
	if p = g.prepareResponses(w, r, p, m, ts, snap, breq, start); p == nil {
		return
	}
	if req.Stream != nil && *req.Stream {
		ch, berr := g.BF.ResponsesStreamRequest(bctx, breq)
		if berr != nil {
			g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
			return
		}
		g.pump(w, bctx, snap, p, m, ts, ch, start, func(c *schemas.BifrostStreamChunk) (string, any, schemas.ModelProvider, *usagePair) {
			ev := c.BifrostResponsesStreamResponse
			if ev == nil {
				return "", nil, "", nil
			}
			var u *usagePair
			if ev.Response != nil {
				ev.Response.Model = m.ID
				if ev.Response.Usage != nil {
					u = &usagePair{ev.Response.Usage.InputTokens, ev.Response.Usage.OutputTokens}
				}
			}
			var frames sseFrames
			for _, a := range anthropic.ToAnthropicResponsesStreamResponse(bctx, ev) {
				if data, err := sonic.Marshal(a); err == nil {
					frames = fmt.Appendf(frames, "event: %s\ndata: %s\n\n", a.Type, data)
				}
			}
			if len(frames) == 0 { // OpenAI lifecycle events with no Anthropic counterpart
				return "", nil, "", nil
			}
			return "", frames, ev.ExtraFields.RoutingInfo.Provider, u
		})
		return
	}
	resp, berr := g.BF.ResponsesRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, bctx, snap, p, m, ts, berr, start)
		return
	}
	l := store.RequestLog{Status: 200, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if resp.Usage != nil {
		l.InTokens, l.OutTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
	}
	l.Provider = g.settle(bctx, snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, l.InTokens, l.OutTokens)
	g.record(p, m, l, start)
	resp.Model = m.ID
	writeJSONHidden(w, l.Provider, anthropic.ToAnthropicResponsesResponse(bctx, resp))
}

// AnthropicModels lists the chat models this key may use in the Anthropic list format.
func (g *Gateway) AnthropicModels(rw http.ResponseWriter, r *http.Request) {
	w := anthropicWriter{rw}
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	out := anthropic.AnthropicListModelsResponse{Data: []anthropic.AnthropicModel{}}
	for _, m := range g.snap.Load().Visible(p.Org.ID) {
		if m.Kind == "chat" && (len(p.Key.Models) == 0 || slices.Contains(p.Key.Models, m.ID)) {
			out.Data = append(out.Data, anthropic.AnthropicModel{ID: m.ID, Type: "model", DisplayName: m.Name, CreatedAt: m.CreatedAt.UTC()})
		}
	}
	if n := len(out.Data); n > 0 {
		out.FirstID, out.LastID = &out.Data[0].ID, &out.Data[n-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
