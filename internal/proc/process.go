package proc

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/logtail"
)

type Status string

const (
	StatusOnline  Status = "online"
	StatusWaiting Status = "waiting" // crashed, restart scheduled
	StatusStopped Status = "stopped"
	StatusErrored Status = "errored"
)

const (
	// A run longer than this resets the crash counter.
	stableAfter = 30 * time.Second
	maxBackoff  = 30 * time.Second
)

// Info is a point-in-time snapshot of a process, as returned by the API.
type Info struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Status    Status    `json:"status"`
	PID       int       `json:"pid"`
	Restarts  int       `json:"restarts"`
	StartedAt time.Time `json:"started_at"`
	ExitCode  int       `json:"exit_code"`
	CPU       float64   `json:"cpu"`
	Memory    int64     `json:"memory"`
	OutLog    string    `json:"out_log"`
	ErrLog    string    `json:"err_log"`
	Health    Health    `json:"health,omitempty"`
	HealthErr string    `json:"health_error,omitempty"`
	Spec      Spec      `json:"spec"`
}

// Process supervises one command. All fields are guarded by mu.
type Process struct {
	mu        sync.Mutex
	id        int
	spec      Spec
	status    Status
	cmd       *exec.Cmd
	pid       int
	startedAt time.Time
	restarts  int // restarts over the lifetime of this entry
	crashes   int // consecutive quick crashes; drives backoff and the give-up limit
	exitCode  int
	stopping  bool
	exited    chan struct{} // closed when the current run exits
	timer     *time.Timer   // pending auto-restart
	outLog    string
	errLog    string
	logger    *log.Logger

	health      Health
	healthFails int
	healthErr   string
	events      func(Event) // may be nil
}

func newProcess(id int, spec Spec, logDir string, logger *log.Logger, events func(Event)) *Process {
	return &Process{
		id:     id,
		spec:   spec,
		status: StatusStopped,
		outLog: filepath.Join(logDir, spec.Name+"-out.log"),
		errLog: filepath.Join(logDir, spec.Name+"-error.log"),
		logger: logger,
		events: events,
	}
}

func (p *Process) running() bool { return p.cmd != nil || p.status == StatusWaiting }

// launchLocked starts the command. Caller holds p.mu.
func (p *Process) launchLocked() error {
	out, err := openLog(p.outLog)
	if err != nil {
		p.status = StatusErrored
		return err
	}
	defer out.Close()
	errf, err := openLog(p.errLog)
	if err != nil {
		p.status = StatusErrored
		return err
	}
	defer errf.Close()

	cmd := exec.Command("/bin/sh", "-c", shellCommand(p.spec.Command))
	cmd.Dir = p.spec.Cwd
	cmd.Env = buildEnv(p.spec.Env, p.id, p.spec.Name)
	cmd.Stdout = out
	cmd.Stderr = errf
	// Own process group, so stop can signal the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(errf, "[procly] failed to start: %v\n", err)
		p.status = StatusErrored
		return fmt.Errorf("start %s: %w", p.spec.Name, err)
	}

	exited := make(chan struct{})
	p.cmd = cmd
	p.pid = cmd.Process.Pid
	p.startedAt = time.Now()
	p.status = StatusOnline
	p.stopping = false
	p.exited = exited
	p.health, p.healthFails, p.healthErr = "", 0, ""
	if p.spec.Health != "" {
		p.health = HealthStarting
		go p.healthLoop(cmd, exited, p.spec)
	}
	p.logger.Printf("%s: started pid=%d", p.spec.Name, p.pid)
	go p.wait(cmd, exited)
	return nil
}

func (p *Process) wait(cmd *exec.Cmd, exited chan struct{}) {
	err := cmd.Wait()
	code := exitCodeOf(err)

	p.mu.Lock()
	defer p.mu.Unlock()
	defer close(exited)
	if p.cmd != cmd {
		return
	}
	ranFor := time.Since(p.startedAt)
	p.cmd, p.pid, p.exited, p.exitCode = nil, 0, nil, code
	p.health, p.healthFails = "", 0

	if p.stopping {
		p.stopping = false
		p.status = StatusStopped
		p.logger.Printf("%s: stopped", p.spec.Name)
		return
	}
	p.logger.Printf("%s: exited code=%d after %s", p.spec.Name, code, ranFor.Round(time.Millisecond))

	if !p.spec.ShouldAutoRestart() {
		if code == 0 {
			p.status = StatusStopped
		} else {
			p.status = StatusErrored
			p.emit(Event{Kind: EventErrored, Process: p.spec.Name, ExitCode: code, Restarts: p.restarts, Detail: p.lastErrLines()})
		}
		return
	}
	if ranFor >= stableAfter {
		p.crashes = 0
	}
	p.crashes++
	if p.crashes > p.spec.MaxRestartsOrDefault() {
		p.status = StatusErrored
		p.logger.Printf("%s: crashed %d times in a row, giving up", p.spec.Name, p.crashes-1)
		p.emit(Event{Kind: EventErrored, Process: p.spec.Name, ExitCode: code, Restarts: p.restarts,
			Detail: fmt.Sprintf("gave up after %d quick crashes\n%s", p.crashes-1, p.lastErrLines())})
		return
	}
	delay := backoff(p.spec.restartDelay(), p.crashes)
	p.status = StatusWaiting
	p.timer = time.AfterFunc(delay, p.autoRestart)
	p.logger.Printf("%s: restarting in %s", p.spec.Name, delay)
	p.emit(Event{Kind: EventCrashed, Process: p.spec.Name, ExitCode: code, Restarts: p.restarts + 1, Retry: delay, Detail: p.lastErrLines()})
}

// lastErrLines returns the end of the stderr log, for alerts.
func (p *Process) lastErrLines() string {
	lines, _ := logtail.Tail(p.errLog, 5)
	return strings.Join(lines, "\n")
}

func (p *Process) autoRestart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status != StatusWaiting {
		return
	}
	p.timer = nil
	p.restarts++
	_ = p.launchLocked()
}

// Stop sends SIGTERM to the process group, then SIGKILL after the kill timeout.
// It returns once the process has exited.
func (p *Process) Stop() {
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if p.cmd == nil {
		p.status = StatusStopped
		p.mu.Unlock()
		return
	}
	p.stopping = true
	pid, exited, timeout, name := p.pid, p.exited, p.spec.killTimeout(), p.spec.Name
	p.mu.Unlock()

	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(timeout):
		p.logger.Printf("%s: did not exit within %s, sending SIGKILL", name, timeout)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-exited
	}
}

// Restart stops the process if needed and starts it again.
func (p *Process) Restart() error {
	p.Stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil // started concurrently
	}
	p.restarts++
	p.crashes = 0
	return p.launchLocked()
}

func (p *Process) Info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoLocked()
}

func (p *Process) infoLocked() Info {
	info := Info{
		ID:        p.id,
		Name:      p.spec.Name,
		Status:    p.status,
		PID:       p.pid,
		Restarts:  p.restarts,
		ExitCode:  p.exitCode,
		OutLog:    p.outLog,
		ErrLog:    p.errLog,
		Health:    p.health,
		HealthErr: p.healthErr,
		Spec:      p.spec,
	}
	if p.cmd != nil {
		info.StartedAt = p.startedAt
	}
	return info
}

func openLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

var assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// Words that must run inside the shell: `exec exit 1` or `exec cd x` would
// look for a program with that name.
var shellBuiltins = map[string]bool{
	"exit": true, "cd": true, "export": true, "source": true, ".": true, "set": true,
	"unset": true, "eval": true, "exec": true, "trap": true, "ulimit": true, "umask": true,
	"wait": true, "shift": true, "read": true, "alias": true, "readonly": true, "local": true,
	"return": true, "break": true, "continue": true, "type": true, "command": true,
	"true": true, "false": true, ":": true, "if": true, "for": true, "while": true,
	"until": true, "case": true, "{": true, "!": true, "function": true, "[": true, "test": true,
}

// shellCommand puts exec in front of the program of a simple command, so the
// shell is replaced and the PID we track is the real program. Leading
// VAR=value words stay in front ("PORT=3000 exec node app.js"). Anything
// compound, quoted or built in keeps the shell; stop still reaches the
// program because it signals the whole process group.
func shellCommand(c string) string {
	if strings.ContainsAny(c, ";&|\n()`") {
		return c
	}
	fields := strings.Fields(c)
	i := 0
	for i < len(fields) && assignmentRe.MatchString(fields[i]) {
		if strings.ContainsAny(fields[i], "\"'$\\") {
			return c // a quoted or expanded value may span words; leave it to sh
		}
		i++
	}
	if i == len(fields) || shellBuiltins[fields[i]] {
		return c
	}
	pos := 0 // byte offset just past the assignments
	for _, f := range fields[:i] {
		pos += strings.Index(c[pos:], f) + len(f)
	}
	head := strings.TrimSpace(c[:pos])
	rest := strings.TrimLeft(c[pos:], " \t")
	if head == "" {
		return "exec " + rest
	}
	return head + " exec " + rest
}

func buildEnv(extra map[string]string, id int, name string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return append(env, "PROCLY_ID="+strconv.Itoa(id), "PROCLY_NAME="+name)
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
			return ws.ExitStatus()
		}
		return ee.ExitCode()
	}
	return -1
}

func backoff(base time.Duration, n int) time.Duration {
	d := base
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}
