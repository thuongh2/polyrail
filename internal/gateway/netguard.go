package gateway

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"time"
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
var hostCheck = struct {
	sync.Mutex
	m map[string]hostResult
}{m: map[string]hostResult{}}

type hostResult struct {
	ok  bool
	exp time.Time
}

func publicURLCached(raw string) bool {
	if raw == "" {
		return true // hosted API default base URL
	}
	hostCheck.Lock()
	r, hit := hostCheck.m[raw]
	hostCheck.Unlock()
	if hit && time.Now().Before(r.exp) {
		return r.ok
	}
	ok := CheckPublicURL(raw) == nil
	hostCheck.Lock()
	if len(hostCheck.m) > 10_000 {
		hostCheck.m = map[string]hostResult{}
	}
	hostCheck.m[raw] = hostResult{ok, time.Now().Add(30 * time.Second)}
	hostCheck.Unlock()
	return ok
}
