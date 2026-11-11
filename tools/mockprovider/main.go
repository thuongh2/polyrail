// Command mockprovider is a fixed-answer OpenAI-compatible endpoint for demos and load tests.
// It listens on 127.0.0.1:9901; register it as a provider of type openai with that base URL.
// It answers chat and text completions (streaming or not), the Responses API, embeddings, text to
// speech (a short WAV tone) and transcriptions (a fixed sentence) without calling any real model.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
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

type chatReq struct {
	Stream   bool `json:"stream"`
	Messages []struct {
		Content any `json:"content"`
	} `json:"messages"`
}

func main() {
	http.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"mock","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
	})
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
		ev("response.created", map[string]any{"response": resp("in_progress", []any{})})
		for _, word := range strings.SplitAfter(text, " ") {
			ev("response.output_text.delta", map[string]any{"item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": word})
		}
		ev("response.completed", map[string]any{"response": resp("completed", msg)})
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
		last := ""
		if n := len(req.Messages); n > 0 {
			last = fmt.Sprint(req.Messages[n-1].Content)
		}
		reply := "This is the mock provider. You said: " + last
		in, out := len(last)/4+5, len(reply)/4+1
		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "mock-1", "object": "chat.completion", "created": 1, "model": "mock",
				"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out},
			})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(v map[string]any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		for _, word := range strings.SplitAfter(reply, " ") {
			chunk(map[string]any{"id": "mock-1", "object": "chat.completion.chunk", "created": 1, "model": "mock",
				"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": word}}}})
		}
		chunk(map[string]any{"id": "mock-1", "object": "chat.completion.chunk", "created": 1, "model": "mock",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	if err := http.ListenAndServe("127.0.0.1:9901", nil); err != nil {
		log.Fatalf("mockprovider: %v (is another mock already running on 9901?)", err)
	}
}
