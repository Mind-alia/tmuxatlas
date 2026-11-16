// Package sudoterm runs one approved sudo command in an ephemeral PTY, on
// the peer that runs under the approving administrator's Unix account.
//
// sudo reads the password on /dev/tty, which is the PTY. The command's
// stdout and stderr go through a pipe instead, copied to the screen and to
// a bounded capture, so the password can never enter the capture. Typed
// input is still redacted from the capture in case a program echoes it.
package sudoterm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty/v2"
	"golang.org/x/sys/unix"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
)

// Prompt is passed to sudo -p; %p is the user whose password sudo wants.
const Prompt = "[sudo] mot de passe de %p : "

var (
	// ErrNoPassword means sudo would run without asking: approval alone
	// would then be enough, which this terminal refuses.
	ErrNoPassword = errors.New("sudo ne demande pas de mot de passe (NOPASSWD ou identifiants en cache) : refus")
	ErrRoot       = errors.New("le terminal sudo ne tourne pas en root")
	ErrBusy       = errors.New("un terminal sudo est déjà ouvert")
)

// Config bounds one terminal.
type Config struct {
	SudoPath     string // resolved from PATH when empty
	Shell        string
	MaxDuration  time.Duration
	IdleTimeout  time.Duration
	CaptureLimit int
}

func DefaultConfig() Config {
	return Config{Shell: "/bin/sh", MaxDuration: 15 * time.Minute, IdleTimeout: 5 * time.Minute, CaptureLimit: sudo.MaxOutputBytes}
}

// Screen receives what the human must see.
type Screen interface {
	Output([]byte) error
	// PasswordMode reports that the terminal is reading input without echo.
	PasswordMode(on bool) error
}

// Result is what leaves the terminal. Output is redacted.
type Result struct {
	Started    bool
	ExitCode   int
	Output     []byte
	Truncated  bool
	Redactions int
	Duration   time.Duration
	Error      string
}

// Runner opens sudo terminals.
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
	if config.MaxDuration <= 0 {
		config.MaxDuration = defaults.MaxDuration
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaults.IdleTimeout
	}
	if config.CaptureLimit <= 0 {
		config.CaptureLimit = defaults.CaptureLimit
	}
	return &Runner{config: config}, nil
}

func (r *Runner) sudoPath() (string, error) {
	if r.config.SudoPath != "" {
		return r.config.SudoPath, nil
	}
	return exec.LookPath("sudo")
}

// SudoPath returns the sudo binary this runner would execute.
func (r *Runner) SudoPath() (string, error) { return r.sudoPath() }

// cleanEnv keeps only what a sudo command needs; no agent sockets, no
// history file.
func cleanEnv() []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TERM=xterm-256color", "HISTFILE=/dev/null", "LANG=" + firstNonEmpty(os.Getenv("LANG"), "C.UTF-8"),
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

// background runs sudo without any terminal.
func (r *Runner) background(ctx context.Context, args ...string) (int, error) {
	path, err := r.sudoPath()
	if err != nil {
		return -1, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = cleanEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// Preflight forgets cached credentials, then checks that sudo really asks
// for a password.
func (r *Runner) Preflight(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := r.background(ctx, "-k"); err != nil {
		return fmt.Errorf("sudo introuvable : %w", err)
	}
	code, err := r.background(ctx, "-n", "-k", "--", "true")
	if err != nil {
		return err
	}
	if code == 0 {
		return ErrNoPassword
	}
	return nil
}

// Run is one open terminal.
type Run struct {
	runner  *Runner
	cmd     *exec.Cmd
	ptmx    *os.File
	screen  Screen
	started time.Time

	mu       sync.Mutex
	typed    []byte
	capture  bytes.Buffer
	trunc    bool
	activity time.Time
	reason   string
	echoOff  bool

	done   chan struct{}
	result Result
	once   sync.Once
	kill   chan string
}

// Start opens the PTY and runs sudo -k -- shell -c command.
func (r *Runner) Start(command string, cols, rows uint16, screen Screen) (*Run, error) {
	if err := sudo.ValidateCommand(command); err != nil {
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
	path, err := r.sudoPath()
	if err != nil {
		release()
		return nil, err
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
	reader, writer, err := os.Pipe()
	if err != nil {
		ptmx.Close()
		tty.Close()
		release()
		return nil, err
	}
	cmd := exec.Command(path, "-k", "-p", Prompt, "--", r.config.Shell, "-c", command)
	cmd.Env = cleanEnv()
	cmd.Dir = "/"
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.Stdin = tty
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		ptmx.Close()
		tty.Close()
		reader.Close()
		writer.Close()
		release()
		return nil, err
	}
	tty.Close()
	writer.Close()
	now := time.Now()
	run := &Run{runner: r, cmd: cmd, ptmx: ptmx, screen: screen, started: now, activity: now,
		done: make(chan struct{}), kill: make(chan string, 1)}
	go run.supervise(reader)
	return run, nil
}

func (run *Run) touch() {
	run.mu.Lock()
	run.activity = time.Now()
	run.mu.Unlock()
}

// Write sends the human's keystrokes to the PTY. They are kept in memory
// only to redact the capture, then wiped.
func (run *Run) Write(input []byte) error {
	run.mu.Lock()
	if len(run.typed)+len(input) <= 64<<10 {
		run.typed = append(run.typed, input...)
	}
	run.activity = time.Now()
	run.mu.Unlock()
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

// toScreen converts pipe output for a terminal: a pipe has no line
// discipline, so bare LF must become CRLF.
func toScreen(data []byte) []byte {
	var out bytes.Buffer
	for i, b := range data {
		if b == '\n' && (i == 0 || data[i-1] != '\r') {
			out.WriteByte('\r')
		}
		out.WriteByte(b)
	}
	return out.Bytes()
}

func (run *Run) supervise(pipe *os.File) {
	var readers sync.WaitGroup
	readers.Add(2)
	// The PTY carries sudo's prompt and anything written to /dev/tty: shown,
	// never captured.
	go func() {
		defer readers.Done()
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
	// The pipe carries the command's stdout and stderr: shown and captured.
	pipeDone := make(chan struct{})
	go func() {
		defer readers.Done()
		defer close(pipeDone)
		buffer := make([]byte, 16<<10)
		for {
			n, err := pipe.Read(buffer)
			if n > 0 {
				run.touch()
				run.mu.Lock()
				room := run.runner.config.CaptureLimit - run.capture.Len()
				if room >= n {
					run.capture.Write(buffer[:n])
				} else {
					if room > 0 {
						run.capture.Write(buffer[:room])
					}
					run.trunc = true
				}
				run.mu.Unlock()
				_ = run.screen.Output(toScreen(buffer[:n]))
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
loop:
	for {
		select {
		case waitErr = <-exited:
			break loop
		case reason := <-run.kill:
			run.terminate(reason)
		case <-deadline.C:
			run.terminate("durée maximale atteinte")
		case <-ticker.C:
			run.watchEcho()
			run.mu.Lock()
			idle := time.Since(run.activity) > run.runner.config.IdleTimeout
			run.mu.Unlock()
			if idle {
				run.terminate("inactivité")
			}
		}
	}
	// A background child may keep the pipe open: do not wait forever.
	select {
	case <-pipeDone:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
	}
	pipe.Close()
	run.ptmx.Close()
	readers.Wait()
	if run.echoOff {
		_ = run.screen.PasswordMode(false)
	}

	// Forget any credential sudo may have cached despite -k.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = run.runner.background(ctx, "-k")
	cancel()

	run.mu.Lock()
	segments := sudo.TypedSegments(run.typed)
	output, redactions := sudo.Redact(run.capture.Bytes(), segments)
	sudo.Wipe(run.typed)
	for _, segment := range segments {
		sudo.Wipe(segment)
	}
	run.typed = nil
	result := Result{
		Started: true, ExitCode: exitCode(waitErr), Output: append([]byte(nil), output...),
		Truncated: run.trunc, Redactions: redactions, Duration: time.Since(run.started), Error: run.reason,
	}
	sudo.Wipe(run.capture.Bytes())
	run.mu.Unlock()
	run.once.Do(func() {
		run.result = result
		close(run.done)
	})
	run.runner.mu.Lock()
	run.runner.busy = false
	run.runner.mu.Unlock()
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

// terminate stops the whole process group: TERM, then KILL.
func (run *Run) terminate(reason string) {
	run.mu.Lock()
	if run.reason != "" {
		run.mu.Unlock()
		return
	}
	run.reason = reason
	run.mu.Unlock()
	pid := run.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	_ = syscall.Kill(-pid, syscall.SIGHUP)
	time.AfterFunc(2*time.Second, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
}

// watchEcho reports when the PTY reads a line without echo: the shape of a
// password prompt.
func (run *Run) watchEcho() {
	off, ok := PasswordMode(run.ptmx)
	if !ok || off == run.echoOff {
		return
	}
	run.echoOff = off
	_ = run.screen.PasswordMode(off)
}

// PasswordMode reports whether a terminal is in canonical mode without
// echo. Full-screen programs use raw mode and are not reported. On Linux a
// PTY master answers for its slave.
func PasswordMode(file *os.File) (bool, bool) {
	conn, err := file.SyscallConn()
	if err != nil {
		return false, false
	}
	var termios *unix.Termios
	var ioctlErr error
	if err := conn.Control(func(fd uintptr) { termios, ioctlErr = unix.IoctlGetTermios(int(fd), unix.TCGETS) }); err != nil || ioctlErr != nil {
		return false, false
	}
	return termios.Lflag&unix.ECHO == 0 && termios.Lflag&unix.ICANON != 0, true
}

// Describe is a short line for logs, without the command itself.
func Describe(result Result) string {
	return strings.TrimSpace(fmt.Sprintf("exit=%d bytes=%d truncated=%t redactions=%d %s",
		result.ExitCode, len(result.Output), result.Truncated, result.Redactions, result.Error))
}
