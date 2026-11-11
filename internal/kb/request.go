package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

// ReadDocInput parses an add-document request shared by the admin API and /v1/vector_stores:
// multipart/form-data with a "file" part (plus an optional "attributes" JSON field), or JSON
// {filename, text} / {url} with optional attributes. fileID is the OpenAI "file_id" field, which
// callers reject. The body is capped at the upload limit plus 1 MB of envelope.
func (s *Service) ReadDocInput(w http.ResponseWriter, r *http.Request) (in DocInput, fileID string, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxDocBytes()+1<<20)
	defer func() {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			err = fmt.Errorf("%w: documents are limited to %d MB", ErrLimit, s.cfg.MaxDocMB)
		}
	}()
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(8 << 20); err != nil { // larger parts spill to temp files
			return in, "", fmt.Errorf("%w: invalid multipart body: %w", store.ErrInvalid, err)
		}
		defer r.MultipartForm.RemoveAll()
		f, hdr, err := r.FormFile("file")
		if err != nil {
			return in, "", invalid("form field file is required")
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return in, "", err
		}
		in = DocInput{Filename: hdr.Filename, ContentType: hdr.Header.Get("Content-Type"), Data: data}
		if in.ContentType == "application/octet-stream" {
			in.ContentType = "" // browsers send this for unknown extensions: decide by filename
		}
		if a := r.FormValue("attributes"); a != "" {
			if in.Attributes, err = attributes([]byte(a)); err != nil {
				return in, "", err
			}
		}
		return in, "", nil
	}
	var body struct {
		Filename   string          `json:"filename"`
		Text       string          `json:"text"`
		URL        string          `json:"url"`
		FileID     string          `json:"file_id"`
		Attributes json.RawMessage `json:"attributes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return in, "", err
		}
		return in, "", invalid("body must be multipart/form-data with a file, or JSON with text or url")
	}
	in = DocInput{Filename: body.Filename, Text: body.Text, URL: strings.TrimSpace(body.URL)}
	if len(body.Attributes) > 0 && string(body.Attributes) != "null" {
		if in.Attributes, err = attributes(body.Attributes); err != nil {
			return in, "", err
		}
	}
	return in, body.FileID, nil
}

// attributes decodes a JSON object of string, number or boolean values.
func attributes(raw []byte) (map[string]string, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, invalid("attributes must be a JSON object")
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := FilterValue(v)
		if !ok {
			return nil, invalid("attribute values must be strings, numbers or booleans")
		}
		out[k] = s
	}
	return out, nil
}

// FilterValue renders a filter value (string, number or boolean) like a stored attribute.
func FilterValue(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}
