package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/proc"
)

func cmdDomains(argv []string) error {
	a, err := parse(argv, withServer(flagSet{bools: set("json", "refresh")}))
	if err != nil {
		return err
	}
	server := pickServer(a)
	var rep domains.Report
	if server != "" {
		c, err := remoteServer(server)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if rep, err = c.Domains(ctx, a.bools["refresh"]); err != nil {
			return remoteErr(server, err)
		}
	} else {
		c, ok := connectOrRecover()
		if !ok {
			return fmt.Errorf("daemon is not running, start a process first: procly start app.js")
		}
		if rep, err = c.Domains(a.bools["refresh"]); err != nil {
			return err
		}
	}
	if a.bools["json"] {
		return printJSON(os.Stdout, rep)
	}
	printDomains(os.Stdout, rep, server)
	return nil
}

// dnsText says in words where a name points.
func dnsText(d domains.DNS) string {
	switch d.Status {
	case "here":
		return "DNS points here"
	case "cloudflare":
		return "DNS points to Cloudflare"
	case "elsewhere":
		if len(d.Addrs) > 0 {
			return "DNS points to " + d.Addrs[0]
		}
		return "DNS points elsewhere"
	case "none":
		return "DNS does not resolve"
	}
	return ""
}

// domainLine is one domain in a sentence: "api.example.com/ws (https, nginx)".
func domainLine(d domains.Domain) string {
	s := d.Host
	if d.Host == "*" {
		s = "any host (default site)"
	}
	if d.Path != "" {
		s += " " + d.Path
	}
	s += " (" + d.Scheme + ", " + d.Source + ")"
	if t := dnsText(d.DNS); t != "" {
		s += ", " + strings.ToLower(t[:1]) + t[1:]
	}
	return s
}

func portList2(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf(":%d", p)
	}
	return strings.Join(parts, " ")
}

func printDomains(w io.Writer, rep domains.Report, server string) {
	if len(rep.Processes) == 0 {
		fmt.Fprintln(w, dim("\n  No running processes.\n"))
		return
	}
	var rows [][]string
	var kinds []string // how to color a row: domain, none, idle
	for _, p := range rep.Processes {
		switch {
		case len(p.Domains) > 0:
			for i, d := range p.Domains {
				name, ports := p.Name, portList2(p.Ports)
				if i > 0 {
					name, ports = "", ""
				}
				rows = append(rows, []string{name, ports, d.Host + d.Path, d.Scheme, d.Source, dnsText(d.DNS)})
				kinds = append(kinds, "domain")
			}
		case len(p.Ports) == 0:
			rows = append(rows, []string{p.Name, "-", "not listening on a port", "", "", ""})
			kinds = append(kinds, "idle")
		default:
			rows = append(rows, []string{p.Name, portList2(p.Ports), "no domain found", "", "", ""})
			kinds = append(kinds, "none")
		}
	}
	fmt.Fprintln(w)
	renderTable(w, []string{"name", "ports", "domain", "", "via", "dns"}, rows, nil, func(row, col int, s string) string {
		switch col {
		case 0:
			return bold(s)
		case 2:
			if kinds[row] == "domain" {
				return green(s)
			}
			return dim(s)
		case 1, 3, 4, 5:
			return dim(s)
		}
		return s
	})
	fmt.Fprintln(w)
	var looked []string
	for _, s := range rep.Sources {
		switch {
		case s.Error != "":
			looked = append(looked, s.Name+" ("+yellow("could not read: "+s.Error)+")")
		case s.Found:
			looked = append(looked, fmt.Sprintf("%s (%s)", s.Name, plural(s.Sites, "site", "sites")))
		default:
			looked = append(looked, s.Name+" (not found)")
		}
	}
	where := "this machine"
	if server != "" {
		where = server
	}
	fmt.Fprintln(w, dim("  Looked at "+where+": "+strings.Join(looked, ", ")+"."))
	fmt.Fprintln(w, dim("  A name is connected when a proxy sends its traffic to a port the process listens on. Docker labels and other proxies are not read."))
	fmt.Fprintln(w)
}

// domainRows are the rows describe adds for one process.
func domainRows(rep domains.Report, name string) [][2]string {
	for _, p := range rep.Processes {
		if p.Name != name {
			continue
		}
		var rows [][2]string
		for i, d := range p.Domains {
			label := ""
			if i == 0 {
				label = "domain"
			}
			rows = append(rows, [2]string{label, domainLine(d)})
		}
		if len(p.Domains) == 0 && len(p.Ports) > 0 {
			rows = append(rows, [2]string{"domain", "none found (listens on " + portList2(p.Ports) + "; no nginx, Caddy or Apache site sends traffic there)"})
		}
		return rows
	}
	return nil
}

func cmdMode(argv []string) error {
	a, err := parse(argv, flagSet{valued: set("instances"), bools: set("yes", "y"), alias: map[string]string{"i": "instances", "y": "yes"}})
	if err != nil {
		return err
	}
	if len(a.pos) != 2 || (a.pos[1] != proc.ExecFork && a.pos[1] != proc.ExecCluster) {
		return errors.New("usage: procly mode <name> fork|cluster [--instances <n|max>] [--yes]")
	}
	n := 0
	if v := a.get("instances"); v == "max" {
		n = 0
	} else if v != "" {
		if n, err = strconv.Atoi(v); err != nil || n < 1 || n > proc.MaxInstances {
			return fmt.Errorf("--instances must be a number from 1 to %d, or max", proc.MaxInstances)
		}
	}
	c, ok := connectOrRecover()
	if !ok {
		return errors.New("daemon is not running")
	}
	cur, err := c.Get(a.pos[0])
	if err != nil {
		// a group's base name: look it up in the list
		list, lerr := c.List()
		if lerr != nil {
			return err
		}
		found := false
		for _, in := range list {
			if in.Spec.Group == a.pos[0] {
				cur, found = in, true
				break
			}
		}
		if !found {
			return err
		}
	}
	what := "one process"
	if cur.Spec.Group != "" {
		what = fmt.Sprintf("%d separate processes", cur.Spec.Instances)
	} else if cur.Spec.ExecMode == proc.ExecCluster {
		what = fmt.Sprintf("a cluster of %d %s", cur.Spec.Workers(), plural(cur.Spec.Workers(), "worker", "workers"))
	}
	fmt.Printf("  %s runs as %s. Switching it to %s restarts it (it is stopped, then started again).\n", bold(a.pos[0]), what, a.pos[1])
	if ws, werr := c.Preflight(a.pos[0], a.pos[1], n); werr == nil {
		printWarnings(ws)
	}
	fmt.Println(dim("  procly then watches the new setup for a few seconds, and puts the old one back if it does not work."))
	if !a.bools["yes"] && !confirmTerminal("  Go on?") {
		fmt.Println(dim("  Nothing changed."))
		return nil
	}
	infos, err := c.SetMode(a.pos[0], a.pos[1], n)
	if err != nil {
		return err
	}
	printList(os.Stdout, proc.NewViews(infos), "")
	fmt.Println(dim("  procly save keeps this after a reboot."))
	return nil
}

// confirmTerminal asks a yes or no question on a terminal. Anything but y or
// yes is a no, and so is input that is not a terminal.
func confirmTerminal(q string) bool {
	if !xterm.IsTerminal(os.Stdin.Fd()) {
		fmt.Println(dim("  (not a terminal: pass --yes to go on without asking)"))
		return false
	}
	fmt.Printf("%s [y/N] ", q)
	var ans string
	fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

// printWarnings shows what procly found in an app's source that matters when
// it runs more than once.
func printWarnings(ws []proc.Warning) {
	for _, w := range ws {
		where := ""
		if w.File != "" {
			where = dim(" (" + w.File + ")")
		}
		fmt.Printf("  %s %s%s\n", yellow("!"), w.Text, where)
		if w.Advice != "" {
			fmt.Printf("    %s\n", dim(w.Advice))
		}
	}
}

// cmdScale changes the workers of a cluster app while it keeps running.
func cmdScale(argv []string) error {
	a, err := parse(argv, flagSet{bools: set("yes", "y"), alias: map[string]string{"y": "yes"}})
	if err != nil {
		return err
	}
	if len(a.pos) != 2 {
		return errors.New("usage: procly scale <name> <n|+n|-n> [--yes]")
	}
	word := a.pos[1]
	relative := strings.HasPrefix(word, "+") || strings.HasPrefix(word, "-")
	n, err := strconv.Atoi(strings.TrimPrefix(word, "+"))
	if err != nil {
		return errors.New("usage: procly scale <name> <n|+n|-n>: a number of workers, or +2 / -1 to change it")
	}
	c, ok := connectOrRecover()
	if !ok {
		return errors.New("daemon is not running")
	}
	cur, err := c.Get(a.pos[0])
	if err != nil {
		return err
	}
	if cur.Spec.ExecMode != proc.ExecCluster {
		return fmt.Errorf("%s does not run in cluster mode; scale changes the workers of a cluster app (procly mode %s cluster)", a.pos[0], a.pos[0])
	}
	to := n
	if relative {
		to = cur.Spec.Workers() + n
	}
	fmt.Printf("  %s has %d %s; scaling it to %d. The running workers keep serving while the others start or stop.\n", bold(a.pos[0]), cur.Spec.Workers(), plural(cur.Spec.Workers(), "worker", "workers"), to)
	if !a.bools["yes"] && !confirmTerminal("  Go on?") {
		fmt.Println(dim("  Nothing changed."))
		return nil
	}
	in, err := c.Scale(a.pos[0], n, relative)
	if err != nil {
		return err
	}
	printList(os.Stdout, proc.NewViews([]proc.Info{in}), "")
	fmt.Println(dim("  procly save keeps this after a reboot."))
	return nil
}
