package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// trustedProxies are the addresses allowed to tell the panel who the visitor is.
// Nothing is trusted by default: X-Forwarded-For from anyone else is ignored,
// because anyone can write it.
type trustedProxies []netip.Prefix

// ParseTrustedProxies reads addresses and ranges ("127.0.0.1", "10.0.0.0/8",
// "fd00::/8"); each value may hold several, separated by commas.
func ParseTrustedProxies(values []string) (trustedProxies, error) {
	var out trustedProxies
	for _, v := range values {
		for _, entry := range strings.Split(v, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if p, err := netip.ParsePrefix(entry); err == nil {
				out = append(out, p.Masked())
				continue
			}
			if a, err := netip.ParseAddr(entry); err == nil {
				a = a.Unmap()
				out = append(out, netip.PrefixFrom(a, a.BitLen()))
				continue
			}
			return nil, fmt.Errorf("--trusted-proxy: %q is not an address or a range like 10.0.0.0/8", entry)
		}
	}
	return out, nil
}

func (t trustedProxies) contains(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range t {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// broad reports whether the list trusts every address, which defeats the point.
func (t trustedProxies) broad() bool {
	for _, p := range t {
		if p.Bits() == 0 {
			return true
		}
	}
	return false
}

func (t trustedProxies) String() string {
	parts := make([]string, len(t))
	for i, p := range t {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

// clientIP is the address the login lockout is counted against. It is the
// connection's address, unless the connection comes from a trusted proxy: then it
// is the last address in X-Forwarded-For that is not itself a trusted proxy (the
// proxy appends the address it saw; whatever the visitor wrote sits to its left).
func (s *server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	conn, err := netip.ParseAddr(host)
	if err != nil || len(s.proxies) == 0 || !s.proxies.contains(conn) {
		return host
	}
	var chain []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		chain = append(chain, strings.Split(v, ",")...)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return host // something that is not an address: do not guess
		}
		if s.proxies.contains(a) {
			continue
		}
		return a.Unmap().String()
	}
	return host // no header, or every hop is a trusted proxy
}
