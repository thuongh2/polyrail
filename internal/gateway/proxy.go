package gateway

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// GlobalProxy is the operator's default outbound proxy (UPSTREAM_PROXY_URL); every provider
// without its own proxy and without no_proxy uses it.
type GlobalProxy struct {
	store.ProviderProxy
	NoProxy string // comma-separated hosts that connect directly (UPSTREAM_NO_PROXY)
}

// ParseGlobalProxy builds the global proxy from UPSTREAM_PROXY_URL (credentials may sit in the
// URL), UPSTREAM_NO_PROXY and the PEM text of UPSTREAM_PROXY_CA_FILE. Empty rawURL means none.
func ParseGlobalProxy(rawURL, noProxy, caPEM string) (*GlobalProxy, error) {
	if rawURL == "" {
		return nil, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("UPSTREAM_PROXY_URL is not a URL")
	}
	g := &GlobalProxy{NoProxy: noProxy}
	if u.User != nil {
		g.Username = u.User.Username()
		g.Password, _ = u.User.Password()
		u.User = nil
	}
	g.URL, g.CACertPEM = u.String(), caPEM
	if err := CheckProxy(&g.ProviderProxy, false); err != nil {
		return nil, err
	}
	return g, nil
}

// CheckProxy validates a proxy's shape: http, https or socks5 scheme, a host, nothing else in the
// URL, username and password together, and a CA that parses. kept tells whether an empty
// password keeps a stored one.
func CheckProxy(x *store.ProviderProxy, kept bool) error {
	u, err := url.Parse(x.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") || u.Hostname() == "" {
		return errors.New("proxy url must be http://, https:// or socks5://host:port")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("proxy url takes only scheme, host and port; put credentials in username and password")
	}
	if p := u.Port(); p != "" {
		if _, err := net.LookupPort("tcp", p); err != nil {
			return errors.New("proxy url has an invalid port")
		}
	}
	if x.Password != "" && x.Username == "" {
		return errors.New("proxy password needs a username")
	}
	if x.Username != "" && x.Password == "" && !kept {
		return errors.New("proxy username needs a password")
	}
	if x.CACertPEM != "" {
		rest, n := []byte(x.CACertPEM), 0
		for {
			var b *pem.Block
			if b, rest = pem.Decode(rest); b == nil {
				break
			}
			if _, err := x509.ParseCertificate(b.Bytes); b.Type != "CERTIFICATE" || err != nil {
				return errors.New("proxy ca_cert_pem must hold PEM certificates")
			}
			n++
		}
		if n == 0 || strings.TrimSpace(string(rest)) != "" {
			return errors.New("proxy ca_cert_pem must hold PEM certificates")
		}
	}
	return nil
}

// CheckPublicProxy verifies that a tenant's proxy host resolves only to public addresses: a
// proxy reaches whatever it is pointed at, so a private one would bypass the SSRF guard.
func CheckPublicProxy(raw string) error {
	return CheckPublicURL(proxyHostURL(raw))
}

// proxyHostURL rewrites a proxy URL of any scheme (socks5 included) as http://host:port, the form
// CheckPublicURL resolves; a host-less URL for an unparsable one, which CheckPublicURL rejects.
func proxyHostURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "http://"
	}
	return "http://" + u.Host
}

// proxyConfig is the Bifrost proxy setting for p: its own proxy (tenants only when allowed) or
// pool (platform providers), else the global pool, else the global proxy, unless p opts out
// (no_proxy), else none. A pool is reached through the loopback forwarder (proxypool.go). Values are literal SecretVars (secret()).
// A tenant's own http/socks5 proxy is pinned to an address vetted as public (pinProxyURL):
// Bifrost dials the proxy hop with a plain dialer, without the private-network check.
func (a *account) proxyConfig(p store.Provider) (*schemas.ProxyConfig, error) {
	x, noProxy := p.Proxy, ""
	if x != nil && p.OrgID != "" && !a.tenantProxy {
		x = nil // stored while tenant proxies were allowed: ignored until they are again
	}
	if x == nil || x.URL == "" {
		if ref, own := poolRef(p, a.globalPool()); ref != "" && a.pools != nil {
			if own {
				return a.pools.config(p.ID, ""), nil
			}
			return a.pools.config(p.ID, a.poolNoProxy), nil
		}
		if p.NoProxy || a.proxy == nil {
			return nil, nil
		}
		x, noProxy = &a.proxy.ProviderProxy, a.proxy.NoProxy
	}
	proxyURL := x.URL
	if p.OrgID != "" && x == p.Proxy && !a.allowPrivate {
		var err error
		if proxyURL, err = pinProxyURL(x.URL); err != nil {
			return nil, fmt.Errorf("provider %s proxy: %w", p.ID, err)
		}
	}
	typ := schemas.HTTPProxy // http:// and https:// proxies both tunnel with CONNECT
	if strings.HasPrefix(x.URL, "socks5://") {
		typ = schemas.Socks5Proxy
	}
	return &schemas.ProxyConfig{Type: typ, URL: secretPtr(proxyURL), Username: secretPtr(x.Username),
		Password: secretPtr(x.Password), CACertPEM: secretPtr(x.CACertPEM), NoProxy: noProxy}, nil
}

// lookupProxyIP resolves proxy hosts (tests replace it).
var lookupProxyIP = net.LookupIP

// pinnedProxies caches pinProxyURL results: Bifrost asks for provider configs on hot paths.
var pinnedProxies = newTTLCache[string](10_000, 0)

// pinProxyURL requires every address of the proxy host to be public and, for http and socks5
// proxies, returns the URL with the host replaced by the first address, so a host that rebinds
// to an internal address after this check is never dialed. https proxies keep the host name:
// the TLS handshake verifies it, which an internal service cannot satisfy.
// shortcut: the pinned address lives as long as the provider's client (until its config changes
// or the node restarts); re-pin periodically if tenant proxies with moving addresses need it.
func pinProxyURL(raw string) (string, error) {
	if v, ok := pinnedProxies.Get(raw); ok {
		if v == "" {
			return "", ErrPrivateAddress
		}
		return v, nil
	}
	pinned, err := pinProxy(raw)
	pinnedProxies.Set(raw, pinned, 0, 30*time.Second)
	return pinned, err
}

func pinProxy(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", ErrPrivateAddress
	}
	ips := []net.IP{net.ParseIP(u.Hostname())}
	if ips[0] == nil {
		if ips, err = lookupProxyIP(u.Hostname()); err != nil || len(ips) == 0 {
			return "", ErrPrivateAddress // same answer as a private address: no internal DNS oracle
		}
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return "", ErrPrivateAddress
		}
	}
	if u.Scheme == "https" {
		return raw, nil
	}
	host := ips[0].String()
	if u.Port() != "" {
		u.Host = net.JoinHostPort(host, u.Port())
	} else if ips[0].To4() == nil {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	return u.String(), nil
}

// proxyOf is what a provider's proxy setting looks like to Reload (restart on change).
func proxyOf(p store.Provider) store.ProviderProxy {
	if p.Proxy == nil {
		return store.ProviderProxy{}
	}
	return *p.Proxy
}

func (a *account) globalPool() string {
	if a.pools == nil {
		return ""
	}
	return a.pools.global
}
