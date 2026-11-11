package store

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// TraceExport sends a span per request to an OpenTelemetry (OTLP/HTTP JSON) endpoint or to
// Langfuse's OTLP endpoint (internal/broadcast, D30). OrgID "" is a platform export (operator),
// which receives the requests of every org.
type TraceExport struct {
	ID    string `json:"id"`
	OrgID string `json:"org_id"`
	// WorkspaceID limits an org export to the requests of that workspace's keys; the default
	// workspace's exports ("") receive every workspace's requests (D31).
	WorkspaceID string `json:"workspace_id,omitempty"`
	Name        string `json:"name"`
	Type        string `json:"type"`     // otlp | langfuse
	Endpoint    string `json:"endpoint"` // otlp: the traces URL (…/v1/traces); langfuse: the Langfuse base URL
	// Headers are sent with every export (auth tokens); write-only: plaintext only inside the
	// gateway, sealed at rest. HeaderNames is what the API shows.
	Headers        map[string]string `json:"-"`
	HeaderNames    []string          `json:"header_names"`
	SampleRate     float64           `json:"sample_rate"`       // 0..1, fraction of requests exported
	KeyIDs         []string          `json:"key_ids,omitempty"` // only these API keys' requests; empty = all
	IncludeContent bool              `json:"include_content"`   // prompt and completion, only for orgs with content logging on
	Enabled        bool              `json:"enabled"`
	Failures       int               `json:"failures"` // consecutive failed batches; 10 disable the export
	CreatedAt      time.Time         `json:"created_at"`
}

// TraceExportTypes are the supported export types.
var TraceExportTypes = []string{"otlp", "langfuse"}

const (
	maxTraceHeaders  = 20
	maxTraceKeyIDs   = 1000
	maxTraceHeaderSz = 4096
)

var reHeaderName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,128}$`) // RFC 9110 token

// reservedHeaders are set by the exporter itself.
var reservedHeaders = []string{"host", "content-type", "content-length", "transfer-encoding", "connection", "user-agent"}

type storedTraceExport struct {
	TraceExport
	Sealed string `json:"sealed_headers"`
}

func traceExportPrefix(orgID string) string {
	if orgID == "" {
		orgID = "_"
	}
	return "trace/" + orgID + "/"
}

// traceExportKey is an export's KV key. Like webhooks, writes skip the config version bump: the
// exporter reloads exports itself and the failure counter changes on failed batches.
func traceExportKey(orgID, id string) string { return traceExportPrefix(orgID) + id }

func (t *TraceExport) validate() error {
	inv := func(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrInvalid}, a...)...) }
	t.Name = strings.TrimSpace(t.Name)
	switch {
	case t.Name == "" || len(t.Name) > 100:
		return inv("name must be 1-100 characters")
	case !slices.Contains(TraceExportTypes, t.Type):
		return inv("type must be one of %s", strings.Join(TraceExportTypes, ", "))
	case t.Endpoint == "":
		return inv("endpoint required")
	case t.SampleRate < 0 || t.SampleRate > 1 || t.SampleRate != t.SampleRate:
		return inv("sample_rate must be between 0 and 1")
	case len(t.KeyIDs) > maxTraceKeyIDs:
		return inv("key_ids has more than %d entries", maxTraceKeyIDs)
	case len(t.Headers) > maxTraceHeaders:
		return inv("at most %d headers", maxTraceHeaders)
	}
	if slices.ContainsFunc(t.KeyIDs, func(s string) bool { return s == "" || len(s) > 64 }) {
		return inv("key_ids entries must be key ids")
	}
	for k, v := range t.Headers {
		if !reHeaderName.MatchString(k) || slices.Contains(reservedHeaders, strings.ToLower(k)) {
			return inv("header %q is not allowed", k)
		}
		if len(v) > maxTraceHeaderSz || strings.ContainsAny(v, "\r\n\x00") {
			return inv("header %q: value must be one line of at most %d bytes", k, maxTraceHeaderSz)
		}
	}
	t.HeaderNames = slices.Sorted(maps.Keys(t.Headers))
	return nil
}

func (s *Store) sealTraceExport(t *TraceExport) (storedTraceExport, error) {
	raw, err := json.Marshal(t.Headers)
	if err != nil {
		return storedTraceExport{}, err
	}
	sealed, err := s.box.Seal(string(raw))
	out := *t
	out.Headers = nil
	return storedTraceExport{out, sealed}, err
}

func (s *Store) openTraceExport(st *storedTraceExport) (*TraceExport, error) {
	t := st.TraceExport
	raw, err := s.box.Open(st.Sealed)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &t.Headers)
	}
	if err != nil {
		return nil, fmt.Errorf("trace export %s: %w", t.ID, err)
	}
	return &t, nil
}

// CreateTraceExport validates and stores t with its headers sealed. Callers check the endpoint
// (SSRF guard) and that key_ids belong to the org.
func (s *Store) CreateTraceExport(ctx context.Context, t *TraceExport) error {
	if err := t.validate(); err != nil {
		return err
	}
	t.ID, t.CreatedAt = NewID(), time.Now().UTC()
	st, err := s.sealTraceExport(t)
	if err != nil {
		return err
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		if t.OrgID != "" {
			if _, err := tx.Get("org/" + t.OrgID); err != nil {
				return err
			}
		}
		return putJSON(tx, traceExportKey(t.OrgID, t.ID), st)
	})
}

// GetTraceExport returns the export with its headers decrypted.
func (s *Store) GetTraceExport(ctx context.Context, orgID, id string) (*TraceExport, error) {
	st, err := getJSON[storedTraceExport](s.get(ctx), traceExportKey(orgID, id))
	if err != nil {
		return nil, err
	}
	return s.openTraceExport(st)
}

// ListTraceExports returns an org's exports ("" = platform) with headers decrypted (gateway
// use; JSON hides them).
func (s *Store) ListTraceExports(ctx context.Context, orgID string) ([]TraceExport, error) {
	return s.listTraceExports(ctx, traceExportPrefix(orgID))
}

// ListAllTraceExports returns every org's and the platform's exports (the exporter's reload).
func (s *Store) ListAllTraceExports(ctx context.Context) ([]TraceExport, error) {
	return s.listTraceExports(ctx, "trace/")
}

func (s *Store) listTraceExports(ctx context.Context, prefix string) ([]TraceExport, error) {
	sts, err := listJSON[storedTraceExport](ctx, s.KV, prefix, "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]TraceExport, 0, len(sts))
	for i := range sts {
		t, err := s.openTraceExport(&sts[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

// UpdateTraceExport applies fn to the stored export (headers decrypted) and validates the
// result; id, org and created_at cannot change.
func (s *Store) UpdateTraceExport(ctx context.Context, orgID, id string, fn func(*TraceExport) error) (*TraceExport, error) {
	var out *TraceExport
	err := s.KV.Update(ctx, func(tx Tx) error {
		st, err := getJSON[storedTraceExport](tx.Get, traceExportKey(orgID, id))
		if err != nil {
			return err
		}
		t, err := s.openTraceExport(st)
		if err != nil {
			return err
		}
		if err := fn(t); err != nil {
			return err
		}
		t.ID, t.OrgID, t.CreatedAt = id, st.OrgID, st.CreatedAt
		if err := t.validate(); err != nil {
			return err
		}
		nst, err := s.sealTraceExport(t)
		if err != nil {
			return err
		}
		out = t
		return putJSON(tx, traceExportKey(orgID, id), nst)
	})
	return out, err
}

func (s *Store) DeleteTraceExport(ctx context.Context, orgID, id string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get(traceExportKey(orgID, id)); err != nil {
			return err
		}
		return tx.Delete(traceExportKey(orgID, id))
	})
}
