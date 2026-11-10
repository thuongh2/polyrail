package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// mcpState tracks which MCP servers are registered in Bifrost (by store id).
type mcpState struct {
	mu     sync.Mutex
	active map[string]store.MCPServer
	byName sync.Map // name -> id
}

func toClientConfig(m store.MCPServer) schemas.MCPClientConfig {
	url := m.URL
	return schemas.MCPClientConfig{ID: m.ID, Name: m.Name, ConnectionType: schemas.MCPConnectionType(m.Type),
		ConnectionString: &url, ToolsToExecute: m.Tools}
}

func (g *Gateway) mcpConfig(ctx context.Context) (*schemas.MCPConfig, error) {
	servers, err := g.S.ListMCP(ctx)
	if err != nil {
		return nil, err
	}
	g.mcp.active = map[string]store.MCPServer{}
	cfg := &schemas.MCPConfig{}
	for _, m := range servers {
		if m.Enabled {
			cfg.ClientConfigs = append(cfg.ClientConfigs, toClientConfig(m))
			g.mcp.active[m.ID] = m
			g.mcp.byName.Store(m.Name, m.ID)
		}
	}
	if len(cfg.ClientConfigs) == 0 {
		return nil, nil
	}
	return cfg, nil
}

// syncMCP adds, edits and removes Bifrost MCP clients to match the store.
func (g *Gateway) syncMCP(ctx context.Context) error {
	servers, err := g.S.ListMCP(ctx)
	if err != nil {
		return err
	}
	g.mcp.mu.Lock()
	defer g.mcp.mu.Unlock()
	want := map[string]store.MCPServer{}
	for _, m := range servers {
		if m.Enabled {
			want[m.ID] = m
		}
	}
	for id, old := range g.mcp.active {
		if m, ok := want[id]; !ok || m.URL != old.URL || m.Type != old.Type {
			if err := g.BF.RemoveMCPClient(id); err != nil {
				slog.Warn("mcp remove", "id", id, "err", err)
			}
			delete(g.mcp.active, id)
			g.mcp.byName.Delete(old.Name)
		}
	}
	for id, m := range want {
		old, ok := g.mcp.active[id]
		switch {
		case !ok:
			err = g.BF.AddMCPClient(toClientConfig(m))
		case !slices.Equal(old.Tools, m.Tools) || old.Name != m.Name:
			err = g.BF.EditMCPClient(id, toClientConfig(m))
		default:
			continue
		}
		if err != nil {
			slog.Warn("mcp connect", "id", id, "url", m.URL, "err", err)
			continue // retried on the next config change
		}
		g.mcp.active[id] = m
		g.mcp.byName.Store(m.Name, id)
	}
	return nil
}

func mcpAllowed(org store.Org, name string) bool {
	return slices.Contains(org.MCPServers, "*") || slices.Contains(org.MCPServers, name)
}

// withMCP sets which MCP servers' tools are injected into the request. Opt-in per request with
// header x-gateway-mcp-clients: name1,name2 (or *), limited to the servers the operator granted
// the org. Without it no tools are injected. Reports whether any server was included.
func (g *Gateway) withMCP(ctx context.Context, r *http.Request, org store.Org) (context.Context, bool) {
	include := []string{}
	for _, name := range strings.Split(r.Header.Get("x-gateway-mcp-clients"), ",") {
		name = strings.TrimSpace(name)
		g.mcp.byName.Range(func(n, id any) bool {
			if (name == "*" || name == n.(string)) && mcpAllowed(org, n.(string)) && !slices.Contains(include, id.(string)) {
				include = append(include, id.(string))
			}
			return true
		})
	}
	return context.WithValue(ctx, bifrost.MCPContextKeyIncludeClients, include), len(include) > 0
}

// MCPExecute runs one tool call returned by the model: POST /v1/mcp/tool/execute.
// The tool must belong to an MCP server granted to the caller's org; calls are rate limited,
// budget-checked and logged like model requests.
func (g *Gateway) MCPExecute(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	body, e := g.readBody(w, r)
	if e != nil {
		writeError(w, e)
		return
	}
	var call schemas.ChatAssistantMessageToolCall
	if err := sonic.Unmarshal(body, &call); err != nil || call.Function.Name == nil {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a tool call"))
		return
	}
	server := ""
	clients, err := g.MCPTools(r.Context())
	if err != nil {
		writeError(w, errf(503, "api_error", "mcp_unavailable", "MCP servers are unavailable"))
		return
	}
	for _, c := range clients {
		for _, t := range c.Tools {
			if t.Name == *call.Function.Name {
				server = c.Config.Name
			}
		}
	}
	if server == "" || !mcpAllowed(p.Org, server) {
		writeError(w, errf(403, "permission_error", "mcp_tool_not_allowed", "tool %q is not available to this organization", *call.Function.Name))
		return
	}
	if e := g.admit(r.Context(), p); e != nil {
		writeError(w, e)
		return
	}
	m := store.Model{ID: "mcp/" + server}
	msg, berr := g.BF.ExecuteMCPTool(r.Context(), call)
	if berr != nil {
		g.record(p, m, store.RequestLog{Status: 502, Provider: server, Error: "mcp tool failed"}, start)
		writeError(w, errf(502, "upstream_error", "mcp_tool_failed", "MCP tool %q failed", *call.Function.Name))
		return
	}
	g.record(p, m, store.RequestLog{Status: 200, Provider: server, Upstream: *call.Function.Name}, start)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

// MCPTools lists connected MCP servers and their tools (for the UI and operators).
func (g *Gateway) MCPTools(ctx context.Context) ([]schemas.MCPClient, error) {
	clients, err := g.BF.GetMCPClients()
	if err != nil && strings.Contains(err.Error(), "not configured") {
		return nil, nil
	}
	return clients, err
}
