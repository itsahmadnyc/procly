package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// selfSigned makes a certificate for 127.0.0.1 with the given serial number, so
// a test can tell which one a server presented.
func selfSigned(t *testing.T, serial int64) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "procly test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true, BasicConstraintsValid: true, // it signs itself, so a client can trust it directly
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func writePair(t *testing.T, dir string, serial int64) (certFile, keyFile string, certPEM []byte) {
	t.Helper()
	c, k := selfSigned(t, serial)
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, c, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, k, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, c
}

// serialSeenBy does a TLS handshake with addr and returns the serial number of
// the certificate it presents.
func serialSeenBy(t *testing.T, addr string) int64 {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) // only the serial is read
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

// tlsServer is a panel-like server using ServeTLS with the reloader, the way Run does.
type tlsServer struct{ URL string }

func startTLS(t *testing.T, r *certReloader, h http.Handler) *tlsServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, TLSConfig: r.config(), ErrorLog: log.New(io.Discard, "", 0)}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return &tlsServer{URL: "https://" + ln.Addr().String()}
}

func TestCertReloaderLoadsAndRefusesMistakes(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writePair(t, dir, 1)
	if _, err := newCertReloader(certFile, keyFile); err != nil {
		t.Fatalf("a good pair: %v", err)
	}
	if _, err := newCertReloader(filepath.Join(dir, "missing.pem"), keyFile); err == nil || !strings.Contains(err.Error(), "missing.pem") {
		t.Errorf("a missing certificate: %v", err)
	}
	// A key that does not belong to the certificate.
	_, otherKey := selfSigned(t, 2)
	os.WriteFile(keyFile, otherKey, 0o600)
	if _, err := newCertReloader(certFile, keyFile); err == nil || !strings.Contains(err.Error(), "tls: cannot use") {
		t.Errorf("a key that does not match: %v", err)
	}
	os.WriteFile(certFile, []byte("not a certificate"), 0o600)
	if _, err := newCertReloader(certFile, keyFile); err == nil {
		t.Error("garbage")
	}
}

func TestCertReloaderServesTheNewCertificateAfterReload(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writePair(t, dir, 1)
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := startTLS(t, r, http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "https://")
	if got := serialSeenBy(t, addr); got != 1 {
		t.Fatalf("serial = %d", got)
	}
	writePair(t, dir, 2)
	if got := serialSeenBy(t, addr); got != 1 {
		t.Errorf("replacing the files changes nothing until a reload: %d", got)
	}
	if err := r.reload(); err != nil {
		t.Fatal(err)
	}
	if got := serialSeenBy(t, addr); got != 2 {
		t.Errorf("after reload serial = %d, want 2", got)
	}

	// A broken replacement is refused and the good certificate stays.
	os.WriteFile(certFile, []byte("broken"), 0o600)
	if err := r.reload(); err == nil {
		t.Error("a broken certificate must be refused")
	}
	if got := serialSeenBy(t, addr); got != 2 {
		t.Errorf("the last good certificate keeps being served: %d", got)
	}
}

type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}
func (l *logLines) all() string { l.mu.Lock(); defer l.mu.Unlock(); return strings.Join(l.lines, "\n") }

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A renewal writes the certificate and then the key. In between they do not
// match: that must not take the panel down, and the finished pair must be picked up.
func TestWatchPicksUpARenewalWrittenInTwoSteps(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, _ := writePair(t, dir, 1)
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := startTLS(t, r, http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "https://")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &logLines{}
	go r.watch(ctx, 20*time.Millisecond, log.logf)

	newCert, newKey := selfSigned(t, 2)
	os.WriteFile(certFile, newCert, 0o600) // the new certificate, with the old key still in place
	eventually(t, "the mismatch to be reported", func() bool { return strings.Contains(log.all(), "still serving the previous certificate") })
	if got := serialSeenBy(t, addr); got != 1 {
		t.Errorf("a half-finished renewal must not change what is served: %d", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(log.all(), "still serving"); n != 1 {
		t.Errorf("one failed change is reported once, not on every look: %d times\n%s", n, log.all())
	}

	os.WriteFile(keyFile, newKey, 0o600) // the renewal finishes
	eventually(t, "the new certificate", func() bool { return serialSeenBy(t, addr) == 2 })
	if !strings.Contains(log.all(), "loaded the new certificate") {
		t.Errorf("the log should say it was loaded:\n%s", log.all())
	}
}

func TestWatchFollowsASymlinkToANewCertificate(t *testing.T) {
	// certbot keeps live/ as symlinks into archive/ and moves them on renewal.
	dir := t.TempDir()
	for i, serial := range []int64{1, 2} {
		d := filepath.Join(dir, "archive", string(rune('a'+i)))
		os.MkdirAll(d, 0o700)
		writePair(t, d, serial)
	}
	live := filepath.Join(dir, "live")
	os.Symlink(filepath.Join(dir, "archive", "a"), live)
	certFile, keyFile := filepath.Join(live, "cert.pem"), filepath.Join(live, "key.pem")
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := startTLS(t, r, http.NotFoundHandler())
	addr := strings.TrimPrefix(srv.URL, "https://")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.watch(ctx, 20*time.Millisecond, func(string, ...any) {})

	tmp := filepath.Join(dir, "live.new")
	os.Symlink(filepath.Join(dir, "archive", "b"), tmp)
	if err := os.Rename(tmp, live); err != nil { // an atomic switch, as certbot does
		t.Fatal(err)
	}
	eventually(t, "the certificate behind the symlink", func() bool { return serialSeenBy(t, addr) == 2 })
}

// The real panel over TLS: pages load, the session cookie is Secure, and plain HTTP is not spoken.
func TestPanelOverTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, certPEM := writePair(t, dir, 1)
	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	p := newPanel(t) // for its backend and password; its own plain server is not used
	home := os.Getenv("PROCLY_HOME")
	a, _, err := loadAuth(filepath.Join(home, "web.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	s := &server{servers: newRegistry(p.backend), auth: a, host: "test-host"}
	srv := startTLS(t, r, s.routes())

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	res, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("a client that trusts the certificate: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK || res.TLS == nil {
		t.Fatalf("page: %d tls=%v", res.StatusCode, res.TLS != nil)
	}
	if _, err := (&http.Client{Timeout: 5 * time.Second}).Get(srv.URL + "/"); err == nil {
		t.Error("a client that does not trust the certificate must be refused")
	}

	body, _ := json.Marshal(map[string]string{"password": p.password})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(string(body)))
	req.Header.Set("X-Procly", "1")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var secure bool
	for _, c := range res.Cookies() {
		if c.Name == cookieName {
			secure = c.Secure && c.HttpOnly
		}
	}
	if res.StatusCode != http.StatusOK || !secure {
		t.Errorf("login over TLS: %d, cookie secure+httponly: %v", res.StatusCode, secure)
	}
}
