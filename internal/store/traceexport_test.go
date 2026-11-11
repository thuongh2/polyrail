package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTraceExportSealedCRUD(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "acme"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	for i, bad := range []TraceExport{
		{OrgID: o.ID, Name: "x", Type: "s3", Endpoint: "https://e"},
		{OrgID: o.ID, Name: "x", Type: "otlp", Endpoint: "https://e", SampleRate: 1.5},
		{OrgID: o.ID, Name: "x", Type: "otlp", Endpoint: "https://e", Headers: map[string]string{"Host": "a"}},
		{OrgID: o.ID, Name: "x", Type: "otlp", Endpoint: "https://e", Headers: map[string]string{"X-A": "a\r\nb"}},
		{OrgID: o.ID, Name: "", Type: "otlp", Endpoint: "https://e"},
	} {
		if err := s.CreateTraceExport(ctx, &bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	te := &TraceExport{OrgID: o.ID, Name: "tempo", Type: "otlp", Endpoint: "https://otel.example.com/v1/traces",
		Headers: map[string]string{"Authorization": "Bearer s3cret"}, SampleRate: 1, Enabled: true}
	if err := s.CreateTraceExport(ctx, te); err != nil {
		t.Fatal(err)
	}
	raw, _ := s.KV.Get(ctx, traceExportKey(o.ID, te.ID))
	js, _ := json.Marshal(te)
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(js), "s3cret") || !strings.Contains(string(js), `"header_names":["Authorization"]`) {
		t.Fatalf("headers must be sealed and hidden: %s / %s", raw, js)
	}
	plat := &TraceExport{Name: "ops", Type: "otlp", Endpoint: "https://o/v1/traces", SampleRate: 0.1, Enabled: true}
	if err := s.CreateTraceExport(ctx, plat); err != nil {
		t.Fatal(err)
	}
	got, err := s.UpdateTraceExport(ctx, o.ID, te.ID, func(x *TraceExport) error { x.Failures = 3; return nil })
	if err != nil || got.Headers["Authorization"] != "Bearer s3cret" || got.Failures != 3 {
		t.Fatalf("update keeps headers: %+v %v", got, err)
	}
	all, err := s.ListAllTraceExports(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	if ps, _ := s.ListTraceExports(ctx, ""); len(ps) != 1 || ps[0].ID != plat.ID {
		t.Fatalf("platform list: %+v", ps)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.ListAllTraceExports(ctx); len(all) != 1 {
		t.Fatalf("org delete keeps its exports: %+v", all)
	}
}
