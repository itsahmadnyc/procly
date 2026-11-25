package tlsid

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	hex := strings.Repeat("a1", 32)
	want := "sha256:" + hex
	upperColons := strings.ToUpper(strings.Join(splitEvery(hex, 2), ":"))
	for _, in := range []string{want, hex, "SHA256:" + strings.ToUpper(hex), upperColons, "  " + want + "\n"} {
		got, err := Normalize(in)
		if err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "sha256:", hex[:62], hex + "00", "sha256:" + hex[:62] + "zz", "md5:" + hex} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("Normalize(%q) should fail", in)
		}
	}
}

func splitEvery(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

func TestPinnedAcceptsOnlyThatCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	fp := Fingerprint(srv.Certificate().Raw)

	get := func(fingerprint string) error {
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: Pinned(fingerprint)}, Timeout: 5 * time.Second}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(fp); err != nil {
		t.Fatalf("pinned fingerprint refused: %v", err)
	}
	if err := get("sha256:" + strings.Repeat("00", 32)); !errors.Is(err, ErrMismatch) {
		t.Fatalf("other fingerprint: want ErrMismatch, got %v", err)
	}
	// A broken fingerprint must fail closed, not fall back to "accept anything".
	if err := get("not-a-fingerprint"); !errors.Is(err, ErrMismatch) {
		t.Fatalf("invalid fingerprint: want ErrMismatch, got %v", err)
	}
}

func TestPinnedRequiresTLS13(t *testing.T) {
	if Pinned("sha256:"+strings.Repeat("00", 32)).MinVersion != tls.VersionTLS13 {
		t.Fatal("pinned connections must use TLS 1.3")
	}
}

func TestNewCertIsAServerCertificate(t *testing.T) {
	certPEM, keyPEM, err := NewCert("procly test")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject.CommonName != "procly test" || c.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
		t.Fatalf("unexpected certificate: %s, valid until %s", c.Subject.CommonName, c.NotAfter)
	}
	if len(c.ExtKeyUsage) != 1 || c.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatal("certificate should be for server auth only")
	}
	other, _, _ := NewCert("procly test")
	if other == certPEM {
		t.Fatal("two certificates came out identical")
	}
}

func TestLoadOrCreateIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.json")
	_, fp1, err := LoadOrCreate(path, "hub")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode %v, want 0600 (it holds the private key)", fi.Mode().Perm())
	}
	_, fp2, err := LoadOrCreate(path, "hub")
	if err != nil || fp1 != fp2 {
		t.Fatalf("second load changed the identity: %s vs %s (%v)", fp1, fp2, err)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(path, "hub"); err == nil {
		t.Fatal("a damaged identity file was silently replaced; that would change the fingerprint under everyone")
	}
}

func TestTokenHash(t *testing.T) {
	h := TokenHash("secret")
	if len(h) != 64 || strings.Contains(h, "secret") {
		t.Fatalf("hash %q", h)
	}
	if !TokenMatches("secret", h) {
		t.Fatal("matching token rejected")
	}
	for _, wrong := range []string{"", "Secret", "secret ", "secre"} {
		if TokenMatches(wrong, h) {
			t.Errorf("%q accepted", wrong)
		}
	}
	if TokenMatches("secret", "") {
		t.Fatal("empty stored hash must never match")
	}
}
