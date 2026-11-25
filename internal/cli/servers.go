package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/itsahmadnyc/procly/internal/agent"
	"github.com/itsahmadnyc/procly/internal/remote"
)

func cmdAgent(argv []string) error {
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
  procly server ls
  procly server rm <name>`

func cmdServer(argv []string) error {
	if len(argv) == 0 {
		return errors.New(serverUsage)
	}
	switch argv[0] {
	case "add":
		return cmdServerAdd(argv[1:])
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
		fmt.Println(dim("\n  No servers added. On another server run 'procly agent', then 'procly server add' here.\n"))
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
				if errors.Is(err, remote.ErrFingerprint) {
					rows[i].status = "● bad fingerprint"
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
		cells[i] = []string{s.Name, rows[i].status, rows[i].procs, rows[i].host, s.URL, rows[i].version}
	}
	fmt.Println()
	renderTable(os.Stdout, []string{"name", "status", "online", "host", "url", "version"}, cells, map[int]bool{2: true},
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
