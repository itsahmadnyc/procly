package remote

import (
	"context"
	"crypto/sha256"
	hexenc "encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fingerprintOf(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	sum := sha256.Sum256(srv.Certificate().Raw)
	fp, err := normalizeFingerprint(hexenc.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestClientPinsCertificate(t *testing.T) {
	var gotAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"version":"test","host":"box","daemon":true}`))
	}))
	defer srv.Close()

	good := Server{Name: "box", URL: srv.URL, Token: "secret", Fingerprint: fingerprintOf(t, srv)}
	ping, err := NewClient(good).Ping(context.Background())
	if err != nil {
		t.Fatalf("pinned fingerprint should connect: %v", err)
	}
	if ping.Host != "box" || gotAuth != "Bearer secret" {
		t.Fatalf("got host %q auth %q", ping.Host, gotAuth)
	}

	bad := good
	bad.Fingerprint = "sha256:" + strings.Repeat("ab", 32)
	gotAuth = ""
	if _, err := NewClient(bad).Ping(context.Background()); !errors.Is(err, ErrFingerprint) {
		t.Fatalf("wrong fingerprint must fail with ErrFingerprint, got %v", err)
	}
	if gotAuth != "" {
		t.Fatal("token was sent to a server with the wrong certificate")
	}
}

func TestStatusErrors(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer srv.Close()
	s := Server{Name: "box", URL: srv.URL, Token: "x", Fingerprint: fingerprintOf(t, srv)}
	_, err := NewClient(s).List(context.Background())
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 401 || se.Message != "bad token" {
		t.Fatalf("want 401 StatusError, got %v", err)
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	hex := strings.Repeat("0f", 32)
	cases := map[string]bool{
		"sha256:" + hex:                          true,
		hex:                                      true,
		strings.ToUpper(hex):                     true,
		"SHA256:" + strings.ToUpper(hex):         true,
		"0F:" + strings.Repeat("0F:", 30) + "0F": true,
		"sha256:" + hex[:62]:                     false,
		"sha256:" + hex[:62] + "zz":              false,
		"":                                       false,
	}
	for in, ok := range cases {
		out, err := normalizeFingerprint(in)
		if ok && (err != nil || out != "sha256:"+hex) {
			t.Errorf("%q: got %q, %v", in, out, err)
		}
		if !ok && err == nil {
			t.Errorf("%q: expected an error", in)
		}
	}
}

func TestServerValidate(t *testing.T) {
	ok := Server{Name: "prod-2", URL: "https://10.0.0.2:4748", Token: "t", Fingerprint: strings.Repeat("ab", 32)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Server{
		{Name: "local", URL: ok.URL, Token: "t", Fingerprint: ok.Fingerprint},
		{Name: "Prod", URL: ok.URL, Token: "t", Fingerprint: ok.Fingerprint},
		{Name: "p", URL: "http://10.0.0.2:4748", Token: "t", Fingerprint: ok.Fingerprint},
		{Name: "p", URL: "https://10.0.0.2:4748/x", Token: "t", Fingerprint: ok.Fingerprint},
		{Name: "p", URL: ok.URL, Token: "", Fingerprint: ok.Fingerprint},
	} {
		if bad.Validate() == nil {
			t.Errorf("expected %+v to be rejected", bad)
		}
	}
}
