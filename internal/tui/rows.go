package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/itsahmadnyc/procly/internal/proc"
)

// selection names what the cursor is on: a process, or a group of instances
// by its base name. The two can not clash: the daemon refuses a process whose
// name is the base name of a group.
type selection struct {
	name  string
	group bool
}

func (s selection) key() string {
	if s.group {
		return "group:" + s.name
	}
	return s.name
}

// row is one line of the process table.
type row struct {
	v       *proc.View   // the process; nil on a group's line
	group   string       // the group's base name, on its line and its members'
	members []*proc.View // on a group's line
	last    bool         // the last member shown under its group
}

func (r row) sel() selection {
	if r.v == nil {
		return selection{r.group, true}
	}
	return selection{r.v.Name, false}
}

// target is what an action on the row sends to the daemon. The base name of
// a group stands for all its instances.
func (r row) target() string {
	if r.v == nil {
		return r.group
	}
	return r.v.Name
}

// views are the processes the row is about.
func (r row) views() []*proc.View {
	if r.v == nil {
		return r.members
	}
	return []*proc.View{r.v}
}

type sortMode int

const (
	sortID sortMode = iota
	sortName
	sortCPU
	sortMem
	sortRestarts
	sortModes
)

var sortNames = [...]string{"id", "name", "cpu", "memory", "restarts"}

// item is a standalone process or a whole group, the unit that is sorted.
type item struct {
	group   string
	members []*proc.View
}

func (it item) id() int {
	lo := it.members[0].ID
	for _, m := range it.members {
		lo = min(lo, m.ID)
	}
	return lo
}

func (it item) name() string {
	if it.group != "" {
		return it.group
	}
	return it.members[0].Name
}

func (it item) sum(f func(*proc.View) float64) float64 {
	var s float64
	for _, m := range it.members {
		s += f(m)
	}
	return s
}

// buildRows turns the daemon's list into table rows: groups together under
// their own line, filtered by name, sorted, and collapsed where asked.
func buildRows(views []proc.View, filter string, by sortMode, collapsed map[string]bool) []row {
	var items []item
	at := map[string]int{}
	for i := range views {
		v := &views[i]
		if v.Group == "" {
			items = append(items, item{members: []*proc.View{v}})
			continue
		}
		if j, ok := at[v.Group]; ok {
			items[j].members = append(items[j].members, v)
			continue
		}
		at[v.Group] = len(items)
		items = append(items, item{group: v.Group, members: []*proc.View{v}})
	}

	q := strings.ToLower(strings.TrimSpace(filter))
	if q != "" {
		kept := items[:0:0]
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.name()), q) {
				kept = append(kept, it)
				continue
			}
			if it.group == "" {
				continue
			}
			var ms []*proc.View
			for _, m := range it.members {
				if strings.Contains(strings.ToLower(m.Name), q) {
					ms = append(ms, m)
				}
			}
			if len(ms) > 0 {
				kept = append(kept, item{group: it.group, members: ms})
			}
		}
		items = kept
	}

	for _, it := range items {
		sort.SliceStable(it.members, func(a, b int) bool { return it.members[a].Index < it.members[b].Index })
	}
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a], items[b]
		var dx, dy float64
		switch by {
		case sortName:
			return x.name() < y.name()
		case sortCPU:
			dx, dy = x.sum(func(v *proc.View) float64 { return v.CPU }), y.sum(func(v *proc.View) float64 { return v.CPU })
		case sortMem:
			dx, dy = x.sum(func(v *proc.View) float64 { return float64(v.Memory) }), y.sum(func(v *proc.View) float64 { return float64(v.Memory) })
		case sortRestarts:
			dx, dy = x.sum(func(v *proc.View) float64 { return float64(v.Restarts) }), y.sum(func(v *proc.View) float64 { return float64(v.Restarts) })
		default:
			return x.id() < y.id()
		}
		if dx != dy {
			return dx > dy
		}
		return x.id() < y.id()
	})

	var rows []row
	for _, it := range items {
		if it.group == "" {
			rows = append(rows, row{v: it.members[0]})
			continue
		}
		rows = append(rows, row{group: it.group, members: it.members})
		if collapsed[it.group] && q == "" {
			continue
		}
		for i, m := range it.members {
			rows = append(rows, row{v: m, group: it.group, last: i == len(it.members)-1})
		}
	}
	return rows
}

// groupCounts sums the members' statuses.
func groupCounts(members []*proc.View) map[proc.Status]int {
	c := map[proc.Status]int{}
	for _, m := range members {
		c[m.Status]++
	}
	return c
}

// groupStatus is the short status of a group: "3 online", "2/3 online".
func groupStatus(members []*proc.View) (string, proc.Status) {
	c := groupCounts(members)
	n := len(members)
	on := c[proc.StatusOnline]
	switch {
	case on == n:
		return "● " + strconv.Itoa(n) + " online", proc.StatusOnline
	case c[proc.StatusStopped] == n:
		return "● " + strconv.Itoa(n) + " stopped", proc.StatusStopped
	case on == 0 && c[proc.StatusErrored] > 0:
		return "● " + strconv.Itoa(c[proc.StatusErrored]) + "/" + strconv.Itoa(n) + " errored", proc.StatusErrored
	}
	return "● " + strconv.Itoa(on) + "/" + strconv.Itoa(n) + " online", proc.StatusWaiting
}
