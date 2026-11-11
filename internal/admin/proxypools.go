package admin

// Proxy pools (D23): operator-only CRUD, a per-member reachability test, and this node's health
// view. Member passwords are write-only: sealed at rest and never returned.

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/agent-gateway/agent-gateway/internal/gateway"
	"github.com/agent-gateway/agent-gateway/internal/store"
)

func (a *API) registerProxyPools(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/proxy-pools", a.operator(a.listProxyPools))
	mux.HandleFunc("POST /api/admin/proxy-pools", a.operator(a.putProxyPool))
	mux.HandleFunc("PUT /api/admin/proxy-pools/{id}", a.operator(a.putProxyPool))
	mux.HandleFunc("DELETE /api/admin/proxy-pools/{id}", a.operator(a.deleteProxyPool))
	mux.HandleFunc("POST /api/admin/proxy-pools/{id}/test", a.operator(a.testProxyPool))
}

// poolInput is the body of POST/PUT …/proxy-pools; fail_closed defaults to true.
type poolInput struct {
	Name          string              `json:"name"`
	Strategy      string              `json:"strategy"`
	StickyMinutes int                 `json:"sticky_minutes"`
	FailClosed    *bool               `json:"fail_closed"`
	Members       []store.ProxyMember `json:"members"`
}

type proxyMemberView struct {
	store.ProxyMember
	Health gateway.MemberHealth `json:"health"`
}

type poolView struct {
	store.ProxyPool
	Members []proxyMemberView `json:"members"`
}

func (a *API) poolView(p store.ProxyPool) poolView {
	h := a.G.ProxyHealth(p)
	v := poolView{ProxyPool: p.Redacted()}
	for _, m := range v.ProxyPool.Members {
		v.Members = append(v.Members, proxyMemberView{m, h[m.ID]})
	}
	return v
}

func (a *API) listProxyPools(w http.ResponseWriter, r *http.Request) {
	ps, err := a.S.ListProxyPools(r.Context(), false)
	if err != nil {
		fail(w, err)
		return
	}
	out := []poolView{}
	for _, p := range ps {
		out = append(out, a.poolView(p))
	}
	writeJSON(w, 200, out)
}

func (a *API) putProxyPool(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if !decode(w, r, &in) {
		return
	}
	p := store.ProxyPool{ID: r.PathValue("id"), Name: strings.TrimSpace(in.Name), Strategy: in.Strategy,
		StickyMinutes: in.StickyMinutes, FailClosed: in.FailClosed == nil || *in.FailClosed, Members: in.Members}
	ctx := r.Context()
	var old *store.ProxyPool
	if p.ID != "" {
		o, err := a.S.GetProxyPool(ctx, p.ID, false)
		if err != nil {
			fail(w, err)
			return
		}
		old = o
	}
	all, err := a.S.ListProxyPools(ctx, false)
	if err != nil {
		fail(w, err)
		return
	}
	for _, x := range all {
		if x.ID != p.ID && (strings.EqualFold(x.Name, p.Name) || x.ID == p.Name) {
			fail(w, fmt.Errorf("%w: a pool named %q exists", store.ErrInvalid, p.Name))
			return
		}
	}
	for i := range p.Members {
		m := &p.Members[i]
		if len(m.Label) > 100 {
			fail(w, fmt.Errorf("%w: member label is at most 100 characters", store.ErrInvalid))
			return
		}
		kept := false
		if old != nil {
			for _, om := range old.Members {
				kept = kept || (m.ID != "" && om.ID == m.ID && om.SealedPassword != "" && om.URL == m.URL && om.Username == m.Username)
			}
		}
		x := m.Proxy()
		if err := gateway.CheckProxy(&x, kept); err != nil {
			fail(w, fmt.Errorf("%w: member %d: %v", store.ErrInvalid, i+1, err))
			return
		}
	}
	if err := a.S.PutProxyPool(ctx, &p); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, "_", "proxy_pool.put", p.ID, fmt.Sprintf("%s %s %d members fail_closed=%t", p.Name, p.Strategy, len(p.Members), p.FailClosed))
	writeJSON(w, 200, a.poolView(p))
}

func (a *API) deleteProxyPool(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := a.S.GetProxyPool(r.Context(), id, false)
	if err != nil {
		fail(w, err)
		return
	}
	if err := a.S.DeleteProxyPool(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, "_", "proxy_pool.delete", id, p.Name)
	w.WriteHeader(http.StatusNoContent)
}

// testProxyPool opens a tunnel to target (default api.openai.com:443) through each member and
// reports reachability and connect latency; nothing from the target is read or echoed.
func (a *API) testProxyPool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Target string `json:"target"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	target := orDefault(in.Target, "api.openai.com:443")
	host, port, err := net.SplitHostPort(target)
	if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		fail(w, fmt.Errorf("%w: target must be host:port", store.ErrInvalid))
		return
	}
	p, err := a.S.GetProxyPool(r.Context(), r.PathValue("id"), true)
	if err != nil {
		fail(w, err)
		return
	}
	res := a.G.TestProxyPool(r.Context(), *p, target)
	a.audit(r, "_", "proxy_pool.test", p.ID, p.Name+" "+target)
	writeJSON(w, 200, map[string]any{"target": target, "results": res})
}

// checkProxyPool validates a provider's pool: platform providers only, an existing pool, and not
// together with a single proxy.
func (a *API) checkProxyPool(p *store.Provider, r *http.Request) error {
	if p.ProxyPool == "" {
		return nil
	}
	if p.OrgID != "" {
		return fmt.Errorf("%w: org providers cannot use a proxy pool", store.ErrInvalid)
	}
	if p.Proxy != nil && p.Proxy.URL != "" {
		return fmt.Errorf("%w: set either proxy or proxy_pool, not both", store.ErrInvalid)
	}
	if _, err := a.S.GetProxyPool(r.Context(), p.ProxyPool, false); err != nil {
		return orInvalid(nil, "proxy_pool names no pool")
	}
	return nil
}
