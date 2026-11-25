package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Hashing at the production cost makes these tests take a minute under -race.
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

func TestProductionBcryptCost(t *testing.T) {
	// TestMain lowered it; check the source value stays strong.
	data, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "var bcryptCost = 12") {
		t.Fatal("production bcrypt cost changed from 12")
	}
}

func TestLoadAuthCreatesPasswordOnceAndStoresOnlyAHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.json")
	a, pw, err := loadAuth(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 20 {
		t.Fatalf("generated password %q is too short", pw)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), pw) {
		t.Fatal("web.json contains the plain password")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("web.json mode %v", fi.Mode().Perm())
	}
	if !a.checkPassword(pw) || a.checkPassword(pw+"x") || a.checkPassword("") {
		t.Fatal("password check is wrong")
	}

	again, pw2, err := loadAuth(path, false)
	if err != nil || pw2 != "" || !again.checkPassword(pw) {
		t.Fatalf("second start should reuse the password and not print it (%q, %v)", pw2, err)
	}

	reset, pw3, err := loadAuth(path, true)
	if err != nil || pw3 == "" || pw3 == pw || reset.checkPassword(pw) || !reset.checkPassword(pw3) {
		t.Fatal("--reset-password should replace the old password")
	}
}

func TestLoadAuthRefusesDamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.json")
	if err := os.WriteFile(path, []byte(`{"password_hash":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAuth(path, false); err == nil {
		t.Fatal("an empty hash must not mean 'no password'")
	}
}

func TestSessions(t *testing.T) {
	a := &auth{sessions: map[string]time.Time{}, fails: map[string][]time.Time{}}
	tok, err := a.newSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("session token %q should be 32 random bytes in hex", tok)
	}
	if !a.validSession(tok) || a.validSession("") || a.validSession(tok[:63]) {
		t.Fatal("session check is wrong")
	}
	other, _ := a.newSession()
	if other == tok {
		t.Fatal("two sessions got the same token")
	}

	a.endSession(tok)
	if a.validSession(tok) {
		t.Fatal("ended session still valid")
	}

	a.sessions[other] = time.Now().Add(-time.Second)
	if a.validSession(other) {
		t.Fatal("expired session still valid")
	}
	if _, ok := a.sessions[other]; ok {
		t.Fatal("expired session not cleaned up")
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	a := &auth{sessions: map[string]time.Time{}, fails: map[string][]time.Time{}}
	for i := 0; i < maxFailures; i++ {
		if ok, _ := a.allowLogin("1.2.3.4"); !ok {
			t.Fatalf("blocked after %d failures, limit is %d", i, maxFailures)
		}
		a.recordFailure("1.2.3.4")
	}
	ok, wait := a.allowLogin("1.2.3.4")
	if ok || wait <= 0 || wait > failWindow {
		t.Fatalf("after %d failures: ok=%v wait=%s", maxFailures, ok, wait)
	}
	if ok, _ := a.allowLogin("5.6.7.8"); !ok {
		t.Fatal("another IP was blocked too")
	}

	// Old failures fall out of the window.
	for i := range a.fails["1.2.3.4"] {
		a.fails["1.2.3.4"][i] = time.Now().Add(-failWindow - time.Second)
	}
	if ok, _ := a.allowLogin("1.2.3.4"); !ok {
		t.Fatal("failures older than the window still count")
	}

	a.recordFailure("9.9.9.9")
	a.clearFailures("9.9.9.9")
	if len(a.fails["9.9.9.9"]) != 0 {
		t.Fatal("a successful login should clear failures")
	}
}
