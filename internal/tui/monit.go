// Package tui implements `procly monit`, a live terminal dashboard.
package tui

import (
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/proc"
)

const (
	refreshEvery = time.Second
	logEvery     = 250 * time.Millisecond
	hostEvery    = 2 * time.Second
	historyLen   = 120
	flashFor     = 5 * time.Second
)

// Run opens the dashboard and blocks until the user quits.
func Run(c *client.Client) error {
	// Bubble Tea already asked the terminal for its background color during
	// init. PROCLY_THEME overrides that for terminals that answer wrongly.
	switch strings.ToLower(os.Getenv("PROCLY_THEME")) {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	}
	_, err := tea.NewProgram(newModel(c), tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithFPS(frameRate())).Run()
	return err
}

// now is the clock, replaced in tests.
var now = time.Now

const (
	scrollGap = 150 * time.Millisecond // steps closer than this belong to one scroll
	scrollMax = 8                      // how many times faster a long scroll gets
)

// stepOf is how many lines one scroll step moves: base for the first, then
// faster the longer the scroll goes on (a held key, a trackpad flick), up to
// scrollMax times. A long scroll then reaches its place in fewer frames,
// which is what a slow link needs; a single step stays one step.
func (m *model) stepOf(base int) int {
	t := now()
	if t.Sub(m.lastScroll) < scrollGap {
		m.streak++
	} else {
		m.streak = 0
	}
	m.lastScroll = t
	return base * min(1+m.streak/5, scrollMax)
}

// frameRate is how often the screen is sent to the terminal at most. Scrolling
// the logs changes most of the screen on every frame, about 7 KB, so 60 frames
// a second is 400 KB/s: more than a link with some latency takes in, and the
// screen then keeps moving for seconds after you stop. Over SSH the rate is
// lower. PROCLY_FPS sets it (1 to 60).
func frameRate() int {
	fps := 30
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		fps = 8
	}
	if n, err := strconv.Atoi(os.Getenv("PROCLY_FPS")); err == nil && n >= 1 && n <= 60 {
		fps = n
	}
	return fps
}

type (
	tickMsg     struct{}
	logTickMsg  struct{}
	hostTickMsg struct{}
	hostMsg     hostStats
	domainsMsg  struct {
		rep domains.Report
		err error
	}
	domainsTickMsg struct{}
	listMsg        struct {
		infos []proc.Info
		err   error
	}
	actionMsg struct {
		verb, name string
		err        error
	}
)

type pane int

const (
	paneProcs pane = iota
	paneLogs
)

type inputMode int

const (
	inputNone  inputMode = iota
	inputProcs           // typing a filter for the process table
	inputLogs            // typing a search for the log pane
)

type model struct {
	c      *client.Client
	views  []proc.View
	rows   []row
	err    error
	loaded bool
	w, h   int

	sel        selection
	collapsed  map[string]bool
	sortBy     sortMode
	procFilter string

	cpu  map[string][]float64 // by selection key; a group's are its members' sums
	mem  map[string][]float64
	pids map[string]string // pids the history belongs to; new pids start a fresh graph

	logKey    string // selection key the log pane shows
	logs      []logLine
	srcs      []*logSource
	logOff    int // lines hidden below the bottom of the pane; 0 follows new output
	unseen    int // lines that arrived while paused
	logFilter string
	stream    streamFilter
	nowrap    bool

	focus    pane
	input    inputMode
	inputBuf string

	zoom, help bool
	confirm    string            // target waiting for delete confirmation
	busy       map[string]string // target -> verb in progress
	flash      string
	flashErr   bool
	flashAt    time.Time
	host       hostStats
	doms       domains.Report // which names reach which process, as the daemon last found
	domsOK     bool           // doms holds an answer

	lastScroll time.Time // when the last scroll step came, for speeding up
	streak     int       // scroll steps in a row, each within scrollGap of the one before
}

func newModel(c *client.Client) model {
	return model{
		c:         c,
		collapsed: map[string]bool{},
		cpu:       map[string][]float64{},
		mem:       map[string][]float64{},
		pids:      map[string]string{},
		busy:      map[string]string{},
		host:      newHostStats(),
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.fetch(), logTick(), fetchHost(), m.fetchDomains()) }

func (m model) fetch() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		infos, err := c.List()
		return listMsg{infos, err}
	}
}

// fetchDomains asks the daemon which domain names reach the processes. The
// daemon keeps its answer for a few seconds, so asking is cheap.
func (m model) fetchDomains() tea.Cmd {
	c := m.c
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		rep, err := c.Domains(false)
		return domainsMsg{rep, err}
	}
}

func tick() tea.Cmd    { return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} }) }
func logTick() tea.Cmd { return tea.Tick(logEvery, func(time.Time) tea.Msg { return logTickMsg{} }) }
func fetchHost() tea.Cmd {
	return func() tea.Msg { return hostMsg(readHost()) }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		return m, m.fetch()
	case listMsg:
		m.loaded = true
		m.err = msg.err
		if msg.err == nil {
			m.views = proc.NewViews(msg.infos)
			m.refreshRows()
			m.record()
		}
		return m, tick()
	case logTickMsg:
		m.readLogs()
		return m, logTick()
	case hostMsg:
		m.host = hostStats(msg)
		return m, tea.Tick(hostEvery, func(time.Time) tea.Msg { return hostTickMsg{} })
	case hostTickMsg:
		return m, fetchHost()
	case domainsMsg:
		if msg.err == nil {
			m.doms, m.domsOK = msg.rep, true
		}
		return m, tea.Tick(10*time.Second, func(time.Time) tea.Msg { return domainsTickMsg{} })
	case domainsTickMsg:
		return m, m.fetchDomains()
	case actionMsg:
		delete(m.busy, msg.name)
		if msg.err != nil {
			m.say(msg.err.Error(), true)
		} else {
			m.say(pastTense[msg.verb]+" "+msg.name, false)
		}
		if msg.verb == "delete" {
			m.logKey = "" // the next list picks a new selection and its logs
		}
		return m, m.fetch()
	case tea.KeyMsg:
		return m.key(msg)
	case tea.MouseMsg:
		return m.mouse(msg)
	}
	return m, nil
}

var pastTense = map[string]string{"restart": "restarted", "reload": "reloaded", "start": "started", "stop": "stopped", "delete": "deleted"}
var progressive = map[string]string{"restart": "restarting", "reload": "reloading", "start": "starting", "stop": "stopping", "delete": "deleting"}

func (m *model) say(s string, isErr bool) { m.flash, m.flashErr, m.flashAt = s, isErr, time.Now() }

// refreshRows rebuilds the table and keeps the cursor on the same process.
// When the selected row went away it picks its group's line (the group was
// folded), or else the row now in the same place.
func (m *model) refreshRows() {
	prev, prevGroup := m.selIndex(), ""
	if prev >= 0 {
		prevGroup = m.rows[prev].group
	}
	m.rows = buildRows(m.views, m.procFilter, m.sortBy, m.collapsed)
	if m.selIndex() < 0 {
		switch {
		case len(m.rows) == 0:
			m.sel = selection{}
		case prevGroup != "" && m.hasRow(selection{prevGroup, true}):
			m.sel = selection{prevGroup, true}
		default:
			m.sel = m.rows[min(max(prev, 0), len(m.rows)-1)].sel()
		}
	}
	m.syncLogs()
}

func (m *model) hasRow(s selection) bool {
	for _, r := range m.rows {
		if r.sel() == s {
			return true
		}
	}
	return false
}

func (m *model) selIndex() int {
	for i, r := range m.rows {
		if r.sel() == m.sel {
			return i
		}
	}
	return -1
}

func (m *model) selectedRow() (row, bool) {
	if i := m.selIndex(); i >= 0 {
		return m.rows[i], true
	}
	return row{}, false
}

func (m *model) moveTo(i int) {
	if len(m.rows) == 0 {
		return
	}
	i = min(max(i, 0), len(m.rows)-1)
	m.sel = m.rows[i].sel()
	m.syncLogs()
}

func (m *model) move(delta int) { m.moveTo(m.selIndex() + delta) }

// syncLogs points the log pane at the selection, when it changed.
func (m *model) syncLogs() {
	key := m.sel.key()
	if m.sel.name == "" {
		key = ""
	}
	if key == m.logKey {
		return
	}
	m.logKey = key
	r, ok := m.selectedRow()
	if !ok {
		m.openLogs(nil)
		return
	}
	m.openLogs(r.views())
}

// record appends one CPU and memory sample per running process and group.
func (m *model) record() {
	seen := map[string]bool{}
	add := func(key, pids string, cpu, mem float64) {
		seen[key] = true
		if m.pids[key] != pids {
			m.pids[key] = pids
			m.cpu[key], m.mem[key] = nil, nil
		}
		m.cpu[key] = appendCapped(m.cpu[key], cpu)
		m.mem[key] = appendCapped(m.mem[key], mem)
	}
	groups := map[string][]*proc.View{}
	for i := range m.views {
		v := &m.views[i]
		if v.Group != "" {
			groups[v.Group] = append(groups[v.Group], v)
		}
		if v.PID == 0 {
			continue
		}
		add(v.Name, strconv.Itoa(v.PID), v.CPU, float64(v.Memory))
	}
	for g, ms := range groups {
		var cpu, mem float64
		var pids []string
		for _, v := range ms {
			if v.PID > 0 {
				cpu += v.CPU
				mem += float64(v.Memory)
				pids = append(pids, strconv.Itoa(v.PID))
			}
		}
		if len(pids) > 0 {
			// A group's graph carries on through rolling restarts.
			add(selection{g, true}.key(), "group", cpu, mem)
		}
	}
	for k := range m.cpu {
		if !seen[k] {
			delete(m.cpu, k)
			delete(m.mem, k)
			delete(m.pids, k)
		}
	}
}

func appendCapped(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > historyLen {
		s = s[len(s)-historyLen:]
	}
	return s
}

// ---- input ----

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirm != "" {
		target := m.confirm
		m.confirm = ""
		if s == "y" || s == "Y" {
			return m, m.act("delete", target)
		}
		m.say("delete cancelled", false)
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	if m.input != inputNone {
		return m.typing(k)
	}
	// Keys that arrive together (key repeat, a paste) come as one message;
	// act on each, as if they had been pressed one at a time.
	if k.Type == tea.KeyRunes && len(k.Runes) > 1 && !k.Paste {
		var cmds []tea.Cmd
		var next tea.Model = m
		for _, r := range k.Runes {
			var cmd tea.Cmd
			next, cmd = next.(model).key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: k.Alt})
			cmds = append(cmds, cmd)
		}
		return next, tea.Batch(cmds...)
	}

	inLogs := m.zoom || m.focus == paneLogs
	page := max(m.layout().logs.h-3, 1)
	r, haveRow := m.selectedRow()
	switch s {
	case "q":
		return m, tea.Quit
	case "esc":
		switch {
		case m.zoom:
			m.zoom = false
		case inLogs && m.logFilter != "":
			m.logFilter = ""
			m.follow()
		case !inLogs && m.procFilter != "":
			m.procFilter = ""
			m.refreshRows()
		case m.focus == paneLogs:
			m.focus = paneProcs
		}
	case "tab", "shift+tab":
		if !m.zoom {
			m.focus = 1 - m.focus
		}
	case "up", "k":
		if inLogs {
			m.scrollLogs(m.stepOf(1))
		} else {
			m.move(-1)
		}
	case "down", "j":
		if inLogs {
			m.scrollLogs(-m.stepOf(1))
		} else {
			m.move(1)
		}
	case "pgup", "ctrl+b":
		m.scrollLogs(page)
	case "pgdown", "ctrl+f":
		m.scrollLogs(-page)
	case "ctrl+u":
		m.scrollLogs(page / 2)
	case "ctrl+d":
		m.scrollLogs(-page / 2)
	case "home", "g":
		if inLogs {
			m.scrollLogs(len(m.logs))
		} else {
			m.moveTo(0)
		}
	case "end", "G":
		if inLogs {
			m.follow()
		} else {
			m.moveTo(len(m.rows) - 1)
		}
	case "f":
		m.follow()
	case "left", "h":
		if haveRow && r.v == nil && !m.collapsed[r.group] {
			m.collapsed[r.group] = true
			m.refreshRows()
		} else if haveRow && r.group != "" && r.v != nil {
			m.sel = selection{r.group, true}
			m.syncLogs()
		}
	case "right", "l":
		if haveRow && r.v == nil && m.collapsed[r.group] {
			delete(m.collapsed, r.group)
			m.refreshRows()
		}
	case " ":
		if haveRow && r.v == nil {
			if m.collapsed[r.group] {
				delete(m.collapsed, r.group)
			} else {
				m.collapsed[r.group] = true
			}
			m.refreshRows()
		}
	case "enter", "z":
		m.zoom = !m.zoom
	case "?":
		m.help = true
	case "/":
		if inLogs {
			m.input, m.inputBuf = inputLogs, m.logFilter
		} else {
			m.input, m.inputBuf = inputProcs, m.procFilter
		}
	case "o":
		m.sortBy = (m.sortBy + 1) % sortModes
		m.refreshRows()
		m.say("sorted by "+sortNames[m.sortBy], false)
	case "e":
		m.stream = (m.stream + 1) % streamModes
		m.follow()
		m.say("showing "+streamNames[m.stream], false)
	case "w":
		m.nowrap = !m.nowrap
		if m.nowrap {
			m.say("long lines are cut", false)
		} else {
			m.say("long lines wrap", false)
		}
	case "c":
		m.logs = nil
		m.follow()
	case "r":
		if haveRow {
			return m, m.act("restart", r.target())
		}
	case "R":
		if haveRow {
			if r.views()[0].ReloadSignal == "" {
				m.say(r.target()+" has no reload_signal; r restarts it instead", true)
				return m, nil
			}
			return m, m.act("reload", r.target())
		}
	case "s":
		if haveRow {
			for _, v := range r.views() {
				if v.Status == proc.StatusOnline || v.Status == proc.StatusWaiting || v.Status == proc.StatusOrphaned {
					return m, m.act("stop", r.target())
				}
			}
			return m, m.act("start", r.target())
		}
	case "d", "x", "delete":
		if haveRow {
			m.confirm = r.target()
		}
	}
	return m, nil
}

// typing edits the filter or search; it applies as you type.
func (m model) typing(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.inputBuf = ""
		m.applyInput()
		m.input = inputNone
		return m, nil
	case tea.KeyEnter, tea.KeyTab:
		m.input = inputNone
		return m, nil
	case tea.KeyBackspace:
		if r := []rune(m.inputBuf); len(r) > 0 {
			m.inputBuf = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		m.inputBuf = ""
	case tea.KeySpace:
		m.inputBuf += " "
	case tea.KeyRunes:
		m.inputBuf += string(k.Runes)
	default:
		return m, nil
	}
	m.applyInput()
	return m, nil
}

func (m *model) applyInput() {
	switch m.input {
	case inputLogs:
		m.logFilter = m.inputBuf
		m.follow()
	case inputProcs:
		m.procFilter = m.inputBuf
		m.refreshRows()
	}
}

func (m model) mouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.help || m.confirm != "" || m.input != inputNone {
		return m, nil
	}
	l := m.layout()
	switch {
	case l.logs.has(e.X, e.Y):
		switch e.Button {
		case tea.MouseButtonWheelUp:
			m.scrollLogs(m.stepOf(3))
		case tea.MouseButtonWheelDown:
			m.scrollLogs(-m.stepOf(3))
		case tea.MouseButtonLeft:
			if e.Action == tea.MouseActionPress && !m.zoom {
				m.focus = paneLogs
			}
		}
	case l.table.has(e.X, e.Y):
		switch e.Button {
		case tea.MouseButtonWheelUp:
			m.move(-1)
		case tea.MouseButtonWheelDown:
			m.move(1)
		case tea.MouseButtonLeft:
			if e.Action != tea.MouseActionPress {
				break
			}
			m.focus = paneProcs
			if i := m.rowAt(l, e.Y); i >= 0 {
				r := m.rows[i]
				if r.sel() == m.sel && r.v == nil { // a second click folds or unfolds a group
					if m.collapsed[r.group] {
						delete(m.collapsed, r.group)
					} else {
						m.collapsed[r.group] = true
					}
					m.refreshRows()
				} else {
					m.moveTo(i)
				}
			}
		}
	}
	return m, nil
}

func (m model) act(verb, target string) tea.Cmd {
	if _, running := m.busy[target]; running {
		return nil
	}
	m.busy[target] = verb
	c := m.c
	return func() tea.Msg {
		var err error
		switch verb {
		case "restart", "start":
			_, err = c.Restart(target)
		case "reload":
			_, err = c.Reload(target)
		case "stop":
			_, err = c.Stop(target)
		case "delete":
			_, err = c.Delete(target)
		}
		return actionMsg{verb, target, err}
	}
}

// busyVerb is what is being done to v, directly or through its group.
func (m model) busyVerb(v *proc.View) (string, bool) {
	if verb, ok := m.busy[v.Name]; ok {
		return verb, true
	}
	if v.Group != "" {
		verb, ok := m.busy[v.Group]
		return verb, ok
	}
	return "", false
}
