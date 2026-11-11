// Package asyncmock serves the OpenAI async API paths the way Bifrost core's OpenAI provider calls
// them: /v1/files, /v1/batches and /v1/videos, held in memory. A batch is in_progress when created
// and completed on its first retrieve, with one chat completion result per input line (usage 10 in,
// 5 out). A video is completed at once and its content is a tiny MP4 header. Used by the mock
// provider and by the gateway tests.
package asyncmock

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// MP4 is the content of every mock video: an ftyp box (http.DetectContentType reports video/mp4).
var MP4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")

type file struct {
	obj     map[string]any
	content []byte
}

type mock struct {
	mu      sync.Mutex
	n       int
	files   map[string]*file
	batches map[string]map[string]any
	videos  map[string]map[string]any
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"error":{"message":"not found","type":"invalid_request_error"}}`)
}

// Register adds the async handlers to mux.
func Register(mux *http.ServeMux) {
	m := &mock{files: map[string]*file{}, batches: map[string]map[string]any{}, videos: map[string]map[string]any{}}
	mux.HandleFunc("POST /v1/files", m.upload)
	mux.HandleFunc("GET /v1/files", m.listFiles)
	mux.HandleFunc("GET /v1/files/{id}", m.withFile(func(w http.ResponseWriter, f *file) { writeJSON(w, f.obj) }))
	mux.HandleFunc("GET /v1/files/{id}/content", m.withFile(func(w http.ResponseWriter, f *file) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(f.content)
	}))
	mux.HandleFunc("DELETE /v1/files/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		_, ok := m.files[r.PathValue("id")]
		delete(m.files, r.PathValue("id"))
		m.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"id": r.PathValue("id"), "object": "file", "deleted": true})
	})
	mux.HandleFunc("POST /v1/batches", m.createBatch)
	mux.HandleFunc("GET /v1/batches", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		data := []any{}
		for _, b := range m.batches {
			data = append(data, b)
		}
		m.mu.Unlock()
		writeJSON(w, map[string]any{"object": "list", "data": data, "has_more": false})
	})
	mux.HandleFunc("GET /v1/batches/{id}", m.getBatch)
	mux.HandleFunc("POST /v1/batches/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		b, ok := m.batches[r.PathValue("id")]
		if !ok {
			notFound(w)
			return
		}
		if b["status"] == "in_progress" {
			b["status"], b["cancelled_at"] = "cancelled", time.Now().Unix()
		}
		writeJSON(w, b)
	})
	mux.HandleFunc("POST /v1/videos", m.createVideo)
	mux.HandleFunc("GET /v1/videos/{id}", m.withVideo(func(w http.ResponseWriter, v map[string]any) { writeJSON(w, v) }))
	mux.HandleFunc("GET /v1/videos/{id}/content", m.withVideo(func(w http.ResponseWriter, v map[string]any) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(MP4)
	}))
	mux.HandleFunc("DELETE /v1/videos/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		_, ok := m.videos[r.PathValue("id")]
		delete(m.videos, r.PathValue("id"))
		m.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"id": r.PathValue("id"), "object": "video.deleted", "deleted": true})
	})
}

func (m *mock) id(prefix string) string {
	m.n++
	return fmt.Sprintf("%s-%d", prefix, m.n)
}

func (m *mock) addFile(name, purpose string, content []byte) map[string]any {
	id := m.id("file")
	obj := map[string]any{"id": id, "object": "file", "bytes": len(content), "created_at": time.Now().Unix(),
		"filename": name, "purpose": purpose, "status": "processed"}
	m.files[id] = &file{obj: obj, content: content}
	return obj
}

func (m *mock) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, `{"error":{"message":"expected multipart form"}}`, http.StatusBadRequest)
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil || r.FormValue("purpose") == "" {
		http.Error(w, `{"error":{"message":"file and purpose are required"}}`, http.StatusBadRequest)
		return
	}
	content, _ := io.ReadAll(f)
	f.Close()
	m.mu.Lock()
	obj := m.addFile(hdr.Filename, r.FormValue("purpose"), content)
	m.mu.Unlock()
	writeJSON(w, obj)
}

func (m *mock) listFiles(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	data := []map[string]any{}
	for _, f := range m.files {
		data = append(data, f.obj)
	}
	m.mu.Unlock()
	sort.Slice(data, func(i, j int) bool { return fmt.Sprint(data[i]["id"]) < fmt.Sprint(data[j]["id"]) })
	writeJSON(w, map[string]any{"object": "list", "data": data, "has_more": false})
}

func (m *mock) withFile(fn func(http.ResponseWriter, *file)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		f, ok := m.files[r.PathValue("id")]
		m.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		fn(w, f)
	}
}

func (m *mock) createBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InputFileID      string            `json:"input_file_id"`
		Endpoint         string            `json:"endpoint"`
		CompletionWindow string            `json:"completion_window"`
		Metadata         map[string]string `json:"metadata"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, `{"error":{"message":"bad json"}}`, http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[req.InputFileID]; !ok {
		notFound(w)
		return
	}
	b := map[string]any{"id": m.id("batch"), "object": "batch", "endpoint": req.Endpoint, "input_file_id": req.InputFileID,
		"completion_window": req.CompletionWindow, "status": "in_progress", "created_at": time.Now().Unix(),
		"request_counts": map[string]int{"total": 0, "completed": 0, "failed": 0}, "metadata": req.Metadata}
	m.batches[b["id"].(string)] = b
	writeJSON(w, b)
}

// getBatch completes an in-progress batch: one result line per input line.
func (m *mock) getBatch(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[r.PathValue("id")]
	if !ok {
		notFound(w)
		return
	}
	if b["status"] == "in_progress" {
		var out bytes.Buffer
		n := 0
		sc := bufio.NewScanner(bytes.NewReader(m.files[b["input_file_id"].(string)].content))
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var item struct {
				CustomID string `json:"custom_id"`
				Body     struct {
					Model string `json:"model"`
				} `json:"body"`
			}
			if json.Unmarshal(sc.Bytes(), &item) != nil {
				continue
			}
			n++
			line, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("batch_req_%d", n), "custom_id": item.CustomID, "error": nil,
				"response": map[string]any{"status_code": 200, "request_id": fmt.Sprintf("req_%d", n), "body": map[string]any{
					"id": fmt.Sprintf("chatcmpl-%d", n), "object": "chat.completion", "created": 1, "model": item.Body.Model,
					"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "mock batch answer"}, "finish_reason": "stop"}},
					"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}}})
			out.Write(line)
			out.WriteByte('\n')
		}
		outFile := m.addFile("batch_output.jsonl", "batch_output", out.Bytes())
		now := time.Now().Unix()
		b["status"], b["output_file_id"], b["completed_at"] = "completed", outFile["id"], now
		b["request_counts"] = map[string]int{"total": n, "completed": n, "failed": 0}
	}
	writeJSON(w, b)
}

func (m *mock) createVideo(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil || r.FormValue("prompt") == "" {
		http.Error(w, `{"error":{"message":"multipart form with prompt required"}}`, http.StatusBadRequest)
		return
	}
	seconds := r.FormValue("seconds")
	if seconds == "" {
		seconds = "4"
	}
	size := r.FormValue("size")
	if size == "" {
		size = "720x1280"
	}
	now := time.Now().Unix()
	m.mu.Lock()
	v := map[string]any{"id": m.id("video"), "object": "video", "model": r.FormValue("model"), "status": "completed", "progress": 100,
		"created_at": now, "completed_at": now, "prompt": r.FormValue("prompt"), "seconds": seconds, "size": size}
	m.videos[v["id"].(string)] = v
	m.mu.Unlock()
	writeJSON(w, v)
}

func (m *mock) withVideo(fn func(http.ResponseWriter, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		v, ok := m.videos[r.PathValue("id")]
		m.mu.Unlock()
		if !ok {
			notFound(w)
			return
		}
		fn(w, v)
	}
}
