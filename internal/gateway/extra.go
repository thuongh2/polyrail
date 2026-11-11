package gateway

// Audio (text to speech, speech to text), legacy text completions and the Responses API.
// Same pipeline as chat: authenticate → resolve model → admit → guardrails → Bifrost → settle/record.

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/sjson"
)

// target resolves and admits a request for model id of the given kind.
func (g *Gateway) target(w http.ResponseWriter, p *principal, id, kind string) (store.Model, []store.Target, *Snapshot, bool) {
	m, ts, e := g.resolve(p, id, kind)
	if e == nil {
		e = g.admit(context.Background(), p)
	}
	if e != nil {
		writeError(w, e)
		return m, nil, nil, false
	}
	return m, ts, g.snap.Load(), true
}

// guard applies the org's guardrails to segs; false means the response was already written.
func (g *Gateway) guard(w http.ResponseWriter, p *principal, m store.Model, segs []*string, start time.Time) bool {
	reason, err := applyGuardrails(p.Org, segs)
	if err != nil {
		writeError(w, errf(500, "api_error", "guardrail_config", "%v", err))
		return false
	}
	if reason != "" {
		g.record(p, m, store.RequestLog{Status: 400, Error: "guardrail: " + reason}, start)
		writeError(w, errf(400, "invalid_request_error", "guardrail_blocked", "%s", reason))
		return false
	}
	return true
}

// upstreamFailed settles, records and writes a Bifrost error.
func (g *Gateway) upstreamFailed(w http.ResponseWriter, snap *Snapshot, p *principal, m store.Model, ts []store.Target, berr *schemas.BifrostError, start time.Time) {
	ae := fromBifrost(berr, m.OrgID != "")
	g.settle(snap, m, ts, "", targetFault(ae.status), start, 0)
	g.record(p, m, store.RequestLog{Status: ae.status, Error: ae.msg}, start)
	writeError(w, ae)
}

// hideExtra encodes a response without Bifrost's internal extra_fields (routing, keys, latency).
// Deleting after encoding also covers core types with their own MarshalJSON, which an embedding
// wrapper cannot override.
func hideExtra(v any) ([]byte, error) {
	out, err := sonic.Marshal(v)
	if err != nil {
		return nil, err
	}
	return sjson.DeleteBytes(out, "extra_fields")
}

func writeJSONHidden(w http.ResponseWriter, provName string, v any) {
	out, err := hideExtra(v)
	if err != nil {
		writeError(w, errf(500, "api_error", "encode", "cannot encode response"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-gateway-provider", provName)
	_, _ = w.Write(out)
}

// ---- text to speech: POST /v1/audio/speech ----

var audioTypes = map[string]string{"mp3": "audio/mpeg", "opus": "audio/ogg", "aac": "audio/aac", "flac": "audio/flac", "wav": "audio/wav", "pcm": "audio/L16"}

func (g *Gateway) Speech(w http.ResponseWriter, r *http.Request) {
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
	var req openai.OpenAISpeechRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || req.Input == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a speech request with model, input and voice"))
		return
	}
	if req.StreamFormat != nil && *req.StreamFormat == "sse" {
		writeError(w, errf(400, "invalid_request_error", "unsupported", "stream_format sse is not supported; omit it to receive the audio file"))
		return
	}
	format := req.ResponseFormat
	if format == "" {
		format = "mp3"
	}
	ctype, ok := audioTypes[format]
	if !ok {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "response_format must be one of mp3, opus, aac, flac, wav, pcm"))
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "speech")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostSpeechRequest(bctx)
	if !g.guard(w, p, m, []*string{&breq.Input.Input}, start) {
		return
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.SpeechRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts, berr, start)
		return
	}
	provName := g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0)
	l := store.RequestLog{Status: 200, Provider: provName, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if resp.Usage != nil && resp.Usage.InputTokens > 0 {
		l.InTokens, l.OutTokens = resp.Usage.InputTokens, resp.Usage.OutputTokens
	} else {
		l.InTokens = utf8.RuneCountInString(breq.Input.Input) // priced per character when no token usage
	}
	g.record(p, m, l, start)
	if sniffed := http.DetectContentType(resp.Audio); strings.HasPrefix(sniffed, "audio/") {
		ctype = sniffed // trust the bytes over the requested format (some providers ignore it)
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("x-gateway-provider", provName)
	_, _ = w.Write(resp.Audio)
}

// ---- speech to text: POST /v1/audio/transcriptions (multipart) ----

func (g *Gateway) Transcriptions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxBodyBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil { // parts over 8 MB spill to temp files
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be multipart/form-data with file and model (max %d bytes)", g.cfg.MaxBodyBytes))
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil || r.FormValue("model") == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "form fields file and model are required"))
		return
	}
	audio, err := io.ReadAll(f)
	f.Close()
	if err != nil || len(audio) == 0 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "cannot read the audio file"))
		return
	}
	m, ts, snap, ok := g.target(w, p, r.FormValue("model"), "transcription")
	if !ok {
		return
	}
	req := openai.OpenAITranscriptionRequest{Model: r.FormValue("model"), File: audio}
	set := func(dst **string, key string) {
		if v := r.FormValue(key); v != "" {
			*dst = &v
		}
	}
	set(&req.Language, "language")
	set(&req.Prompt, "prompt")
	set(&req.ResponseFormat, "response_format")
	if ext := strings.TrimPrefix(path.Ext(hdr.Filename), "."); ext != "" {
		req.Format = &ext // some providers (Gemini) need the container format
	}
	if req.Prompt != nil && !g.guard(w, p, m, []*string{req.Prompt}, start) {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostTranscriptionRequest(bctx)
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	resp, berr := g.BF.TranscriptionRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts, berr, start)
		return
	}
	provName := g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, 0)
	l := store.RequestLog{Status: 200, Provider: provName, Upstream: resp.ExtraFields.RoutingInfo.Model}
	l.InTokens, l.OutTokens, l.Cost = transcriptionUsage(m, resp)
	g.record(p, m, l, start)
	if req.ResponseFormat != nil && (*req.ResponseFormat == "text" || *req.ResponseFormat == "srt" || *req.ResponseFormat == "vtt") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("x-gateway-provider", provName)
		_, _ = io.WriteString(w, resp.Text)
		return
	}
	writeJSONHidden(w, provName, resp)
}

// transcriptionUsage returns tokens and, for duration-priced models, the cost in micro-USD.
func transcriptionUsage(m store.Model, resp *schemas.BifrostTranscriptionResponse) (in, out int, costMicros int64) {
	var seconds float64
	if u := resp.Usage; u != nil {
		if u.InputTokens != nil {
			in = *u.InputTokens
		}
		if u.OutputTokens != nil {
			out = *u.OutputTokens
		}
		if u.Seconds != nil {
			seconds = float64(*u.Seconds)
		}
	}
	if seconds == 0 && resp.Duration != nil {
		seconds = *resp.Duration
	}
	if m.AudioMinutePrice > 0 && seconds > 0 { // 0 lets record() price by tokens
		costMicros = int64(math.Round(seconds/60*float64(m.AudioMinutePrice))) + cost(m, in, out)
	}
	return in, out, costMicros
}

// ---- legacy text completions: POST /v1/completions (chat-kind models) ----

func (g *Gateway) Completions(w http.ResponseWriter, r *http.Request) {
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
	var req openai.OpenAITextCompletionRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" || req.Prompt == nil {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a completion request with model and prompt"))
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "chat")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostTextCompletionRequest(bctx)
	var segs []*string
	if in := breq.Input; in != nil {
		if in.PromptStr != nil {
			segs = append(segs, in.PromptStr)
		}
		for i := range in.PromptArray {
			segs = append(segs, &in.PromptArray[i])
		}
	}
	if !g.guard(w, p, m, segs, start) {
		return
	}
	if breq.Params == nil {
		breq.Params = &schemas.TextCompletionParameters{}
	}
	if c := p.Org.MaxTokensCap; c > 0 && (breq.Params.MaxTokens == nil || *breq.Params.MaxTokens > c) {
		breq.Params.MaxTokens = &c
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	if req.Stream != nil && *req.Stream {
		ch, berr := g.BF.TextCompletionStreamRequest(bctx, breq)
		if berr != nil {
			g.upstreamFailed(w, snap, p, m, ts, berr, start)
			return
		}
		g.pump(w, snap, p, m, ts, ch, start, func(c *schemas.BifrostStreamChunk) (string, any, schemas.ModelProvider, *usagePair) {
			t := c.BifrostTextCompletionResponse
			if t == nil {
				return "", nil, "", nil
			}
			t.Model = m.ID
			var u *usagePair
			if t.Usage != nil {
				u = &usagePair{t.Usage.PromptTokens, t.Usage.CompletionTokens}
			}
			return "", t, t.ExtraFields.RoutingInfo.Provider, u
		})
		return
	}
	resp, berr := g.BF.TextCompletionRequest(bctx, breq)
	if berr != nil {
		g.upstreamFailed(w, snap, p, m, ts, berr, start)
		return
	}
	l := store.RequestLog{Status: 200, Upstream: resp.ExtraFields.RoutingInfo.Model}
	if resp.Usage != nil {
		l.InTokens, l.OutTokens = resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	l.Provider = g.settle(snap, m, ts, resp.ExtraFields.RoutingInfo.Provider, false, start, l.OutTokens)
	setCost(w, g.record(p, m, l, start))
	resp.Model = m.ID
	writeJSONHidden(w, l.Provider, resp)
}

// ---- Responses API: POST /v1/responses (chat-kind models) ----

func (g *Gateway) Responses(w http.ResponseWriter, r *http.Request) {
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
	var req openai.OpenAIResponsesRequest
	if err := sonic.Unmarshal(body, &req); err != nil || req.Model == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a responses request with model and input"))
		return
	}
	m, ts, snap, ok := g.target(w, p, req.Model, "chat")
	if !ok {
		return
	}
	bctx := newBifrostCtx(r)
	breq := req.ToBifrostResponsesRequest(bctx)
	if !g.prepareResponses(w, p, m, ts, snap, breq, start) {
		return
	}
	if req.Stream != nil && *req.Stream {
		ch, berr := g.BF.ResponsesStreamRequest(bctx, breq)
		if berr != nil {
			g.upstreamFailed(w, snap, p, m, ts, berr, start)
			return
		}
		g.pump(w, snap, p, m, ts, ch, start, func(c *schemas.BifrostStreamChunk) (string, any, schemas.ModelProvider, *usagePair) {
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
			return string(ev.Type), ev, ev.ExtraFields.RoutingInfo.Provider, u
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
	setCost(w, g.record(p, m, l, start))
	resp.Model = m.ID
	writeJSONHidden(w, l.Provider, resp)
}

// prepareResponses applies guardrails, the org's max-token cap and the route plan to a Responses
// request; false means the error response was already written.
func (g *Gateway) prepareResponses(w http.ResponseWriter, p *principal, m store.Model, ts []store.Target, snap *Snapshot, breq *schemas.BifrostResponsesRequest, start time.Time) bool {
	var segs []*string
	for i := range breq.Input {
		if c := breq.Input[i].Content; c != nil {
			if c.ContentStr != nil {
				segs = append(segs, c.ContentStr)
			}
			for j := range c.ContentBlocks {
				if c.ContentBlocks[j].Text != nil {
					segs = append(segs, c.ContentBlocks[j].Text)
				}
			}
		}
	}
	if !g.guard(w, p, m, segs, start) {
		return false
	}
	if breq.Params == nil {
		breq.Params = &schemas.ResponsesParameters{}
	}
	if c := p.Org.MaxTokensCap; c > 0 && (breq.Params.MaxOutputTokens == nil || *breq.Params.MaxOutputTokens > c) {
		breq.Params.MaxOutputTokens = &c
	}
	breq.Provider, breq.Model, breq.Fallbacks = bifrostTargets(snap, ts)
	return true
}

// ---- shared SSE pump ----

// sseFrames is a pump payload that is already framed as one or more server-sent events.
type sseFrames []byte

type usagePair struct{ in, out int }

// pump writes a Bifrost stream as server-sent events. conv maps one chunk to (event name or "",
// payload, serving provider, usage if present); a nil payload skips the chunk.
func (g *Gateway) pump(w http.ResponseWriter, snap *Snapshot, p *principal, m store.Model, ts []store.Target, ch chan *schemas.BifrostStreamChunk, start time.Time,
	conv func(*schemas.BifrostStreamChunk) (string, any, schemas.ModelProvider, *usagePair)) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	l := store.RequestLog{Status: 200, Stream: true}
	var served schemas.ModelProvider
	gone, named := false, false
	for c := range ch { // always drain: Bifrost owns the channel
		if gone {
			continue
		}
		var event string
		var payload any
		if c.BifrostError != nil {
			ae := fromBifrost(c.BifrostError, m.OrgID != "")
			l.Status, l.Error = ae.status, ae.msg
			event, payload = errorBody(w, ae)
		} else {
			var prov schemas.ModelProvider
			var u *usagePair
			event, payload, prov, u = conv(c)
			if payload == nil {
				continue
			}
			if served == "" && prov != "" {
				w.Header().Set("x-gateway-provider", snap.Providers[snap.byBifrost[prov]].Name)
			}
			if prov != "" {
				served = prov
			}
			if u != nil {
				l.InTokens, l.OutTokens = u.in, u.out
			}
		}
		frame, ok := payload.(sseFrames)
		if ok {
			named = true
		} else {
			data, err := hideExtra(payload)
			if err != nil {
				continue
			}
			if event != "" {
				frame, named = fmt.Appendf(nil, "event: %s\n", event), true
			}
			frame = fmt.Appendf(frame, "data: %s\n\n", data)
		}
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := w.Write(frame); err != nil || rc.Flush() != nil {
			gone = true
			l.Status, l.Error = 499, "client closed connection"
		}
	}
	if !gone && !named { // Responses API streams end with response.completed, not [DONE]
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		_ = rc.Flush()
	}
	l.Provider = g.settle(snap, m, ts, served, targetFault(l.Status), start, l.OutTokens)
	g.record(p, m, l, start)
}
