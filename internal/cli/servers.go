package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/itsahmadnyc/procly/internal/agent"
	"github.com/itsahmadnyc/procly/internal/hub"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

func cmdAgent(argv []string) error {
	if len(argv) > 0 {
		switch argv[0] {
		case "join":
			return cmdAgentJoin(argv[1:])
		case "connect":
			if len(argv) > 1 {
				return errors.New("usage: procly agent connect (settings come from 'procly agent join')")
			}
			if _, err := ensureDaemon(); err != nil {
				return err
			}
			return agent.Connect(os.Stdout)
		}
	}
	a, err := parse(argv, flagSet{
		valued: set("addr", "port"),
		bools:  set("reset-token"),
	})
	if err != nil {
		return err
	}
	addr := agent.DefaultAddr
	if v := a.get("addr"); v != "" {
		addr = v
	}
	if v := a.get("port"); v != "" {
		if a.get("addr") != "" {
			return errors.New("use either --addr or --port, not both")
		}
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			return errors.New("--port must be between 1 and 65535")
		}
		addr = "0.0.0.0:" + v
	}
	if _, err := ensureDaemon(); err != nil {
		return err
	}
	return agent.Run(agent.Options{Addr: addr, ResetToken: a.bools["reset-token"], Out: os.Stdout})
}

const serverUsage = `usage:
  procly server add <name> <https://host:4748> --token <token> --fingerprint <sha256:...>
  procly server invite <name> [--hub-address <host:4749>]
  procly server ls
  procly server rm <name>`

func cmdAgentJoin(argv []string) error {
	a, err := parse(argv, flagSet{valued: set("name", "fingerprint", "token")})
	if err != nil {
		return err
	}
	if len(a.pos) != 1 || a.get("name") == "" || a.get("fingerprint") == "" || a.get("token") == "" {
		return errors.New("usage: procly agent join <hub-host:4749> --name <name> --fingerprint <sha256:...> --token <token>\n  Copy it from 'procly server invite' on the hub.")
	}
	cfg := agent.JoinConfig{Hub: a.pos[0], Name: a.get("name"), Fingerprint: a.get("fingerprint"), Token: a.get("token")}
	if err := agent.Join(cfg); err != nil {
		var rej *agent.ErrRejected
		switch {
		case errors.Is(err, remote.ErrFingerprint):
			return errors.New("the hub's certificate fingerprint doesn't match; copy the command again from 'procly server invite'")
		case errors.As(err, &rej):
			return err
		}
		return fmt.Errorf("%v\n  Is 'procly hub' running there, and is port %s reachable from this server?", err, portFrom(cfg.Hub))
	}
	fmt.Printf("  %s %s as %s\n", green("joined"), bold(cfg.Hub), bold(cfg.Name))
	fmt.Println(dim("  Settings saved to ~/.procly/agent-connect.json. Keep the agent connected with:"))
	fmt.Print("\n  procly start \"procly agent connect\" --name procly-agent\n\n")
	return nil
}

func portFrom(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return hub.DefaultPort
}

func cmdHub(argv []string) error {
	a, err := parse(argv, flagSet{valued: set("addr", "port")})
	if err != nil {
		return err
	}
	addr := hub.DefaultAddr
	if v := a.get("addr"); v != "" {
		addr = v
	}
	if v := a.get("port"); v != "" {
		if a.get("addr") != "" {
			return errors.New("use either --addr or --port, not both")
		}
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			return errors.New("--port must be between 1 and 65535")
		}
		addr = "0.0.0.0:" + v
	}
	return hub.Run(hub.Options{Addr: addr, Out: os.Stdout})
}

// cmdServerInvite registers a tunnel server and prints what to run on it.
func cmdServerInvite(argv []string) error {
	a, err := parse(argv, flagSet{valued: set("hub-address")})
	if err != nil {
		return err
	}
	if len(a.pos) != 1 {
		return errors.New("usage: procly server invite <name> [--hub-address <host:4749>]")
	}
	name := a.pos[0]
	if err := remote.ValidName(name); err != nil {
		return err
	}
	existing, ok, err := remote.Get(name)
	if err != nil {
		return err
	}
	if ok && existing.Mode != remote.ModeTunnel {
		return fmt.Errorf("%s is already added as a direct server; remove it first with 'procly server rm %s'", name, name)
	}
	_, fp, err := hub.Identity()
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := remote.Add(remote.Server{Name: name, Mode: remote.ModeTunnel, TokenHash: tlsid.TokenHash(token)}); err != nil {
		return err
	}
	addr := a.get("hub-address")
	if addr == "" {
		addr = "<this-server-address>:" + hub.DefaultPort
	}
	if ok {
		fmt.Printf("  %s %s with a new token; its old connection will be dropped.\n", yellow("re-invited"), bold(name))
	} else {
		fmt.Printf("  %s %s\n", green("invited"), bold(name))
	}
	fmt.Printf("\n  On %s run:\n\n", name)
	fmt.Printf("  procly agent join %s --name %s \\\n    --fingerprint %s \\\n    --token %s\n", addr, name, fp, token)
	fmt.Println("  procly start \"procly agent connect\" --name procly-agent")
	fmt.Println(dim("\n  The token is shown once; only its hash is kept here."))
	fmt.Println(dim("  This server must run the hub and accept port " + portFrom(addr) + ": procly start \"procly hub\" --name procly-hub"))
	fmt.Println()
	return nil
}

func cmdServer(argv []string) error {
	if len(argv) == 0 {
		return errors.New(serverUsage)
	}
	switch argv[0] {
	case "add":
		return cmdServerAdd(argv[1:])
	case "invite":
		return cmdServerInvite(argv[1:])
	case "ls", "list":
		return cmdServerList()
	case "rm", "remove", "delete":
		if len(argv) != 2 {
			return errors.New("usage: procly server rm <name>")
		}
		if err := remote.Remove(argv[1]); err != nil {
			return err
		}
		fmt.Printf("  %s %s\n", green("removed"), bold(argv[1]))
		return nil
	default:
		return errors.New(serverUsage)
	}
}

func cmdServerAdd(argv []string) error {
	a, err := parse(argv, flagSet{valued: set("token", "fingerprint")})
	if err != nil {
		return err
	}
	if len(a.pos) != 2 || a.get("token") == "" || a.get("fingerprint") == "" {
		return errors.New(serverUsage)
	}
	s := remote.Server{Name: a.pos[0], URL: a.pos[1], Token: a.get("token"), Fingerprint: a.get("fingerprint")}
	if err := s.Validate(); err != nil {
		return err
	}

	// Check before saving, so a typo doesn't end up in the list.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ping, err := remote.NewClient(s).Ping(ctx)
	if err != nil {
		var se *remote.StatusError
		switch {
		case errors.Is(err, remote.ErrFingerprint):
			return errors.New("the certificate fingerprint doesn't match; copy it again from 'procly agent' on that server")
		case errors.As(err, &se) && se.Code == 401:
			return errors.New("the agent rejected the token; copy it again, or make a new one with 'procly agent --reset-token'")
		}
		return fmt.Errorf("can't reach %s: %v\n  Is 'procly agent' running there, and is the port open to this server?", s.URL, err)
	}
	if err := remote.Add(s); err != nil {
		return err
	}
	fmt.Printf("  %s %s %s\n", green("added"), bold(s.Name), dim(fmt.Sprintf("(%s, procly %s)", ping.Host, ping.Version)))
	if !ping.Daemon {
		fmt.Println(dim("  The agent is up but its daemon isn't; it starts with the first 'procly start' there."))
	}
	fmt.Println(dim("  It shows up in 'procly web' right away."))
	return nil
}

func cmdServerList() error {
	list, err := remote.Load()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println(dim("\n  No servers yet. Reachable server: run 'procly agent' there, then 'procly server add' here."))
		fmt.Println(dim("  Behind NAT: run 'procly hub' here, then 'procly server invite <name>'.\n"))
		return nil
	}
	type row struct {
		status, procs, host, version string
		ok                           bool
	}
	rows := make([]row, len(list))
	var wg sync.WaitGroup
	for i, s := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c := remote.NewClient(s)
			ping, err := c.Ping(ctx)
			if err != nil {
				rows[i] = row{status: "● unreachable", procs: "-", host: "-", version: "-"}
				var se *remote.StatusError
				switch {
				case errors.Is(err, remote.ErrFingerprint):
					rows[i].status = "● bad fingerprint"
				case errors.Is(err, remote.ErrHubDown):
					rows[i].status = "● hub not running"
				case errors.As(err, &se) && se.Code == 502 && s.Mode == remote.ModeTunnel:
					rows[i].status = "● not connected"
				}
				return
			}
			rows[i] = row{status: "● online", ok: true, host: ping.Host, version: ping.Version, procs: "-"}
			if views, err := c.List(ctx); err == nil {
				online := 0
				for _, v := range views {
					if v.Status == "online" {
						online++
					}
				}
				rows[i].procs = fmt.Sprintf("%d/%d", online, len(views))
			}
		}()
	}
	wg.Wait()

	cells := make([][]string, len(list))
	for i, s := range list {
		where := s.URL
		if s.Mode == remote.ModeTunnel {
			where = "tunnel (dials in)"
		}
		cells[i] = []string{s.Name, rows[i].status, rows[i].procs, rows[i].host, where, rows[i].version}
	}
	fmt.Println()
	renderTable(os.Stdout, []string{"name", "status", "online", "host", "address", "version"}, cells, map[int]bool{2: true},
		func(r, c int, s string) string {
			switch c {
			case 0:
				return bold(s)
			case 1:
				if rows[r].ok {
					return green(s)
				}
				return red(s)
			case 4, 5:
				return dim(s)
			}
			return s
		})
	fmt.Println()
	return nil
}
