package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionTTL   = 7 * 24 * time.Hour
	failWindow   = 10 * time.Minute
	maxFailures  = 5
	cookieName   = "procly_session"
	passwordSize = 15 // bytes of randomness, 20 characters encoded
)

// bcryptCost is a var only so tests can lower it; production uses 12.
var bcryptCost = 12

type webConfig struct {
	PasswordHash string `json:"password_hash"`
}

// auth holds the panel password hash, live sessions and failed logins.
// Sessions live in memory, so restarting `procly web` signs everyone out.
type auth struct {
	mu       sync.Mutex
	path     string // where the hash is stored, so a changed password survives a restart
	hash     []byte
	sessions map[string]time.Time
	fails    map[string][]time.Time
}

// loadAuth reads the password hash, creating a new random password on first
// run or when reset is true. The new password is returned once, in plain text,
// so it can be shown to the user; only the hash is stored.
func loadAuth(path string, reset bool) (*auth, string, error) {
	a := &auth{path: path, sessions: map[string]time.Time{}, fails: map[string][]time.Time{}}
	if !reset {
		data, err := os.ReadFile(path)
		if err == nil {
			var cfg webConfig
			if err := json.Unmarshal(data, &cfg); err != nil || cfg.PasswordHash == "" {
				return nil, "", errors.New(path + " is damaged, run 'procly web --reset-password'")
			}
			a.hash = []byte(cfg.PasswordHash)
			return a, "", nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}

	pw, err := randomString(passwordSize, base64.RawURLEncoding.EncodeToString)
	if err != nil {
		return nil, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return nil, "", err
	}
	data, _ := json.MarshalIndent(webConfig{PasswordHash: string(hash)}, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, "", err
	}
	a.hash = hash
	return a, pw, nil
}

func randomString(n int, encode func([]byte) string) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return encode(b), nil
}

func (a *auth) checkPassword(pw string) bool {
	return bcrypt.CompareHashAndPassword(a.hash, []byte(pw)) == nil
}

func (a *auth) newSession() (string, error) {
	token, err := randomString(32, hex.EncodeToString)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for t, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, t)
		}
	}
	a.sessions[token] = now.Add(sessionTTL)
	return token, nil
}

func (a *auth) validSession(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.sessions, token)
		return false
	}
	return true
}

func (a *auth) endSession(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// allowLogin reports whether ip may try a password, and how long to wait if not.
func (a *auth) allowLogin(ip string) (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	recent := a.pruneLocked(ip)
	if len(recent) < maxFailures {
		return true, 0
	}
	return false, time.Until(recent[0].Add(failWindow))
}

func (a *auth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fails[ip] = append(a.pruneLocked(ip), time.Now())
}

func (a *auth) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, ip)
}

func (a *auth) pruneLocked(ip string) []time.Time {
	cutoff := time.Now().Add(-failWindow)
	kept := a.fails[ip][:0]
	for _, t := range a.fails[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(a.fails, ip)
		return nil
	}
	a.fails[ip] = kept
	return kept
}

// Password rules for one a person chooses: long enough to resist guessing, and
// short enough for bcrypt, which only reads the first 72 bytes.
const (
	minPasswordLen = 10
	maxPasswordLen = 72
)

var (
	errWrongPassword = errors.New("the current password is wrong")
	errWeakPassword  = errors.New("the new password needs at least 10 characters")
	errLongPassword  = errors.New("the new password can be at most 72 characters")
	errSamePassword  = errors.New("the new password is the same as the current one")
)

// changePassword checks the current password, stores the hash of the new one
// and ends every session except keep, the one that asked. The file is
// replaced in one step, so a crash leaves the old password or the new one.
func (a *auth) changePassword(current, next, keep string) (ended int, err error) {
	if !a.checkPassword(current) {
		return 0, errWrongPassword
	}
	switch {
	case len([]rune(next)) < minPasswordLen:
		return 0, errWeakPassword
	case len(next) > maxPasswordLen:
		return 0, errLongPassword
	case next == current:
		return 0, errSamePassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcryptCost)
	if err != nil {
		return 0, err
	}
	data, _ := json.MarshalIndent(webConfig{PasswordHash: string(hash)}, "", "  ")
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, a.path); err != nil {
		return 0, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hash = hash
	for t := range a.sessions {
		if t != keep {
			delete(a.sessions, t)
			ended++
		}
	}
	return ended, nil
}
