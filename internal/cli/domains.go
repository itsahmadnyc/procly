package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/itsahmadnyc/procly/internal/domains"
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
