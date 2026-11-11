package gateway

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-gateway/agent-gateway/internal/store"
	bifrost "github.com/maximhq/bifrost/core"
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
		ctx, _ := g.withMCP(context.Background(), r, &principal{Org: org, Key: store.APIKey{MCPServers: key}})
		return ctx.Value(bifrost.MCPContextKeyIncludeClients).([]string)
	}
	if got := ids(store.Org{}, "*"); len(got) != 0 {
		t.Fatalf("org without grant got tools: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"search"}}, "*"); len(got) != 1 || got[0] != "id1" {
		t.Fatalf("granted search only: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"*"}}, "files"); len(got) != 1 || got[0] != "id2" {
		t.Fatalf("wildcard grant, files requested: %v", got)
	}
	if got := ids(store.Org{MCPServers: []string{"*"}}, ""); len(got) != 0 {
		t.Fatalf("no header must inject nothing: %v", got)
	}
	// per-key list narrows the org grant (org ∩ key); it never widens it
	if got := ids(store.Org{MCPServers: []string{"*"}}, "*", "files"); len(got) != 1 || got[0] != "id2" {
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
	br := newBreaker()
	first := map[string]int{}
	for i := 0; i < 4000; i++ {
		ts := plan(m, snap, br)
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
	if ts := plan(m, snap, br); ts[0].ProviderID != "b" || ts[1].ProviderID != "a" {
		t.Fatalf("open circuit must move a to the end: %v", ts)
	}
}
