package broadcast

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/polyrail/polyrail/internal/gateway"
)

// OTLP/HTTP JSON encoding (opentelemetry-proto, JSON Protobuf mapping): trace and span ids are
// hex, 64-bit integers are strings, enums are numbers. Attribute names follow the OpenTelemetry
// GenAI semantic conventions where one exists, polyrail.* otherwise.

const (
	spanKindClient = 3
	statusOK       = 1
	statusError    = 2
	contentMax     = 16 << 10 // per content attribute; longer text is cut at a rune boundary
)

type anyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type spanStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpSpan struct {
	TraceID           string     `json:"traceId"`
	SpanID            string     `json:"spanId"`
	Name              string     `json:"name"`
	Kind              int        `json:"kind"`
	StartTimeUnixNano string     `json:"startTimeUnixNano"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano"`
	Attributes        []keyValue `json:"attributes"`
	Status            spanStatus `json:"status"`
}

type attrs []keyValue

func (a *attrs) str(k, v string) {
	if v != "" {
		*a = append(*a, keyValue{k, anyValue{StringValue: &v}})
	}
}

func (a *attrs) int(k string, v int64) {
	s := strconv.FormatInt(v, 10)
	*a = append(*a, keyValue{k, anyValue{IntValue: &s}})
}

func (a *attrs) double(k string, v float64) { *a = append(*a, keyValue{k, anyValue{DoubleValue: &v}}) }

func (a *attrs) flag(k string, v bool) {
	if v {
		*a = append(*a, keyValue{k, anyValue{BoolValue: &v}})
	}
}

// operation is the gen_ai.operation.name of a model kind.
func operation(kind string) string {
	switch kind {
	case "", "chat":
		return "chat"
	case "embedding":
		return "embeddings"
	}
	return kind
}

// cut keeps at most contentMax bytes of s, ending on a rune boundary.
func cut(s string) string {
	if len(s) <= contentMax {
		return s
	}
	n := contentMax
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…[truncated]"
}

// spanOf encodes one request as an OTLP span. The trace id is derived from the request log id and
// the span id is the log id itself, so a span links back to its request log row. Content (the
// request and response bodies) is added only when withContent is set and the gateway captured it.
func spanOf(tr gateway.Trace, withContent bool) json.RawMessage {
	l := tr.Log
	h := sha256.Sum256([]byte("polyrail-trace:" + l.ID))
	spanID := l.ID
	if len(spanID) != 16 {
		spanID = hex.EncodeToString(h[16:24])
	}
	op := operation(tr.Kind)
	var a attrs
	a.str("gen_ai.operation.name", op)
	a.str("gen_ai.system", tr.System)
	a.str("gen_ai.provider.name", tr.System)
	a.str("gen_ai.request.model", l.Model)
	resp := l.Upstream
	if resp == "" {
		resp = l.Model
	}
	a.str("gen_ai.response.model", resp)
	a.int("gen_ai.usage.input_tokens", int64(l.InTokens))
	a.int("gen_ai.usage.output_tokens", int64(l.OutTokens))
	a.double("gen_ai.usage.cost", float64(l.Cost)/1e6)
	a.int("http.response.status_code", int64(l.Status))
	a.str("user.id", l.EndUser)
	a.str("polyrail.request_id", l.ID)
	a.str("polyrail.org_id", l.OrgID)
	a.str("polyrail.key_id", l.KeyID)
	a.str("polyrail.end_user", l.EndUser)
	a.str("polyrail.provider", l.Provider)
	a.int("polyrail.cost_micros", l.Cost)
	a.int("polyrail.latency_ms", l.LatencyMs)
	a.flag("polyrail.cached", l.Cached)
	a.flag("polyrail.stream", l.Stream)
	st := spanStatus{Code: statusOK}
	if l.Status >= 400 {
		msg := l.Error
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(l.Status)
		}
		st = spanStatus{Code: statusError, Message: cut(msg)}
		a.str("error.type", strconv.Itoa(l.Status))
	}
	if withContent {
		a.str("gen_ai.prompt", cut(tr.Request))
		a.str("gen_ai.completion", cut(tr.Response))
	}
	end := l.At.Add(time.Duration(l.LatencyMs) * time.Millisecond)
	b, _ := json.Marshal(otlpSpan{
		TraceID: hex.EncodeToString(h[:16]), SpanID: spanID, Name: strings.TrimSpace(op + " " + l.Model), Kind: spanKindClient,
		StartTimeUnixNano: strconv.FormatInt(l.At.UnixNano(), 10), EndTimeUnixNano: strconv.FormatInt(end.UnixNano(), 10),
		Attributes: a, Status: st,
	})
	return b
}

// payload wraps spans in one OTLP ExportTraceServiceRequest.
func payload(spans []json.RawMessage) []byte {
	var b strings.Builder
	b.WriteString(`{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"polyrail"}}]},` +
		`"scopeSpans":[{"scope":{"name":"polyrail"},"spans":[`)
	for i, s := range spans {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(s)
	}
	b.WriteString(`]}]}]}`)
	return []byte(b.String())
}
