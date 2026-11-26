package web

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// certReloader serves a certificate and key from files and picks up a renewed
// pair without a restart. A pair that cannot be loaded (half written, wrong
// key, garbage) is refused and the last good one keeps being served.
type certReloader struct {
	certFile, keyFile string
	current           atomic.Pointer[tls.Certificate]

	// Only the watcher goroutine touches these.
	good, failed fileStamp
}

// fileStamp says whether either file changed: a hash of what the two paths read
// now. Not modification time and size: two certificates written in the same
// instant look alike to those, and a symlink moved to a new certificate must
// count however its files are dated.
type fileStamp struct{ cert, key [sha256.Size]byte }

func stampOf(certFile, keyFile string) fileStamp {
	var s fileStamp
	if b, err := os.ReadFile(certFile); err == nil {
		s.cert = sha256.Sum256(b)
	}
	if b, err := os.ReadFile(keyFile); err == nil {
		s.key = sha256.Sum256(b)
	}
	return s
}

// newCertReloader loads the pair now, so a mistake stops the panel at start.
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	r.good = stampOf(certFile, keyFile)
	return r, nil
}

// reload reads the files again. On failure the current certificate is kept.
func (r *certReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("tls: cannot use %s and %s: %v", r.certFile, r.keyFile, err)
	}
	r.current.Store(&cert)
	return nil
}

// GetCertificate is tls.Config.GetCertificate.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.current.Load(), nil
}

// watch looks at the files every interval until ctx ends and reloads when they
// change, calling logf with what it did. A change that fails to load is tried
// again whenever the files change again (a renewal writes the certificate and
// the key one after the other), and reported once.
func (r *certReloader) watch(ctx context.Context, every time.Duration, logf func(format string, args ...any)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := stampOf(r.certFile, r.keyFile)
		if now == r.good || now == r.failed {
			continue
		}
		if err := r.reload(); err != nil {
			r.failed = now
			logf("%v; still serving the previous certificate", err)
			continue
		}
		r.good = now
		logf("tls: loaded the new certificate from %s", r.certFile)
	}
}

// config is the server's TLS configuration.
func (r *certReloader) config() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: r.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
}
