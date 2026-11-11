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

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// mcpState tracks which MCP servers are registered in Bifrost (by store id).
type mcpState struct {
	mu     sync.Mutex
	active map[string]store.MCPServer
	byName sync.Map // name -> id
}

// toClientConfig maps a stored server to Bifrost: one shared, persistent connection without auth
// (Bifrost's default for new HTTP clients is a fresh connection per call).
func toClientConfig(m store.MCPServer) *schemas.MCPClientConfig {
	url := secret(m.URL)
	return &schemas.MCPClientConfig{ID: m.ID, Name: m.Name, ConnectionType: schemas.MCPConnectionType(m.Type),
		ConnectionString: &url, ToolsToExecute: m.Tools, AuthType: schemas.MCPAuthTypeNone, NeedsSessionStickiness: new(true)}
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
			err = g.BF.AddMCPClient(ctx, toClientConfig(m))
		case !slices.Equal(old.Tools, m.Tools) || old.Name != m.Name:
			err = g.BF.UpdateMCPClient(id, toClientConfig(m))
		default:
			continue
		}
		if err != nil {
			slog.Warn("mcp connect", "id", id, "url", m.URL, "err", err)
			continue // retried on the next config change
		}
		if ok && old.Name != m.Name {
			g.mcp.byName.Delete(old.Name)
		}
		g.mcp.active[id] = m
		g.mcp.byName.Store(m.Name, id)
	}
	return nil
}

// mcpAllowed reports whether the key may use an MCP server: the operator granted it to the org and
// the key's own list (empty = every granted server) names it.
func mcpAllowed(p *principal, name string) bool {
	org := p.Org.MCPServers
	return (slices.Contains(org, "*") || slices.Contains(org, name)) &&
		(len(p.Key.MCPServers) == 0 || slices.Contains(p.Key.MCPServers, name))
}

// withMCP sets which MCP servers' tools are injected into the request. Opt-in per request with
// header x-gateway-mcp-clients: name1,name2 (or *), limited to the servers the operator granted
// the org and the API key allows. Without it no tools are injected (Bifrost filters by server
// name; an empty list includes none). Reports whether any server was included.
func (g *Gateway) withMCP(ctx *schemas.BifrostContext, r *http.Request, p *principal) bool {
	include := []string{}
	for _, name := range strings.Split(r.Header.Get("x-gateway-mcp-clients"), ",") {
		name = strings.TrimSpace(name)
		g.mcp.byName.Range(func(n, _ any) bool {
			if (name == "*" || name == n.(string)) && mcpAllowed(p, n.(string)) && !slices.Contains(include, n.(string)) {
				include = append(include, n.(string))
			}
			return true
		})
	}
	ctx.SetValue(schemas.MCPContextKeyIncludeClients, include)
	return len(include) > 0
}

// MCPExecute runs one tool call returned by the model: POST /v1/mcp/tool/execute.
// The tool must belong to an MCP server granted to the caller's org and allowed for the key; calls are rate limited,
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
	server, name := "", *call.Function.Name
	clients, err := g.MCPTools(r.Context())
	if err != nil {
		writeError(w, errf(503, "api_error", "mcp_unavailable", "MCP servers are unavailable"))
		return
	}
	for _, c := range clients {
		for _, t := range c.Tools {
			// models see "<server>-<tool>"; a bare tool name is accepted too
			if full := c.Config.Name + "-" + t.Name; name == full || name == t.Name {
				server, call.Function.Name = c.Config.Name, &full
			}
		}
	}
	if server == "" || !mcpAllowed(p, server) {
		writeError(w, errf(403, "permission_error", "mcp_tool_not_allowed", "tool %q is not available to this API key", *call.Function.Name))
		return
	}
	if e := g.admit(r.Context(), p); e != nil {
		writeError(w, e)
		return
	}
	m := store.Model{ID: "mcp/" + server}
	bctx := newBifrostCtx(r)
	bctx.SetValue(schemas.MCPContextKeyIncludeClients, []string{server})
	msg, berr := g.BF.ExecuteChatMCPTool(bctx, &call)
	if berr != nil {
		g.record(p, m, store.RequestLog{Status: 502, Provider: server, Error: "mcp tool failed"}, start)
		writeError(w, errf(502, "upstream_error", "mcp_tool_failed", "MCP tool %q failed", *call.Function.Name))
		return
	}
	g.record(p, m, store.RequestLog{Status: 200, Provider: server, Upstream: name}, start)
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
