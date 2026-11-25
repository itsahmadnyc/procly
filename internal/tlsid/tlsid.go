// Package tlsid makes self-signed TLS identities and pins them by
// fingerprint. procly never uses certificate authorities: each side is
// told the other's SHA-256 fingerprint once, at setup.
package tlsid

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"
)

// ErrMismatch means the peer presented a different certificate than the pinned one.
var ErrMismatch = errors.New("certificate fingerprint does not match; the other side was reinstalled or someone is in the middle")

// Fingerprint is sha256 of a certificate's DER bytes, as "sha256:<hex>".
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Normalize accepts "sha256:ab12...", "AB:12:...", or plain hex.
func Normalize(fp string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(fp))
	h = strings.TrimPrefix(h, "sha256:")
	h = strings.ReplaceAll(h, ":", "")
	if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
		return "", fmt.Errorf("fingerprint must be a sha256 printed by procly, got %q", fp)
	}
	return "sha256:" + h, nil
}

// Pinned returns a client TLS config that accepts exactly one certificate.
func Pinned(fingerprint string) *tls.Config {
	want, _ := Normalize(fingerprint)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// CA verification is replaced by the fingerprint check below.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || want == "" {
				return ErrMismatch
			}
			got := Fingerprint(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return ErrMismatch
			}
			return nil
		},
	}
}

// NewCert makes a self-signed ECDSA P-256 server certificate valid for 10 years.
func NewCert(commonName string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})), nil
}

type stored struct {
	CertPEM string `json:"cert"`
	KeyPEM  string `json:"key"`
}

// LoadOrCreate reads a certificate+key JSON file (mode 0600), creating it
// on first use.
func LoadOrCreate(path, commonName string) (tls.Certificate, string, error) {
	var s stored
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s); err != nil || s.CertPEM == "" {
			return tls.Certificate{}, "", errors.New(path + " is damaged; delete it to make a new identity")
		}
	case errors.Is(err, os.ErrNotExist):
		if s.CertPEM, s.KeyPEM, err = NewCert(commonName); err != nil {
			return tls.Certificate{}, "", err
		}
		out, _ := json.MarshalIndent(s, "", "  ")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0o600); err != nil {
			return tls.Certificate{}, "", err
		}
		if err := os.Rename(tmp, path); err != nil {
			return tls.Certificate{}, "", err
		}
	default:
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair([]byte(s.CertPEM), []byte(s.KeyPEM))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, Fingerprint(cert.Certificate[0]), nil
}

// TokenHash is how stored tokens are kept: sha256, hex.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenMatches compares a presented token with a stored hash in constant time.
func TokenMatches(token, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(TokenHash(token)), []byte(hash)) == 1
}
