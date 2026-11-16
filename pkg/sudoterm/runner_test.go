package sudoterm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/sudoterm/sudotermtest"
)

const typedLine = sudotermtest.TypedLine

// installFakeSudo builds a runner on the fake sudo, never the real one.
func installFakeSudo(t *testing.T) *Runner {
	t.Helper()
	path := sudotermtest.Install(t)
	runner, err := NewRunner(Config{MaxDuration: 10 * time.Second, IdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := runner.SudoPath(); err != nil || resolved != path {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("screen never reached the expected state: %q %v", s.output.String(), s.password)
}

func passwordPrompted(s *fakeScreen) bool {
	return strings.Contains(s.output.String(), "mot de passe de") && len(s.password) > 0 && s.password[len(s.password)-1]
}

func TestRunCapturesOnlyPipes(t *testing.T) {
	runner := installFakeSudo(t)
	screen := &fakeScreen{}
	run, err := runner.Start(`echo sortie-standard; echo sortie-erreur >&2; echo ecran-seul > /dev/tty; exit 3`, 80, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, passwordPrompted)
	if err := run.Write([]byte(typedLine + "\r")); err != nil {
		t.Fatal(err)
	}
	result := run.Wait()
	output := string(result.Output)
	if !result.Started || result.ExitCode != 3 {
		t.Fatalf("result = %+v", result)
	}
	for _, want := range []string{"sortie-standard", "sortie-erreur"} {
		if !strings.Contains(output, want) {
			t.Fatalf("capture %q lacks %q", output, want)
		}
	}
	for _, unwanted := range []string{typedLine, "mot de passe", "ecran-seul"} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("capture %q contains %q", output, unwanted)
		}
	}
	screen.mu.Lock()
	shown := screen.output.String()
	modes := append([]bool(nil), screen.password...)
	screen.mu.Unlock()
	if strings.Contains(shown, typedLine) {
		t.Fatal("typed input was echoed to the screen")
	}
	if !strings.Contains(shown, "ecran-seul") || !strings.Contains(shown, "sortie-standard\r\n") {
		t.Fatalf("screen = %q", shown)
	}
	if len(modes) < 2 || !modes[0] || modes[len(modes)-1] {
		t.Fatalf("password mode changes = %v, want on then off", modes)
	}
}

func TestRunRedactsEchoedInput(t *testing.T) {
	runner := installFakeSudo(t)
	screen := &fakeScreen{}
	run, err := runner.Start(`IFS= read -r line; echo "recu:$line"`, 80, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	screen.waitFor(t, passwordPrompted)
	run.Write([]byte(typedLine + "\r"))
	screen.waitFor(t, func(s *fakeScreen) bool { return len(s.password) >= 2 && !s.password[len(s.password)-1] })
	// The human types again, visibly: a program may copy it to stdout.
	run.Write([]byte("ligne-visible-5678\r"))
	result := run.Wait()
	if got := string(result.Output); got != "recu:[caviardé]\n" || result.Redactions != 1 {
		t.Fatalf("capture = %q (%d redactions)", got, result.Redactions)
	}
}

func TestRunTruncatesCapture(t *testing.T) {
	runner := installFakeSudo(t)
	runner.config.CaptureLimit = 10
	screen := &fakeScreen{}
	run, _ := runner.Start(`printf 'abcdefghijklmnop'`, 80, 24, screen)
	screen.waitFor(t, passwordPrompted)
	run.Write([]byte(typedLine + "\r"))
	result := run.Wait()
	if string(result.Output) != "abcdefghij" || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunLimits(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		reason string
	}{
		{"max duration", Config{MaxDuration: 500 * time.Millisecond, IdleTimeout: time.Minute}, "durée maximale atteinte"},
		{"idle", Config{MaxDuration: time.Minute, IdleTimeout: 400 * time.Millisecond}, "inactivité"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := installFakeSudo(t)
			runner.config.MaxDuration, runner.config.IdleTimeout = tt.config.MaxDuration, tt.config.IdleTimeout
			screen := &fakeScreen{}
			run, err := runner.Start(`sleep 30`, 80, 24, screen)
			if err != nil {
				t.Fatal(err)
			}
			screen.waitFor(t, passwordPrompted)
			run.Write([]byte(typedLine + "\r"))
			select {
			case <-run.Done():
			case <-time.After(8 * time.Second):
				t.Fatal("terminal outlived its limit")
			}
			if result := run.Wait(); result.Error != tt.reason || result.ExitCode == 0 {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestRunIsExclusive(t *testing.T) {
	runner := installFakeSudo(t)
	screen := &fakeScreen{}
	run, err := runner.Start(`true`, 80, 24, screen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(`true`, 80, 24, &fakeScreen{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start = %v", err)
	}
	run.Stop("test")
	run.Wait()
	next, err := runner.Start(`true`, 80, 24, &fakeScreen{})
	if err != nil {
		t.Fatalf("Start after close = %v", err)
	}
	next.Stop("test")
	next.Wait()
}

func TestPreflight(t *testing.T) {
	tests := []struct {
		name     string
		nopasswd string
		want     error
	}{
		{"password required", "0", nil},
		{"nopasswd refused", "1", ErrNoPassword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := installFakeSudo(t)
			t.Setenv("FAKE_SUDO_NOPASSWD", tt.nopasswd)
			// cleanEnv drops the variable; the fake reads it through a wrapper.
			path, _ := runner.SudoPath()
			wrapper := filepath.Join(filepath.Dir(path), "sudo-wrapper")
			os.WriteFile(wrapper, []byte("#!/bin/sh\nFAKE_SUDO_NOPASSWD="+tt.nopasswd+" exec "+path+" \"$@\"\n"), 0o755)
			runner.config.SudoPath = wrapper
			if err := runner.Preflight(context.Background()); !errors.Is(err, tt.want) {
				t.Fatalf("Preflight = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRejectsMultiLineCommand(t *testing.T) {
	runner := installFakeSudo(t)
	if _, err := runner.Start("true\nid", 80, 24, &fakeScreen{}); err == nil {
		t.Fatal("multi-line command accepted")
	}
}
