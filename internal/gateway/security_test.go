package gateway

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestBlockedIPRanges(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "100.64.0.1", "0.0.0.1", "198.18.0.1", "169.254.169.254", "::1", "fd00::1", "64:ff9b::a00:1", "::ffff:127.0.0.1"} {
		if !blockedIP(net.ParseIP(ip)) {
			t.Errorf("%s should be blocked", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111"} {
		if blockedIP(net.ParseIP(ip)) {
			t.Errorf("%s should be allowed", ip)
		}
	}
	if CheckPublicURL("http://127.0.0.1:8080") == nil || CheckPublicURL("ftp://8.8.8.8") == nil || CheckPublicURL("https://8.8.8.8/v1") != nil {
		t.Fatal("CheckPublicURL")
	}
}

func TestUpstreamErrorsSanitized(t *testing.T) {
	code := 401
	be := &schemas.BifrostError{StatusCode: &code, Error: &schemas.ErrorField{Message: "Incorrect API key provided: sk-proj-abcdef123456"}}
	if e := fromBifrost(be, false); strings.Contains(e.msg, "abcdef") || e.status != 401 {
		t.Fatalf("public: %+v", e)
	}
	be.Error.Message = "<html>internal admin page</html>"
	if e := fromBifrost(be, true); strings.Contains(e.msg, "internal") {
		t.Fatalf("private model leaks upstream body: %q", e.msg)
	}
}

func TestMCPOrgAllowlist(t *testing.T) {
	g := &Gateway{}
	g.mcp.byName.Store("search", "id1")
	g.mcp.byName.Store("files", "id2")
	ids := func(org store.Org, hdr string, key ...string) []string {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("x-gateway-mcp-clients", hdr)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		g.withMCP(ctx, r, &principal{Org: org, Key: store.APIKey{MCPServers: key}})
		return ctx.Value(schemas.MCPContextKeyIncludeClients).([]string) // Bifrost filters by server name
	}
	if got := ids(store.Org{}, "*"); len(got) != 0 {
		t.Fatalf("org without grant got tools: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"search"}}, "*"); len(got) != 1 || got[0] != "search" {
		t.Fatalf("granted search only: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"*"}}, "files"); len(got) != 1 || got[0] != "files" {
		t.Fatalf("wildcard grant, files requested: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"*"}}, ""); len(got) != 0 {
		t.Fatalf("no header must inject nothing: %v", got)
	}
	// per-key list narrows the org grant (org ∩ key); it never widens it
	if got := ids(store.Org{MCPServers: []string{"*"}}, "*", "files"); len(got) != 1 || got[0] != "files" {
		t.Fatalf("key limited to files: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"search"}}, "*", "files"); len(got) != 0 {
		t.Fatalf("key list must not widen the org grant: %v", got)
	}
}

func TestCostMicros(t *testing.T) {
	m := store.Model{InputPrice: 150_000, OutputPrice: 600_000} // $0.15 / $0.60 per 1M
	for _, c := range []struct {
		in, out int
		want    int64
	}{{0, 0, 0}, {12, 3, 4}, {10, 0, 2}, {1_000_000, 1_000_000, 750_000}, {3, 0, 0}} {
		if got := cost(m, c.in, c.out); got != c.want {
			t.Errorf("cost(%d,%d) = %d, want %d", c.in, c.out, got, c.want)
		}
	}
}

func TestPlanWeightedSplitAndBreakerOrder(t *testing.T) {
	snap := &Snapshot{Providers: map[string]store.Provider{"a": {ID: "a"}, "b": {ID: "b"}}}
	m := store.Model{Targets: []store.Target{{ProviderID: "a", Model: "x", Weight: 3}, {ProviderID: "b", Model: "x", Weight: 1}}}
	rt := newRouter()
	br := rt.br
	first := map[string]int{}
	for i := 0; i < 4000; i++ {
		ts := plan(m, snap, rt, nil)
		if len(ts) != 2 {
			t.Fatalf("plan dropped a target: %v", ts)
		}
		first[ts[0].ProviderID]++
	}
	if share := float64(first["a"]) / 4000; share < 0.70 || share > 0.80 { // expect 0.75
		t.Fatalf("weighted split: a served first %.2f of the time", share)
	}
	for i := 0; i < br.threshold; i++ {
		br.result(targetID(m.Targets[0]), false)
	}
	if ts := plan(m, snap, rt, nil); ts[0].ProviderID != "b" || ts[1].ProviderID != "a" {
		t.Fatalf("open circuit must move a to the end: %v", ts)
	}
}

func TestHideExtraCoversCustomMarshalers(t *testing.T) {
	ef := schemas.BifrostResponseExtraFields{RoutingInfo: schemas.RoutingInfo{Provider: "p-secret", Key: "k"}}
	for _, v := range []any{
		&schemas.BifrostResponsesResponse{ID: new("r"), ExtraFields: ef},
		&schemas.BifrostResponsesStreamResponse{Type: "response.created", ExtraFields: ef},
		&schemas.BifrostTranscriptionResponse{Text: "t", ExtraFields: ef},
		chatOut{BifrostChatResponse: &schemas.BifrostChatResponse{ID: "c", ExtraFields: ef}, Provider: "pub"},
	} {
		out, err := hideExtra(v)
		if err != nil || strings.Contains(string(out), "extra_fields") || strings.Contains(string(out), "p-secret") {
			t.Fatalf("%T leaks extra_fields: %s %v", v, out, err)
		}
	}
}

// Tenant providers whose adapters follow URLs from upstream replies (Replicate polling, Hugging
// Face speech audio, Gemini video downloads) must not sit behind a tenant-chosen host, and
// Bifrost's dialer may reach private networks only for operator providers.
func TestTenantProvidersThatFollowUpstreamURLs(t *testing.T) {
	pub := "https://8.8.8.8" // a literal public address: no DNS needed
	cases := []struct {
		p           store.Provider
		sync, async bool
	}{
		{store.Provider{OrgID: "o", Type: "openai", BaseURL: pub}, true, true},
		{store.Provider{OrgID: "o", Type: "replicate", BaseURL: pub}, false, false},
		{store.Provider{OrgID: "o", Type: "huggingface", BaseURL: pub}, false, false},
		{store.Provider{OrgID: "o", Type: "replicate"}, true, true},
		{store.Provider{OrgID: "o", Type: "gemini", BaseURL: pub}, true, false},
		{store.Provider{OrgID: "o", Type: "openai", BaseURL: "http://127.0.0.1:8123"}, false, false},
		{store.Provider{Type: "replicate", BaseURL: "http://127.0.0.1:9"}, true, true}, // operator
	}
	for _, c := range cases {
		if got := tenantReachable(c.p, false, false); got != c.sync {
			t.Errorf("%s %q sync: got %v", c.p.Type, c.p.BaseURL, got)
		}
		if got := tenantReachable(c.p, false, true); got != c.async {
			t.Errorf("%s %q async: got %v", c.p.Type, c.p.BaseURL, got)
		}
	}
	if !tenantReachable(cases[1].p, true, true) {
		t.Error("AllowPrivateURLs lifts the restriction")
	}

	op := store.Provider{ID: "op", Type: "ollama", BaseURL: "http://10.0.0.5:11434", Enabled: true}
	ten := store.Provider{ID: "ten", OrgID: "o", Type: "openai", BaseURL: pub, Enabled: true}
	snap := &Snapshot{Providers: map[string]store.Provider{op.ID: op, ten.ID: ten},
		byBifrost: map[schemas.ModelProvider]string{BifrostKey(op): op.ID, BifrostKey(ten): ten.ID}}
	a := &account{snap: func() *Snapshot { return snap }}
	for _, c := range []struct {
		p    store.Provider
		want bool
	}{{op, true}, {ten, false}} {
		p, want := c.p, c.want
		cfg, err := a.GetConfigForProvider(BifrostKey(p))
		if err != nil || cfg.NetworkConfig.AllowPrivateNetwork != want {
			t.Errorf("%s: AllowPrivateNetwork = %v, want %v (%v)", p.ID, cfg != nil && cfg.NetworkConfig.AllowPrivateNetwork, want, err)
		}
	}
}

// Batch requests get what a synchronous call gets: guardrails on their text (not on image data)
// and the org's output token cap.
func TestBatchFileGuardrailsAndTokenCap(t *testing.T) {
	m := store.Model{ID: "test/chat", Kind: "chat"}
	org := store.Org{ID: "batch-guard", MaxTokensCap: 50, Guardrails: store.Guardrails{BlockedPatterns: []string{`(?i)forbidden`}, MaskPII: true}}
	line := func(content string, extra string) string {
		return `{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"test/chat"` + extra + `,"messages":[{"role":"user","content":` + content + `}]}}` + "\n"
	}
	if _, err := rewriteBatchFile([]byte(line(`"ok"`, "")+line(`[{"type":"text","text":"this is FORBIDDEN"}]`, "")), m, "up", org); !errors.Is(err, errGuardrail) {
		t.Fatalf("blocked pattern in a content part must be refused, got %v", err)
	}
	img := `[{"type":"text","text":"mail a@b.co"},{"type":"image_url","image_url":{"url":"data:image/png;base64,+1234567890123"}}]`
	out, err := rewriteBatchFile([]byte(line(img, `,"max_tokens":4000,"temperature":0.25`)+line(`"hi"`, "")), m, "up", org)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{`"model":"up"`, `[email]`, `+1234567890123`, `"max_tokens":50`, `"max_completion_tokens":50`, `"temperature":0.25`} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten batch lacks %s: %s", want, got)
		}
	}
	if strings.Contains(got, "a@b.co") || strings.Contains(got, "4000") {
		t.Errorf("PII or uncapped tokens left in batch: %s", got)
	}
}
