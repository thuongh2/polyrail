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
	ids := func(org store.Org, hdr string) []string {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("x-gateway-mcp-clients", hdr)
		ctx, _ := g.withMCP(context.Background(), r, org)
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
}
