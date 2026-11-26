package web

import (
	"context"
	"sync"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/sysinfo"
)

// backend is one server the panel can manage: this machine through the
// daemon socket, or another machine through its agent.
type backend interface {
	List(ctx context.Context) ([]proc.View, error)
	Action(ctx context.Context, name, verb string) error
	Save(ctx context.Context) (int, error)
	Logs(ctx context.Context, name string, send func([]logtail.Event) bool) error
	System(ctx context.Context) (sysinfo.System, error)
}

// starter is a backend that can start new commands. Only the local one is:
// agents refuse new commands on purpose.
type starter interface {
	Start(ctx context.Context, spec proc.Spec) error
}

type localBackend struct{ c *client.Client }

func (l localBackend) Start(_ context.Context, spec proc.Spec) error {
	_, err := l.c.Start(spec)
	return err
}

func (l localBackend) List(context.Context) ([]proc.View, error) {
	infos, err := l.c.List()
	if err != nil {
		return nil, errDaemonDown
	}
	return proc.NewViews(infos), nil
}

func (l localBackend) Action(_ context.Context, name, verb string) error {
	var err error
	switch verb {
	case "restart", "start":
		_, err = l.c.Restart(name)
	case "stop":
		_, err = l.c.Stop(name)
	case "reload":
		_, err = l.c.Reload(name)
	case "delete":
		_, err = l.c.Delete(name)
	}
	return err
}

// System reads this machine directly: the panel runs where the daemon runs.
func (l localBackend) System(context.Context) (sysinfo.System, error) { return sysinfo.Read(), nil }

func (l localBackend) Save(context.Context) (int, error) {
	n, _, err := l.c.Save()
	return n, err
}

func (l localBackend) Logs(ctx context.Context, name string, send func([]logtail.Event) bool) error {
	// Paths come from the daemon, never from the request.
	info, err := l.c.Get(name)
	if err != nil {
		return err
	}
	logtail.Stream(ctx, info.OutLog, info.ErrLog, 300, send)
	return nil
}

// registry hands out backends by server name. servers.json is re-read on
// every lookup so `procly server add` takes effect without a restart;
// clients are cached so connections are reused.
type registry struct {
	local backend
	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	srv    remote.Server
	client *remote.Client
}

type serverEntry struct {
	name    string
	backend backend
	url     string
}

func newRegistry(local backend) *registry {
	return &registry{local: local, cache: map[string]cached{}}
}

// all returns local first, then remote servers by name.
func (r *registry) all() ([]serverEntry, error) {
	list, err := remote.Load()
	entries := []serverEntry{{name: remote.LocalName, backend: r.local}}
	if err != nil {
		return entries, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range list {
		seen[s.Name] = true
		c, ok := r.cache[s.Name]
		if !ok || c.srv != s {
			c = cached{srv: s, client: remote.NewClient(s)}
			r.cache[s.Name] = c
		}
		where := s.URL
		if s.Mode == remote.ModeTunnel {
			where = "tunnel"
		}
		entries = append(entries, serverEntry{name: s.Name, backend: c.client, url: where})
	}
	for name := range r.cache {
		if !seen[name] {
			delete(r.cache, name)
		}
	}
	return entries, nil
}

func (r *registry) get(name string) (backend, bool) {
	entries, _ := r.all()
	for _, e := range entries {
		if e.name == name {
			return e.backend, true
		}
	}
	return nil, false
}
