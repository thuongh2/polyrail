package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
	"github.com/polyrail/polyrail/internal/usage"
)

// extraUpstream answers the audio, completions and responses endpoints like OpenAI does.
func extraUpstream(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/speech":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\x3e\x00\x00\x00\x7d\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00"))
		case "/v1/audio/transcriptions":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("model") != "whisper-up" {
				http.Error(w, `{"error":{"message":"bad form"}}`, 400)
				return
			}
			_, hdr, _ := r.FormFile("file")
			fmt.Fprintf(w, `{"text":"heard %s","duration":90}`, hdr.Filename)
		case "/v1/completions":
			fmt.Fprint(w, `{"id":"c","object":"text_completion","created":1,"model":"up","choices":[{"index":0,"text":" world","finish_reason":"stop"}],"usage":{"prompt_tokens":1000000,"completion_tokens":0,"total_tokens":1000000}}`)
		case "/v1/responses":
			fmt.Fprint(w, `{"id":"r","object":"response","created_at":1,"model":"up","status":"completed","output":[{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi there","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAudioCompletionsResponses(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	up := extraUpstream(t)
	p := &store.Provider{Name: "audio", Type: "openai", BaseURL: up.URL, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
	if err := f.g.S.PutProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*store.Model{
		{ID: "test/tts", Kind: "speech", Enabled: true, InputPrice: 15_000_000, Targets: []store.Target{{ProviderID: p.ID, Model: "tts-up"}}},
		{ID: "test/stt", Kind: "transcription", Enabled: true, AudioMinutePrice: 6_000, Targets: []store.Target{{ProviderID: p.ID, Model: "whisper-up"}}},
		{ID: "test/text", Kind: "chat", Enabled: true, InputPrice: 2_000_000, Targets: []store.Target{{ProviderID: p.ID, Model: "up"}}},
	} {
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/audio/speech", f.g.Speech)
	mux.HandleFunc("POST /v1/audio/transcriptions", f.g.Transcriptions)
	mux.HandleFunc("POST /v1/completions", f.g.Completions)
	mux.HandleFunc("POST /v1/responses", f.g.Responses)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f.srv = srv

	// text to speech: audio bytes, sniffed content type, priced per character (10 chars at $15/1M)
	r := f.post(t, "/v1/audio/speech", f.key, `{"model":"test/tts","input":"0123456789","voice":"alloy"}`)
	audio, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "audio/") || !bytes.HasPrefix(audio, []byte("RIFF")) {
		t.Fatalf("speech: %d %s %q", r.StatusCode, r.Header.Get("Content-Type"), audio[:min(8, len(audio))])
	}
	if r := f.post(t, "/v1/audio/speech", f.key, `{"model":"test/text","input":"x","voice":"alloy"}`); errCode(readJSON(t, r)) != "wrong_model_kind" {
		t.Fatal("speech on a chat model must be rejected")
	}

	// speech to text: multipart in, JSON out, priced by audio minute (90 s at $0.006/min = 9000 micros)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", "test/stt")
	fw, _ := mw.CreateFormFile("file", "meeting.mp3")
	_, _ = fw.Write([]byte("fake mp3 bytes"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/audio/transcriptions", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if m := readJSON(t, res); res.StatusCode != 200 || !strings.HasPrefix(fmt.Sprint(m["text"]), "heard ") || m["extra_fields"] != nil {
		t.Fatalf("transcription %d: %v", res.StatusCode, m)
	}

	// legacy completions and the Responses API
	r = f.post(t, "/v1/completions", f.key, `{"model":"test/text","prompt":"hello"}`)
	if m := readJSON(t, r); r.StatusCode != 200 || m["model"] != "test/text" {
		t.Fatalf("completions %d: %v", r.StatusCode, m)
	}
	r = f.post(t, "/v1/responses", f.key, `{"model":"test/text","input":"hi"}`)
	raw, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(raw), "hi there") {
		t.Fatalf("responses %d: %s", r.StatusCode, raw)
	}
	var resp map[string]any
	_ = json.Unmarshal(raw, &resp)
	if resp["model"] != "test/text" {
		t.Fatalf("responses model: %v", resp["model"])
	}

	// spend: speech 10 chars × $15/1M = 150, stt 9000, completion 1M tokens × $2/1M = 2_000_000, responses 3 × $2/1M ≈ 6
	f.g.Usage.Close()
	time.Sleep(10 * time.Millisecond)
	a, _ := f.g.S.KV.GetAgg(ctx, usage.OrgSpendKey(f.org.ID))
	if want := int64(150 + 9000 + 2_000_000 + 6); a[store.AggCostMicros] != want {
		t.Fatalf("spend %d, want %d", a[store.AggCostMicros], want)
	}
}
