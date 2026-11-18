// Package sudoterm opens one ephemeral interactive shell in a PTY, on the
// peer that runs under the approving administrator's Unix account.
//
// The command an agent proposed is typed at the prompt but never run: the
// human reads it, changes it, runs it or types other commands, and gives
// their password to sudo directly. Nothing is captured: the terminal's
// output goes to the administrator's screen only, and keystrokes go to the
// PTY only.
package sudoterm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty/v2"
	"golang.org/x/sys/unix"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// SystemPath is the PATH of the shell: fixed, nothing from the peer's
// environment.
const SystemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

var (
	ErrRoot = errors.New("le terminal sudo ne tourne pas en root")
	ErrBusy = errors.New("un terminal sudo est déjà ouvert")
)

// Config bounds one terminal.
type Config struct {
	Shell       string // bash, started with --noprofile --norc -i
	Path        string // PATH of the shell; SystemPath when empty
	MaxDuration time.Duration
	IdleTimeout time.Duration
	// PrefillTimeout bounds the wait for the shell's line editor before the
	// proposed command is typed at the prompt.
	PrefillTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{Shell: "/bin/bash", MaxDuration: 15 * time.Minute, IdleTimeout: 5 * time.Minute, PrefillTimeout: 5 * time.Second}
}

// Proposal is what the agent asked for, as approved.
type Proposal struct {
	Command   string // validated by sudo.ValidateCommand
	Requester string // the requesting host's fingerprint, set by the hub
}

// Line is the command typed at the prompt: sudo in front unless the
// proposal already starts with it.
func (p Proposal) Line() string {
	if p.Command == "sudo" || strings.HasPrefix(p.Command, "sudo ") {
		return p.Command
	}
	return "sudo " + p.Command
}

// Screen receives what the human must see.
type Screen interface {
	Output([]byte) error
	// PasswordMode reports that the terminal is reading input without echo.
	PasswordMode(on bool) error
}

// Result is all that leaves the terminal: no output, no keystrokes.
type Result struct {
	Started  bool
	ExitCode int // of the shell
	Duration time.Duration
	Error    string // why it ended early, if it did
}

// Runner opens sudo terminals, one at a time.
type Runner struct {
	config Config
	mu     sync.Mutex
	busy   bool
}

func NewRunner(config Config) (*Runner, error) {
	if os.Geteuid() == 0 {
		return nil, ErrRoot
	}
	defaults := DefaultConfig()
	if config.Shell == "" {
		config.Shell = defaults.Shell
	}
	if config.Path == "" {
		config.Path = SystemPath
	}
	if config.MaxDuration <= 0 {
		config.MaxDuration = defaults.MaxDuration
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.PrefillTimeout <= 0 {
		config.PrefillTimeout = defaults.PrefillTimeout
	}
	return &Runner{config: config}, nil
}

// LookSudo returns the sudo the shell would run, resolved on its PATH.
func (r *Runner) LookSudo() (string, error) {
	for _, dir := range strings.Split(r.config.Path, ":") {
		candidate := dir + "/sudo"
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

// shellEnv keeps only what an interactive shell needs: no agent sockets, no
// history file, no profile.
func (r *Runner) shellEnv() []string {
	env := []string{
		"PATH=" + r.config.Path, "TERM=xterm-256color", "HISTFILE=/dev/null", "HISTSIZE=1000",
		"LANG=" + firstNonEmpty(os.Getenv("LANG"), "C.UTF-8"),
		`PS1=[terminal sudo] \u@\h:\w\$ `,
	}
	if current, err := user.Current(); err == nil {
		env = append(env, "HOME="+current.HomeDir, "USER="+current.Username, "LOGNAME="+current.Username)
	}
	return env
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Banner is printed before the shell starts: who proposed what, with the
// length and short sha256 shown in the panel, and what happens next.
func Banner(p Proposal) []byte {
	return []byte(fmt.Sprintf(
		"\x1b[1m# Proposé par %s : %s\x1b[0m\r\n"+
			"# %d caractères, sha256 %s. Rien n'est lancé : relisez la ligne ci-dessous,\r\n"+
			"# modifiez-la, lancez-la ou tapez d'autres commandes, puis « exit ».\r\n\r\n",
		p.Requester, p.Command, utf8.RuneCountInString(p.Command), sudo.Fingerprint(p.Command)))
}

// Run is one open terminal.
type Run struct {
	runner   *Runner
	cmd      *exec.Cmd
	ptmx     *os.File
	screen   Screen
	started  time.Time
	mu       sync.Mutex
	activity time.Time
	reason   string
	echoOff  bool
	done     chan struct{}
	result   Result
	kill     chan string
}

// Start opens the PTY, starts the shell and types the proposed command at
// its first prompt, without Enter.
func (r *Runner) Start(p Proposal, cols, rows uint16, screen Screen) (*Run, error) {
	// The validation also makes typing safe: no control character can reach
	// the line editor, so the line cannot run or rewrite itself.
	if err := sudo.ValidateCommand(p.Command); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	r.busy = true
	r.mu.Unlock()
	release := func() {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		release()
		return nil, err
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})
	cmd := exec.Command(r.config.Shell, "--noprofile", "--norc", "-i")
	cmd.Env = r.shellEnv()
	cmd.Dir = "/"
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	_ = screen.Output(Banner(p))
	if err := cmd.Start(); err != nil {
		ptmx.Close()
		tty.Close()
		release()
		return nil, err
	}
	tty.Close()
	now := time.Now()
	run := &Run{runner: r, cmd: cmd, ptmx: ptmx, screen: screen, started: now, activity: now,
		done: make(chan struct{}), kill: make(chan string, 1)}
	go run.prefill(p.Line())
	go run.supervise()
	return run, nil
}

// prefill waits for the line editor (readline leaves canonical mode while it
// reads a line) and types line into it.
func (run *Run) prefill(line string) {
	deadline := time.Now().Add(run.runner.config.PrefillTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-run.done:
			return
		default:
		}
		if lineEditing(run.ptmx) {
			_, _ = run.ptmx.Write([]byte(line))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = run.screen.Output([]byte("\r\n# Commande non pré-remplie (shell trop lent) : tapez-la vous-même.\r\n"))
}

func (run *Run) touch() {
	run.mu.Lock()
	run.activity = time.Now()
	run.mu.Unlock()
}

// Write sends the human's keystrokes to the PTY, and nowhere else.
func (run *Run) Write(input []byte) error {
	run.touch()
	_, err := run.ptmx.Write(input)
	return err
}

// Resize changes the PTY size.
func (run *Run) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return pty.Setsize(run.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Stop ends the terminal early.
func (run *Run) Stop(reason string) {
	select {
	case run.kill <- reason:
	default:
	}
}

// Done is closed once the result is ready.
func (run *Run) Done() <-chan struct{} { return run.done }

// Wait returns the result once the terminal is closed.
func (run *Run) Wait() Result {
	<-run.done
	return run.result
}

func (run *Run) supervise() {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buffer := make([]byte, 16<<10)
		for {
			n, err := run.ptmx.Read(buffer)
			if n > 0 {
				run.touch()
				_ = run.screen.Output(append([]byte(nil), buffer[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- run.cmd.Wait() }()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(run.runner.config.MaxDuration)
	defer deadline.Stop()
	var waitErr error
	// escalate fires killGrace after the shell was asked to stop. It lives in
	// this loop, not in a detached timer, so KILL only goes out while the
	// shell is not reaped yet: until then its pid cannot name anyone else.
	var escalate <-chan time.Time
	stop := func(reason string) {
		if fire := run.terminate(reason); fire != nil {
			escalate = fire
		}
	}
loop:
	for {
		select {
		case waitErr = <-exited:
			break loop
		case reason := <-run.kill:
			stop(reason)
		case <-deadline.C:
			stop("durée maximale atteinte")
		case <-escalate:
			escalate = nil
			select {
			case waitErr = <-exited:
				break loop // already reaped: the cleanup below handles the rest
			default:
			}
			pid := run.cmd.Process.Pid
			_ = signalProcess(-pid, syscall.SIGKILL)
			killSession(pid)
		case <-ticker.C:
			run.watchEcho()
			run.mu.Lock()
			idle := time.Since(run.activity) > run.runner.config.IdleTimeout
			run.mu.Unlock()
			if idle {
				stop("inactivité")
			}
		}
	}
	// Whatever the shell left behind goes too: its process group, and the
	// background jobs that interactive job control put in groups of their own
	// within the same session.
	_ = signalProcess(-run.cmd.Process.Pid, syscall.SIGKILL)
	killSession(run.cmd.Process.Pid)
	run.ptmx.Close()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
	}
	if run.echoOff {
		_ = run.screen.PasswordMode(false)
	}
	run.mu.Lock()
	result := Result{Started: true, ExitCode: exitCode(waitErr), Duration: time.Since(run.started), Error: run.reason}
	run.mu.Unlock()
	run.result = result
	// Free the runner before telling anyone the terminal ended: whoever
	// learns it closed (Wait, the audit log, the hub) may open the next one
	// at once.
	run.runner.mu.Lock()
	run.runner.busy = false
	run.runner.mu.Unlock()
	close(run.done)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exit.ExitCode()
	}
	return -1
}

// killGrace is how long the shell's process group has to stop after HUP
// and TERM before KILL.
const killGrace = 2 * time.Second

// signalProcess sends signals for the runner; tests watch it.
var signalProcess = syscall.Kill

// terminate asks the whole process group to stop with HUP and TERM, and
// returns the channel that fires when KILL is due. It returns nil when the
// shell was already asked to stop.
func (run *Run) terminate(reason string) <-chan time.Time {
	run.mu.Lock()
	if run.reason != "" {
		run.mu.Unlock()
		return nil
	}
	run.reason = reason
	run.mu.Unlock()
	pid := run.cmd.Process.Pid
	_ = signalProcess(-pid, syscall.SIGHUP)
	_ = signalProcess(-pid, syscall.SIGTERM)
	return time.After(killGrace)
}

// killSession kills every process of session sid that this account can
// signal. The shell leads its session (setsid), so sid is its pid.
func killSession(sid int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp session ...; comm may hold spaces.
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) > 3 && fields[3] == strconv.Itoa(sid) {
			_ = signalProcess(pid, syscall.SIGKILL)
		}
	}
}

// watchEcho reports when the PTY reads a line without echo: the shape of a
// password prompt, such as sudo's.
func (run *Run) watchEcho() {
	off, ok := PasswordMode(run.ptmx)
	if !ok || off == run.echoOff {
		return
	}
	run.echoOff = off
	_ = run.screen.PasswordMode(off)
}

func termios(file *os.File) (*unix.Termios, bool) {
	conn, err := file.SyscallConn()
	if err != nil {
		return nil, false
	}
	var value *unix.Termios
	var ioctlErr error
	if err := conn.Control(func(fd uintptr) { value, ioctlErr = unix.IoctlGetTermios(int(fd), ioctlGetTermios) }); err != nil || ioctlErr != nil {
		return nil, false
	}
	return value, true
}

// PasswordMode reports whether a terminal is in canonical mode without
// echo. Full-screen programs use raw mode and are not reported. A PTY master
// answers for its slave.
func PasswordMode(file *os.File) (bool, bool) {
	value, ok := termios(file)
	if !ok {
		return false, false
	}
	return value.Lflag&unix.ECHO == 0 && value.Lflag&unix.ICANON != 0, true
}

// lineEditing reports whether a line editor such as readline owns the
// terminal: canonical mode is off.
func lineEditing(file *os.File) bool {
	value, ok := termios(file)
	return ok && value.Lflag&unix.ICANON == 0
}

// Describe is a short line for logs, without the command itself.
func Describe(result Result) string {
	return strings.TrimSpace(fmt.Sprintf("shell_exit=%d duration=%s %s", result.ExitCode, result.Duration.Round(time.Second), result.Error))
}
