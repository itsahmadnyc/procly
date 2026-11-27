package daemon

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/proc"
)

// domainOptions is where the proxies' configuration is looked for; a test
// points it at a temporary directory.
var domainOptions = domains.DefaultOptions

// domainsCache keeps the last answer for a few seconds: the panel and the
// terminal dashboard both ask on a timer, and reading the proxies'
// configuration and every process's sockets is not free.
type domainsCache struct {
	mu  sync.Mutex
	at  time.Time
	rep domains.Report
}

const domainsTTL = 15 * time.Second

func (c *domainsCache) get(ctx context.Context, m *proc.Manager, refresh bool) domains.Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !refresh && !c.at.IsZero() && time.Since(c.at) < domainsTTL {
		return c.rep
	}
	c.rep = domains.Build(ctx, domains.Read(domainOptions()), m.PortNumbers(), true)
	c.at = time.Now()
	return c.rep
}

func (c *domainsCache) handler(m *proc.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, c.get(ctx, m, r.URL.Query().Get("refresh") == "1"))
	}
}
