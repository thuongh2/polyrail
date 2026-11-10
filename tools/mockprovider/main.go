// Command mockprovider is a fixed-answer OpenAI-compatible endpoint for demos and load tests.
// It listens on 127.0.0.1:9901; register it as a provider of type openai with that base URL.
package main

import "net/http"

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`))
	})
	if err := http.ListenAndServe("127.0.0.1:9901", nil); err != nil {
		panic(err)
	}
}
