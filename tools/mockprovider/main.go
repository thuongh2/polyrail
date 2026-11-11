// Command mockprovider is a fixed-answer OpenAI-compatible endpoint for demos and load tests.
// It listens on 127.0.0.1:9901; register it as a provider of type openai with that base URL.
// It answers chat and text completions (streaming or not), the Responses API, embeddings, text to
// speech (a short WAV tone), transcriptions (a fixed sentence), images (1x1 PNGs), rerank, token
// counting (OpenAI and Anthropic paths) and OCR (Mistral path) without calling any real model.
// Chat answers (chat.go) are Markdown, JSON for response_format, or tool calls when tools are offered.
// Multi-turn chats report the earlier turns as prompt-cache reads (chatUsage).
// Files, batches and videos (OpenAI async paths) are served by package asyncmock.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/polyrail/polyrail/tools/mockprovider/asyncmock"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func sse(w http.ResponseWriter, event string, v any) {
	b, _ := json.Marshal(v)
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	w.(http.Flusher).Flush()
}

// tone returns a mono 16-bit 16 kHz WAV: a 440 Hz beep about as long as the text would take to say.
func tone(text string) []byte {
	const rate = 16000
	n := rate * min(8, 1+len(text)/15)
	var pcm bytes.Buffer
	for i := 0; i < n; i++ {
		v := int16(6000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		_ = binary.Write(&pcm, binary.LittleEndian, v)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+pcm.Len()))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(pcm.Len()))
	b.Write(pcm.Bytes())
	return b.Bytes()
}

var reWord = regexp.MustCompile(`[a-z0-9]+`)

// hashEmbed is a deterministic, similarity-meaningful embedding (docs/KB.md §9): a hashed bag of
// lowercase words (fnv32a % dims, sign from bit 31), L2-normalized. Returns the vector and the
// word count.
func hashEmbed(text string, dims int) ([]float64, int) {
	v := make([]float64, dims)
	words := reWord.FindAllString(strings.ToLower(text), -1)
	for _, w := range words {
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
	if n > 0 {
		for i := range v {
			v[i] /= math.Sqrt(n)
		}
	}
	return v, len(words)
}

// embeddings answers OpenAI /v1/embeddings with one hashEmbed vector per input item;
// size = "dimensions" (1..4096) else 64.
func embeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model      string `json:"model"`
		Input      any    `json:"input"`
		Dimensions int    `json:"dimensions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"invalid JSON body"}}`, http.StatusBadRequest)
		return
	}
	dims := req.Dimensions
	if dims <= 0 || dims > 4096 {
		dims = 64
	}
	var inputs []string
	switch in := req.Input.(type) {
	case string:
		inputs = []string{in}
	case []any:
		for _, it := range in {
			if s, ok := it.(string); ok {
				inputs = append(inputs, s)
			} else {
				inputs = append(inputs, fmt.Sprint(it)) // token arrays: hashed by their printed form
			}
		}
	default:
		http.Error(w, `{"error":{"message":"input must be a string or an array"}}`, http.StatusBadRequest)
		return
	}
	data := make([]any, len(inputs))
	tokens := 0
	for i, s := range inputs {
		v, n := hashEmbed(s, dims)
		tokens += n
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": v}
	}
	model := req.Model
	if model == "" {
		model = "mock"
	}
	writeJSON(w, map[string]any{"object": "list", "model": model, "data": data,
		"usage": map[string]int{"prompt_tokens": tokens, "total_tokens": tokens}})
}

// chatUsage is the deterministic usage of a chat answer: the last message plus 5 prompt tokens,
// and every earlier message counted as a prompt-cache read (as if a provider had cached the
// conversation prefix), so a multi-turn chat reports prompt_tokens_details.cached_tokens (D32).
func chatUsage(req chatReq, reply string) map[string]any {
	in, cached := 5, 0
	for i, m := range req.Messages {
		text, images := textOf(m.Content)
		n := len(text)/4 + 85*images // an image part counts like OpenAI's low-detail image
		if i < len(req.Messages)-1 {
			cached += n
		}
		in += n
	}
	out := len(reply)/4 + 1
	u := map[string]any{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out}
	if cached > 0 {
		u["prompt_tokens_details"] = map[string]int{"cached_tokens": cached}
	}
	return u
}

// keyFaults lets demos and e2e tests exercise key status (docs/KEYS.md): a key starting with
// "bad-" gets 401, one starting with "rl-" or "slow-" gets 429 with Retry-After: 5. Other keys
// (and requests without a key) are served normally.
func keyFaults(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = r.Header.Get("x-api-key")
		}
		switch {
		case strings.HasPrefix(key, "bad-"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
		case strings.HasPrefix(key, "rl-"), strings.HasPrefix(key, "slow-"):
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"Rate limit reached","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

func main() {
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { // lets "Discover models" work in the demo
		var data []any
		for _, id := range []string{"mock-chat", "mock-embed", "mock-tts", "mock-whisper", "mock-image", "mock-rerank"} {
			data = append(data, map[string]any{"id": id, "object": "model", "created": 1, "owned_by": "mock"})
		}
		writeJSON(w, map[string]any{"object": "list", "data": data})
	})
	http.HandleFunc("/v1/embeddings", embeddings)
	http.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input string }
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(tone(req.Input))
	})
	http.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, `{"error":{"message":"expected multipart form"}}`, http.StatusBadRequest)
			return
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, `{"error":{"message":"file is required"}}`, http.StatusBadRequest)
			return
		}
		f.Close()
		seconds := float64(hdr.Size) / 32000 // as if 16 kHz 16-bit mono
		writeJSON(w, map[string]any{"text": fmt.Sprintf("This is the mock transcript of %s.", hdr.Filename), "language": "english", "duration": seconds})
	})
	http.HandleFunc("/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt any  `json:"prompt"`
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		text := fmt.Sprintf(" (mock completion of: %v)", req.Prompt)
		usage := map[string]int{"prompt_tokens": 5, "completion_tokens": len(text) / 4, "total_tokens": 5 + len(text)/4}
		if !req.Stream {
			writeJSON(w, map[string]any{"id": "cmpl-1", "object": "text_completion", "created": 1, "model": "mock",
				"choices": []any{map[string]any{"index": 0, "text": text, "finish_reason": "stop"}}, "usage": usage})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, word := range strings.SplitAfter(text, " ") {
			sse(w, "", map[string]any{"id": "cmpl-1", "object": "text_completion", "created": 1, "model": "mock",
				"choices": []any{map[string]any{"index": 0, "text": word}}})
		}
		sse(w, "", map[string]any{"id": "cmpl-1", "object": "text_completion", "created": 1, "model": "mock",
			"choices": []any{map[string]any{"index": 0, "text": "", "finish_reason": "stop"}}, "usage": usage})
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	http.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input  any  `json:"input"`
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		text := fmt.Sprintf("This is the mock provider (Responses API). You said: %v", req.Input)
		resp := func(status string, out []any) map[string]any {
			return map[string]any{"id": "resp_1", "object": "response", "created_at": 1, "model": "mock", "status": status, "output": out,
				"usage": map[string]int{"input_tokens": 8, "output_tokens": len(text) / 4, "total_tokens": 8 + len(text)/4}}
		}
		msg := []any{map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}}
		if !req.Stream {
			writeJSON(w, resp("completed", msg))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		seq := 0
		ev := func(t string, v map[string]any) {
			v["type"], v["sequence_number"] = t, seq
			seq++
			sse(w, t, v)
		}
		item := map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
		part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
		ev("response.created", map[string]any{"response": resp("in_progress", []any{})})
		ev("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		ev("response.content_part.added", map[string]any{"item_id": "msg_1", "output_index": 0, "content_index": 0, "part": part})
		for _, word := range strings.SplitAfter(text, " ") {
			ev("response.output_text.delta", map[string]any{"item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": word})
		}
		ev("response.output_text.done", map[string]any{"item_id": "msg_1", "output_index": 0, "content_index": 0, "text": text})
		ev("response.content_part.done", map[string]any{"item_id": "msg_1", "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}}})
		ev("response.output_item.done", map[string]any{"output_index": 0, "item": msg[0]})
		ev("response.completed", map[string]any{"response": resp("completed", msg)})
	})
	// a 1x1 PNG for every requested image (n, default 1)
	images := func(w http.ResponseWriter, n int) {
		const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
		data := []any{}
		for i := 0; i < max(1, min(n, 10)); i++ {
			data = append(data, map[string]any{"b64_json": png})
		}
		writeJSON(w, map[string]any{"created": 1, "data": data, "usage": map[string]int{"input_tokens": 8, "output_tokens": 0, "total_tokens": 8}})
	}
	http.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ N int }
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		images(w, req.N)
	})
	for _, path := range []string{"/v1/images/edits", "/v1/images/variations"} {
		http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				http.Error(w, `{"error":{"message":"expected multipart form"}}`, http.StatusBadRequest)
				return
			}
			n, _ := strconv.Atoi(r.FormValue("n"))
			images(w, n)
		})
	}
	http.HandleFunc("/v1/rerank", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string `json:"query"`
			Documents []any  `json:"documents"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		results := []any{}
		for i := range req.Documents { // shorter documents rank higher; deterministic for demos
			results = append(results, map[string]any{"index": i, "relevance_score": 1 / float64(2+len(fmt.Sprint(req.Documents[i])))})
		}
		tokens := len(req.Query) / 4 * max(1, len(req.Documents))
		writeJSON(w, map[string]any{"id": "rerank-1", "results": results, "usage": map[string]int{"prompt_tokens": tokens, "total_tokens": tokens}})
	})
	countTokens := func(w http.ResponseWriter, r *http.Request) int {
		b, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		return len(b)/4 + 1
	}
	http.HandleFunc("/v1/responses/input_tokens", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"object": "response.input_tokens", "input_tokens": countTokens(w, r)})
	})
	http.HandleFunc("/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) { // Anthropic-type providers
		writeJSON(w, map[string]any{"input_tokens": countTokens(w, r)})
	})
	http.HandleFunc("/v1/ocr", func(w http.ResponseWriter, r *http.Request) { // Mistral-type providers
		writeJSON(w, map[string]any{"model": "mock", "pages": []any{map[string]any{"index": 0, "markdown": "# Mock OCR\n\nThis is the mock text of page 1."}},
			"usage_info": map[string]int{"pages_processed": 1, "doc_size_bytes": 0}})
	})
	http.HandleFunc("/", chat)               // chat completions (chat.go)
	asyncmock.Register(http.DefaultServeMux) // files, batches, videos
	if err := http.ListenAndServe("127.0.0.1:9901", keyFaults(http.DefaultServeMux)); err != nil {
		log.Fatalf("mockprovider: %v (is another mock already running on 9901?)", err)
	}
}
