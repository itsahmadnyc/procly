package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseTrustedProxies(t *testing.T) {
	good, err := ParseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8, 192.168.1.5", "::1", "fd00::/8", "", " "})
	if err != nil || len(good) != 5 {
		t.Fatalf("got %v, %v", good, err)
	}
	for _, in := range []string{"127.0.0.1", "10.4.5.6", "192.168.1.5", "::1", "fd00::99"} {
		if !good.contains(mustAddr(t, in)) {
			t.Errorf("%s should be trusted", in)
		}
	}
	for _, in := range []string{"8.8.8.8", "192.168.1.6", "2001:db8::1", "11.0.0.1"} {
		if good.contains(mustAddr(t, in)) {
			t.Errorf("%s should not be trusted", in)
		}
	}
	// An IPv4 address written as IPv6 is the same address.
	if !good.contains(mustAddr(t, "::ffff:127.0.0.1")) {
		t.Error("::ffff:127.0.0.1 is 127.0.0.1")
	}
	for _, bad := range []string{"nope", "300.1.1.1", "10.0.0.0/33", "10.0.0.0/8, oops", "localhost"} {
		if _, err := ParseTrustedProxies([]string{bad}); err == nil || !strings.Contains(err.Error(), "--trusted-proxy") {
			t.Errorf("%q should be refused, naming the flag: %v", bad, err)
		}
	}
	if none, err := ParseTrustedProxies(nil); err != nil || len(none) != 0 {
		t.Errorf("nothing given trusts nothing: %v %v", none, err)
	}
	all, _ := ParseTrustedProxies([]string{"0.0.0.0/0"})
	if !all.broad() || good.broad() {
		t.Error("broad() should flag a range that trusts everything")
	}
}

func TestClientIP(t *testing.T) {
	proxies, _ := ParseTrustedProxies([]string{"127.0.0.1", "10.0.0.0/8", "::1"})
	s := &server{proxies: proxies}
	none := &server{}
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	cases := []struct {
		name string
		s    *server
		r    *http.Request
		want string
	}{
		{"no proxies configured: the header is ignored", none, req("127.0.0.1:5000", "203.0.113.9"), "127.0.0.1"},
		{"a spoofed header from an untrusted address is ignored", s, req("198.51.100.7:4000", "203.0.113.9"), "198.51.100.7"},
		{"untrusted address, a chain that names a trusted one", s, req("198.51.100.7:4000", "203.0.113.9, 127.0.0.1"), "198.51.100.7"},
		{"trusted proxy, no header", s, req("127.0.0.1:5000"), "127.0.0.1"},
		{"trusted proxy, one visitor", s, req("127.0.0.1:5000", "203.0.113.9"), "203.0.113.9"},
		{"what the visitor wrote sits to the left of what the proxy added", s, req("127.0.0.1:5000", "1.1.1.1, 2.2.2.2, 203.0.113.9"), "203.0.113.9"},
		{"a second trusted hop in the chain is skipped", s, req("127.0.0.1:5000", "203.0.113.9, 10.1.2.3"), "203.0.113.9"},
		{"several headers are read as one chain", s, req("127.0.0.1:5000", "1.1.1.1", "203.0.113.9"), "203.0.113.9"},
		{"every hop trusted: the connection", s, req("127.0.0.1:5000", "10.1.1.1, 10.2.2.2"), "127.0.0.1"},
		{"something that is not an address: do not guess", s, req("127.0.0.1:5000", "203.0.113.9, garbage"), "127.0.0.1"},
		{"an IPv6 visitor", s, req("[::1]:5000", "2001:db8::7"), "2001:db8::7"},
		{"an IPv4-mapped visitor is the plain address", s, req("127.0.0.1:5000", "::ffff:203.0.113.9"), "203.0.113.9"},
		{"spaces around entries", s, req("127.0.0.1:5000", "  203.0.113.9  "), "203.0.113.9"},
	}
	for _, c := range cases {
		if got := c.s.clientIP(c.r); got != c.want {
			t.Errorf("%s: clientIP = %q, want %q", c.name, got, c.want)
		}
	}
}

func login(p *panel, password, forwardedFor string) int {
	body, _ := json.Marshal(map[string]string{"password": password})
	return p.reqFrom(http.MethodPost, "/api/login", string(body), true, forwardedFor).code
}

// Without --trusted-proxy the header changes nothing: visitors behind a proxy
// share one address, and nobody can dodge the lockout by inventing addresses.
func TestLockoutIgnoresForwardedForByDefault(t *testing.T) {
	p := newPanel(t)
	for i, who := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5"} {
		if code := login(p, "nope", who); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, code)
		}
	}
	if code := login(p, p.password, "203.0.113.50"); code != http.StatusTooManyRequests {
		t.Errorf("five failures from five claimed addresses still lock the one real address: %d", code)
	}
}

func TestLockoutIsPerVisitorBehindATrustedProxy(t *testing.T) {
	p := newPanelBehind(t, "127.0.0.1")
	// Visitor A mistypes five times. The proxy appends A's address on the right;
	// the part on the left is whatever A sent, and changing it must not help.
	for i, spoof := range []string{"", "9.9.9.9, ", "8.8.8.8, ", "7.7.7.7, ", "6.6.6.6, "} {
		if code := login(p, "nope", spoof+"198.51.100.1"); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, code)
		}
	}
	if code := login(p, p.password, "5.5.5.5, 198.51.100.1"); code != http.StatusTooManyRequests {
		t.Errorf("visitor A is locked out however the header is dressed up: %d", code)
	}
	// Visitor B is not affected by A's typos.
	if code := login(p, p.password, "198.51.100.2"); code != http.StatusOK {
		t.Errorf("visitor B must still get in: %d", code)
	}
}

func TestSpoofedHeaderFromAnUntrustedAddressDoesNotEscapeTheLockout(t *testing.T) {
	// The test client connects from 127.0.0.1, which is not trusted here.
	p := newPanelBehind(t, "10.0.0.0/8")
	for i := 0; i < maxFailures; i++ {
		login(p, "nope", "10.1.1.1")
	}
	if code := login(p, p.password, "203.0.113.77"); code != http.StatusTooManyRequests {
		t.Errorf("a header from an untrusted address must be ignored: %d", code)
	}
}
