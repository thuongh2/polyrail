// Command mockprovider is a fixed-answer OpenAI-compatible endpoint for demos and load tests.
// It listens on 127.0.0.1:9901; register it as a provider of type openai with that base URL.
// It answers chat (streaming or not) and embeddings without calling any real model.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

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
		panic(err)
	}
}
