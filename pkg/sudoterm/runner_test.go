package sudoterm

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/sudo"
	"github.com/LosFurina/tmuxatlas/pkg/sudoterm/sudotermtest"
)

const typedLine = sudotermtest.TypedLine

// newTestRunner builds a runner whose shell finds the fake sudo first, and
// fails unless sudo resolves to it: tests never reach the real one.
func newTestRunner(t *testing.T, config Config) *Runner {
	t.Helper()
	path := sudotermtest.Install(t)
	config.Path = strings.TrimSuffix(path, "/sudo") + ":" + SystemPath
	runner, err := NewRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := runner.LookSudo(); err != nil || resolved != path {
		t.Fatalf("runner resolves sudo to %q: refusing to run", resolved)
	}
	return runner
}

type fakeScreen struct {
	mu       sync.Mutex
	output   bytes.Buffer
	password []bool
}

func (s *fakeScreen) Output(p []byte) error {
	s.mu.Lock()
	s.output.Write(p)
	s.mu.Unlock()
	return nil
}

func (s *fakeScreen) PasswordMode(on bool) error {
	s.mu.Lock()
	s.password = append(s.password, on)
	s.mu.Unlock()
	return nil
}

func (s *fakeScreen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

func (s *fakeScreen) waitFor(t *testing.T, check func(*fakeScreen) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := check(s)
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never reached the expected state: %q %v", s.text(), s.password)
}

func shows(text string) func(*fakeScreen) bool {
	return func(s *fakeScreen) bool { return strings.Contains(s.output.String(), text) }
}

func passwordPrompted(s *fakeScreen) bool {
	return strings.Contains(s.output.String(), "password:") && len(s.password) > 0 && s.password[len(s.password)-1]
}

func TestShellProposesTheCommandWithoutRunningIt(t *testing.T) {
	runner := newTestRunner(t, Config{MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second})
	screen := &fakeScreen{}
	proposal := Proposal{Command: "echo bonjour-$((40+2))", Requester: "host-agents"}
	run, err := runner.Start(proposal, 120, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Stop("test")
	screen.waitFor(t, shows("[terminal sudo]"))
	screen.waitFor(t, shows("sudo echo bonjour-$((40+2))"))
	banner := "# Proposé par host-agents : echo bonjour-$((40+2))"
	digest := "# 22 caractères, sha256 " + sudo.Fingerprint(proposal.Command)
	shown := screen.text()
	if at := strings.Index(shown, banner); at < 0 || at > strings.Index(shown, "[terminal sudo]") || !strings.Contains(shown, digest) {
		t.Fatalf("banner missing or after the prompt: %q", shown)
	}
	// Typed, not run: nothing happens until Enter.
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(screen.text(), "bonjour-42") {
		t.Fatalf("the proposed command ran by itself: %q", screen.text())
	}
	if err := run.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, passwordPrompted)
	if err := run.Write([]byte(typedLine + "\r")); err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, shows("bonjour-42"))
	// The human may type anything else.
	_ = run.Write([]byte("echo autre-$((1+1))\r"))
	screen.waitFor(t, shows("autre-2"))
	_ = run.Write([]byte("exit\r"))
	result := run.Wait()
	if !result.Started || result.ExitCode != 0 || result.Error != "" {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(screen.text(), typedLine) {
		t.Fatal("the password was echoed to the screen")
	}
}

func TestRunLimits(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config Config
		reason string
	}{
		{"idle", Config{MaxDuration: 10 * time.Second, IdleTimeout: 400 * time.Millisecond}, "inactivité"},
		{"max duration", Config{MaxDuration: 600 * time.Millisecond, IdleTimeout: 10 * time.Second}, "durée maximale atteinte"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := newTestRunner(t, tt.config)
			screen := &fakeScreen{}
			run, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, screen)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-run.Done():
			case <-time.After(8 * time.Second):
				t.Fatal("terminal not closed")
			}
			if result := run.Wait(); result.Error != tt.reason {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestClosingKillsBackgroundJobs(t *testing.T) {
	for _, tt := range []struct{ name, job string }{
		{"job", "sleep 300 &"},
		// Its own process group, deaf to SIGHUP, like nohup: only the
		// session sweep reaches it.
		{"nohup", "(trap '' HUP; exec sleep 300) &"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := newTestRunner(t, Config{MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second})
			screen := &fakeScreen{}
			run, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, screen)
			if err != nil {
				t.Fatal(err)
			}
			screen.waitFor(t, shows("sudo true"))
			_ = run.Write([]byte("\x15" + tt.job + " echo job=$!\r"))
			screen.waitFor(t, func(s *fakeScreen) bool { return regexp.MustCompile(`job=\d+`).MatchString(s.output.String()) })
			pid, _ := strconv.Atoi(strings.TrimPrefix(regexp.MustCompile(`job=\d+`).FindString(screen.text()), "job="))
			run.Stop("fermé")
			run.Wait()
			deadline := time.Now().Add(3 * time.Second)
			for syscall.Kill(pid, 0) == nil {
				if time.Now().After(deadline) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
					t.Fatalf("background job %d survived the terminal", pid)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

// The runner is not root: a process that sudo started refuses its signals.
// The terminal then says that something is still running rather than
// ending as if all was stopped.
func TestClosingReportsProcessesItCannotKill(t *testing.T) {
	var mu sync.Mutex
	protected := 0
	signalProcess = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		refuse := pid == protected
		mu.Unlock()
		if refuse {
			return syscall.EPERM // what kill(2) answers for a root process
		}
		return syscall.Kill(pid, sig)
	}
	t.Cleanup(func() { signalProcess = syscall.Kill })
	runner := newTestRunner(t, Config{MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second})
	screen := &fakeScreen{}
	run, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, shows("sudo true"))
	// Deaf to HUP and TERM in a group of its own: only KILL would end it.
	_ = run.Write([]byte("\x15(trap '' HUP TERM; exec sleep 300) & echo job=$!\r"))
	screen.waitFor(t, func(s *fakeScreen) bool { return regexp.MustCompile(`job=\d+`).MatchString(s.output.String()) })
	pid, _ := strconv.Atoi(strings.TrimPrefix(regexp.MustCompile(`job=\d+`).FindString(screen.text()), "job="))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	mu.Lock()
	protected = pid
	mu.Unlock()
	run.Stop("fermé")
	result := run.Wait()
	if !strings.HasPrefix(result.Error, "fermé ; 1 processus encore actif") {
		t.Fatalf("reason %q does not report the surviving process", result.Error)
	}
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("process %d was expected to survive in this test", pid)
	}
}

// A shell deaf to HUP and TERM is still killed once the grace runs out.
func TestStubbornShellIsKilledAfterTheGrace(t *testing.T) {
	runner := newTestRunner(t, Config{MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second})
	screen := &fakeScreen{}
	run, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, shows("sudo true"))
	_ = run.Write([]byte("\x15trap '' HUP TERM; echo piege-$((6*7))-pose\r"))
	screen.waitFor(t, shows("piege-42-pose"))
	started := time.Now()
	run.Stop("fermé")
	done := make(chan Result, 1)
	go func() { done <- run.Wait() }()
	select {
	case <-done:
		if elapsed := time.Since(started); elapsed < killGrace/2 {
			t.Fatalf("the shell ignored HUP and TERM but ended after %s", elapsed)
		}
	case <-time.After(killGrace + 5*time.Second):
		t.Fatal("the shell survived the grace")
	}
}

// Once the terminal ended, no signal may go out for its pid any more: the
// pid can belong to another process by then.
func TestNoSignalAfterTheTerminalEnded(t *testing.T) {
	var mu sync.Mutex
	var ended bool
	var late []string
	signalProcess = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		if ended {
			late = append(late, fmt.Sprintf("%d %v", pid, sig))
		}
		mu.Unlock()
		return syscall.Kill(pid, sig)
	}
	t.Cleanup(func() { signalProcess = syscall.Kill })
	runner := newTestRunner(t, Config{MaxDuration: 20 * time.Second, IdleTimeout: 10 * time.Second})
	screen := &fakeScreen{}
	run, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, shows("sudo true"))
	run.Stop("fermé") // the shell leaves at once on HUP
	run.Wait()
	mu.Lock()
	ended = true
	mu.Unlock()
	time.Sleep(killGrace + 500*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(late) > 0 {
		t.Fatalf("signals sent after the terminal ended: %v", late)
	}
}

func TestRunIsExclusive(t *testing.T) {
	runner := newTestRunner(t, Config{MaxDuration: 5 * time.Second, IdleTimeout: 5 * time.Second})
	run, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, &fakeScreen{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, &fakeScreen{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second terminal: %v", err)
	}
	// Once Wait returns, the next terminal opens at once, every time.
	for i := 0; i < 20; i++ {
		run.Stop("test")
		run.Wait()
		run, err = runner.Start(Proposal{Command: "true", Requester: "h"}, 80, 24, &fakeScreen{})
		if err != nil {
			t.Fatalf("terminal %d after the previous one closed: %v", i+2, err)
		}
	}
	run.Stop("test")
	run.Wait()
}

func TestRejectsCommandsThatCouldTypeKeys(t *testing.T) {
	runner := newTestRunner(t, Config{})
	for _, command := range []string{"true\nid", "true\rid", "true\x1b[A", "true\tid", "true id"} {
		if _, err := runner.Start(Proposal{Command: command, Requester: "h"}, 80, 24, &fakeScreen{}); err == nil {
			t.Fatalf("command %q accepted", command)
		}
	}
}

func TestProposalLine(t *testing.T) {
	for command, want := range map[string]string{
		"apt-get install -y jq": "sudo apt-get install -y jq",
		"sudo systemctl status": "sudo systemctl status",
		"sudoedit /etc/hosts":   "sudo sudoedit /etc/hosts",
	} {
		if got := (Proposal{Command: command}).Line(); got != want {
			t.Errorf("Line(%q) = %q, want %q", command, got, want)
		}
	}
}
