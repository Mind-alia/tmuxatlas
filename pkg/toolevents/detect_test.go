package toolevents

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// startAs runs sleep under the name agy, directly or through a launcher.
func startAs(t *testing.T, launcher bool) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is unavailable")
	}
	agy := filepath.Join(t.TempDir(), "agy")
	if err := os.Symlink(sleep, agy); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(agy, "30")
	if launcher {
		// "; true" keeps sh as the parent, as python3 is for agy's launcher.
		command = exec.Command("sh", "-c", agy+" 30; true")
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
}

func TestPaneAgent(t *testing.T) {
	cases := []struct {
		name     string
		launcher bool
	}{{"agy started directly", false}, {"agy behind a launcher", true}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			startAs(t, tc.launcher)
			deadline := time.Now().Add(5 * time.Second)
			for PaneAgent("python3", os.Getpid()) != ToolAgy {
				if time.Now().After(deadline) {
					t.Fatal("agy was not found in the process tree")
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
	for command, want := range map[string]Tool{"claude": ToolClaude, "agy": ToolAgy, "zsh": "", "vim": ""} {
		if got := PaneAgent(command, 0); got != want {
			t.Errorf("PaneAgent(%q) = %q, want %q", command, got, want)
		}
	}
}
