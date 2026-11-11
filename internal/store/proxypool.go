package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ProxyStrategies pick a pool member for each new upstream connection (gateway/proxypool.go).
var ProxyStrategies = []string{"round-robin", "random", "sticky", "latency"}

// ProxyPool is an operator-managed set of outbound proxies that platform providers (and, as the
// global default, every provider) rotate through (D23). Platform-only: tenants never see pools.
type ProxyPool struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Strategy      string        `json:"strategy"`       // one of ProxyStrategies
	StickyMinutes int           `json:"sticky_minutes"` // sticky: keep one member this long
	FailClosed    bool          `json:"fail_closed"`    // every member unusable: refuse (true) or connect directly
	Members       []ProxyMember `json:"members"`
	CreatedAt     time.Time     `json:"created_at"`
}

// ProxyMember is one proxy of a pool. Password follows the Value/Sealed pattern: plaintext only
// on input and inside the gateway, sealed at rest, never returned (see Redacted).
type ProxyMember struct {
	ID             string `json:"id"`
	URL            string `json:"url"` // http://, https:// or socks5://host:port
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	SealedPassword string `json:"sealed_password,omitempty"`
	CACertPEM      string `json:"ca_cert_pem,omitempty"`
	Label          string `json:"label,omitempty"`
	Enabled        bool   `json:"enabled"`
}

// Proxy is the member as a single proxy (the shape gateway.CheckProxy validates).
func (m ProxyMember) Proxy() ProviderProxy {
	return ProviderProxy{URL: m.URL, Username: m.Username, Password: m.Password, CACertPEM: m.CACertPEM}
}

// Redacted returns the pool without member passwords (plaintext or sealed).
func (p ProxyPool) Redacted() ProxyPool {
	p.Members = slices.Clone(p.Members)
	for i := range p.Members {
		p.Members[i].Password, p.Members[i].SealedPassword = "", ""
	}
	return p
}

// PutProxyPool creates (empty ID) or replaces a pool, filling defaults and sealing member
// passwords. A member sent without a password keeps its stored one while its id, URL and
// username stay the same; a new URL or username needs the password again.
func (s *Store) PutProxyPool(ctx context.Context, p *ProxyPool) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 100 {
		return fmt.Errorf("%w: pool name required (at most 100 characters)", ErrInvalid)
	}
	if p.Strategy == "" {
		p.Strategy = "round-robin"
	}
	if !slices.Contains(ProxyStrategies, p.Strategy) {
		return fmt.Errorf("%w: strategy must be one of %s", ErrInvalid, strings.Join(ProxyStrategies, ", "))
	}
	if p.StickyMinutes == 0 {
		p.StickyMinutes = 30
	}
	if p.StickyMinutes < 1 || p.StickyMinutes > 1440 {
		return fmt.Errorf("%w: sticky_minutes must be 1 to 1440", ErrInvalid)
	}
	if len(p.Members) == 0 || len(p.Members) > 100 {
		return fmt.Errorf("%w: a pool needs 1 to 100 members", ErrInvalid)
	}
	create := p.ID == ""
	if create {
		p.ID, p.CreatedAt = NewID(), time.Now().UTC()
	}
	return s.update(ctx, func(tx Tx) error {
		old, err := getJSON[ProxyPool](tx.Get, "proxypool/"+p.ID)
		switch {
		case err != nil && !errors.Is(err, ErrNotFound):
			return err
		case !create && old == nil:
			return ErrNotFound
		case old != nil:
			p.CreatedAt = old.CreatedAt
		}
		seen := map[string]bool{}
		for i := range p.Members {
			m := &p.Members[i]
			if m.ID == "" {
				m.ID = NewID()
			}
			if seen[m.ID] {
				return fmt.Errorf("%w: duplicate member id %s", ErrInvalid, m.ID)
			}
			seen[m.ID] = true
			m.SealedPassword = "" // never trust a sealed value from input
			if m.Password != "" {
				if m.SealedPassword, err = s.box.Seal(m.Password); err != nil {
					return err
				}
				m.Password = ""
				continue
			}
			if old == nil || m.Username == "" {
				continue
			}
			for _, om := range old.Members {
				if om.ID != m.ID || om.SealedPassword == "" {
					continue
				}
				if om.URL != m.URL || om.Username != m.Username {
					return fmt.Errorf("%w: re-enter the password of member %s when changing its URL or username", ErrInvalid, m.ID)
				}
				m.SealedPassword = om.SealedPassword
			}
		}
		return putJSON(tx, "proxypool/"+p.ID, p)
	})
}

// GetProxyPool returns a pool; with decrypt, member passwords are filled in (gateway use only).
func (s *Store) GetProxyPool(ctx context.Context, id string, decrypt bool) (*ProxyPool, error) {
	p, err := getJSON[ProxyPool](s.get(ctx), "proxypool/"+id)
	if err != nil || !decrypt {
		return p, err
	}
	return p, s.openPool(p)
}

// ListProxyPools returns every pool; with decrypt, member passwords are filled in.
func (s *Store) ListProxyPools(ctx context.Context, decrypt bool) ([]ProxyPool, error) {
	ps, err := listJSON[ProxyPool](ctx, s.KV, "proxypool/", "", 0)
	if err != nil || !decrypt {
		return ps, err
	}
	for i := range ps {
		if err := s.openPool(&ps[i]); err != nil {
			return nil, err
		}
	}
	return ps, nil
}

func (s *Store) openPool(p *ProxyPool) error {
	for i := range p.Members {
		m := &p.Members[i]
		if m.SealedPassword == "" {
			continue
		}
		var err error
		if m.Password, err = s.box.Open(m.SealedPassword); err != nil {
			return fmt.Errorf("proxy pool %s member %s password: %w", p.ID, m.ID, err)
		}
	}
	return nil
}

// DeleteProxyPool removes a pool no provider names.
// shortcut: the in-use check reads providers outside the delete's transaction; a provider saved
// with this pool in the same instant keeps a dangling id, which fails closed (no proxy, 502).
func (s *Store) DeleteProxyPool(ctx context.Context, id string) error {
	if _, err := s.GetProxyPool(ctx, id, false); err != nil {
		return err
	}
	ps, err := s.ListProviders(ctx, false)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.ProxyPool == id {
			return fmt.Errorf("%w: provider %s (%s) uses this pool", ErrInvalid, p.Name, p.ID)
		}
	}
	return s.update(ctx, func(tx Tx) error { return tx.Delete("proxypool/" + id) })
}
