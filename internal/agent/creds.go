package agent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/itsahmadnyc/procly/internal/tlsid"
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
		host, _ := os.Hostname()
		if c.CertPEM, c.KeyPEM, err = tlsid.NewCert("procly agent " + host); err != nil {
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
	id.fingerprint = tlsid.Fingerprint(id.cert.Certificate[0])
	return id, nil
}
