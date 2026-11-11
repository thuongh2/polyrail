package gateway

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

// blockedNets are destinations a tenant-controlled URL must never reach (SSRF).
var blockedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "64:ff9b::/96", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var ErrPrivateAddress = errors.New("address is not publicly routable")

func blockedIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	for _, p := range blockedNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// CheckPublicURL verifies an http(s) URL whose host resolves only to public addresses.
func CheckPublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("must be an http(s) URL")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return ErrPrivateAddress // same answer as a private address: no internal DNS oracle
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return ErrPrivateAddress
		}
	}
	return nil
}

// hostCheck re-validates tenant provider URLs at request time, cached briefly.
// shortcut: Bifrost's HTTP client does its own DNS lookup, so a rebinding window of the cache TTL
// remains; operators should also block internal egress at the network layer (docs/DEPLOY.md).
var hostCheck = newTTLCache[bool](10_000, 0)

func publicURLCached(raw string) bool {
	if raw == "" {
		return true // hosted API default base URL
	}
	if ok, hit := hostCheck.Get(raw); hit {
		return ok
	}
	ok := CheckPublicURL(raw) == nil
	hostCheck.Set(raw, ok, 0, 30*time.Second)
	return ok
}

// followsUpstreamURLs are tenant-usable provider types whose Bifrost adapter requests URLs taken
// from upstream responses (Replicate prediction polling, stream and output URLs; Hugging Face
// speech audio URLs) with the provider's HTTP client, which always permits loopback. Behind a
// tenant-chosen base_url the upstream could steer the gateway to 127.0.0.1 and have the body
// returned, so tenants get these types only on the vendor's own host.
var followsUpstreamURLs = map[string]bool{"replicate": true, "huggingface": true}

// FollowsUpstreamURLs reports whether provider type t needs its vendor host for tenants.
func FollowsUpstreamURLs(t string) bool { return followsUpstreamURLs[t] }

// tenantReachable reports whether provider p may serve a request: operator providers (and every
// provider with private URLs allowed) always; org providers on a public base_url, and on the
// vendor host for types that follow upstream URLs. async adds Gemini, whose video download
// follows the URL in the upstream operation (with redirects).
func tenantReachable(p store.Provider, allowPrivate, async bool) bool {
	if p.OrgID == "" || allowPrivate {
		return true
	}
	if p.BaseURL != "" && (followsUpstreamURLs[p.Type] || (async && p.Type == "gemini")) {
		return false
	}
	return publicURLCached(p.BaseURL)
}
