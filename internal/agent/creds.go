package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"time"
)

// creds is what the agent keeps on disk (agent.json, mode 0600). The token
// itself is never stored, only its SHA-256.
type creds struct {
	TokenHash string `json:"token_hash"`
	CertPEM   string `json:"cert"`
	KeyPEM    string `json:"key"`
}

type identity struct {
	cert        tls.Certificate
	tokenHash   []byte
	fingerprint string
	newToken    string // set only when a token was just created
}

// Fingerprint is how hubs pin an agent's certificate: sha256 of the DER bytes.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func loadIdentity(path string, resetToken bool) (*identity, error) {
	var c creds
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, errors.New(path + " is damaged; delete it to start over")
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}

	id := &identity{}
	changed := false
	if c.CertPEM == "" || c.KeyPEM == "" {
		if c.CertPEM, c.KeyPEM, err = newCert(); err != nil {
			return nil, err
		}
		changed = true
	}
	if c.TokenHash == "" || resetToken {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		id.newToken = base64.RawURLEncoding.EncodeToString(raw)
		sum := sha256.Sum256([]byte(id.newToken))
		c.TokenHash = hex.EncodeToString(sum[:])
		changed = true
	}
	if changed {
		out, _ := json.MarshalIndent(c, "", "  ")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, out, 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, err
		}
	}

	if id.cert, err = tls.X509KeyPair([]byte(c.CertPEM), []byte(c.KeyPEM)); err != nil {
		return nil, err
	}
	if id.tokenHash, err = hex.DecodeString(c.TokenHash); err != nil {
		return nil, errors.New(path + " has a bad token hash; run with --reset-token")
	}
	id.fingerprint = Fingerprint(id.cert.Certificate[0])
	return id, nil
}

// newCert makes a self-signed ECDSA P-256 certificate. Hubs trust it by
// fingerprint, so names and CAs don't matter.
func newCert() (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "procly agent " + host},
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
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}
