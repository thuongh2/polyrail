package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type chatMsg struct {
	Role       string `json:"role"`
	Content    any    `json:"content"`
	ToolCallID string `json:"tool_call_id"`
}

type chatTool struct {
	Function struct {
		Name       string         `json:"name"`
		Parameters map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatReq struct {
	Stream         bool       `json:"stream"`
	Messages       []chatMsg  `json:"messages"`
	Tools          []chatTool `json:"tools"`
	ToolChoice     any        `json:"tool_choice"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string         `json:"name"`
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

// textOf returns a message's text and how many image parts it carries (content string or parts).
func textOf(content any) (string, int) {
	parts, ok := content.([]any)
	if !ok {
		if s, ok := content.(string); ok {
			return s, 0
		}
		return fmt.Sprint(content), 0
	}
	var b strings.Builder
	images := 0
	for _, p := range parts {
		m, _ := p.(map[string]any)
		switch m["type"] {
		case "text":
			s, _ := m["text"].(string)
			b.WriteString(s)
		case "image_url", "input_image":
			images++
		}
	}
	return b.String(), images
}

// example builds a value matching a JSON schema's type/properties/items/enum (enough for demos).
func example(s map[string]any, key string) any {
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		return e[0]
	}
	t := s["type"]
	if ts, ok := t.([]any); ok && len(ts) > 0 {
		t = ts[0]
	}
	switch t {
	case "object":
		out := map[string]any{}
		props, _ := s["properties"].(map[string]any)
		for k, v := range props {
			sub, _ := v.(map[string]any)
			out[k] = example(sub, k)
		}
		return out
	case "array":
		sub, _ := s["items"].(map[string]any)
		return []any{example(sub, key)}
	case "integer":
		return 42
	case "number":
		return 3.5
	case "boolean":
		return true
	case "null":
		return nil
	}
	if key == "" {
		return "mock"
	}
	return "mock " + key
}

// chatReply is the mock answer: tool calls when tools are offered (and the last message is not a
// tool result), JSON for response_format, else a Markdown reply that exercises headings, lists,
// a code block, a link and a [1] citation, mentioning any attached images.
func chatReply(req chatReq) (string, []any) {
	var last chatMsg
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1]
	}
	text, images := textOf(last.Content)
	if last.Role == "tool" {
		return "The tool returned:\n\n```json\n" + text + "\n```\n\n**Summary:** the mock provider read the tool result above.", nil
	}
	if choice, _ := req.ToolChoice.(string); len(req.Tools) > 0 && choice != "none" {
		tool := req.Tools[0]
		if c, ok := req.ToolChoice.(map[string]any); ok { // {"type":"function","function":{"name":…}}
			fn, _ := c["function"].(map[string]any)
			for _, t := range req.Tools {
				if t.Function.Name == fn["name"] {
					tool = t
				}
			}
		}
		args, _ := json.Marshal(example(tool.Function.Parameters, ""))
		if tool.Function.Parameters == nil {
			args = []byte("{}")
		}
		return "", []any{map[string]any{"id": "call_mock_1", "type": "function",
			"function": map[string]any{"name": tool.Function.Name, "arguments": string(args)}}}
	}
	switch req.ResponseFormat.Type {
	case "json_schema":
		b, _ := json.Marshal(example(req.ResponseFormat.JSONSchema.Schema, ""))
		return string(b), nil
	case "json_object":
		b, _ := json.Marshal(map[string]any{"answer": "This is the mock provider.", "you_said": text})
		return string(b), nil
	}
	seen := ""
	if images > 0 {
		seen = fmt.Sprintf("I received **%d image(s)**; the mock provider cannot look at them, but the parts arrived intact.\n\n", images)
	}
	return "#### Mock answer\n\n" + seen + "This is the mock provider. You said: " + text + "\n\n" +
		"*   **Lists:** nested items work\n    1.  first\n    2.  second\n*   **Code:** see below [1]\n\n" +
		"```go\nfmt.Println(\"hello from the mock\")\n```\n\n" +
		"| Field | Value |\n|---|---|\n| model | mock |\n\n" +
		"Read more in the [OpenAI API reference](https://platform.openai.com/docs/api-reference/chat).", nil
}

func chat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req) // images arrive as data URLs
	reply, calls := chatReply(req)
	finish := "stop"
	if calls != nil {
		finish = "tool_calls"
	}
	usage := chatUsage(req, reply)
	if !req.Stream {
		msg := map[string]any{"role": "assistant", "content": reply}
		if calls != nil {
			msg["content"], msg["tool_calls"] = nil, calls
		}
		writeJSON(w, map[string]any{
			"id": "mock-1", "object": "chat.completion", "created": 1, "model": "mock",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   usage,
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta map[string]any, extra map[string]any) {
		c := map[string]any{"index": 0, "delta": delta}
		v := map[string]any{"id": "mock-1", "object": "chat.completion.chunk", "created": 1, "model": "mock", "choices": []any{c}}
		for k, x := range extra {
			if k == "finish_reason" {
				c[k] = x
			} else {
				v[k] = x
			}
		}
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
	}
	for i, call := range calls { // the name first, then the arguments in two pieces, like OpenAI
		c := call.(map[string]any)
		fn := c["function"].(map[string]any)
		args := fn["arguments"].(string)
		chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": i, "id": c["id"], "type": "function",
			"function": map[string]any{"name": fn["name"], "arguments": ""}}}}, nil)
		half := len(args) / 2
		for _, piece := range []string{args[:half], args[half:]} {
			chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": piece}}}}, nil)
		}
	}
	for _, word := range strings.SplitAfter(reply, " ") {
		if word != "" {
			chunk(map[string]any{"content": word}, nil)
		}
	}
	chunk(map[string]any{}, map[string]any{"finish_reason": finish, "usage": usage})
	fmt.Fprint(w, "data: [DONE]\n\n")
}
