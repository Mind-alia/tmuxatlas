package agentsetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupClaudeDirMergesHooks(t *testing.T) {
	tests := []struct {
		name     string
		sudoHook bool
	}{
		{"notify hooks only", false},
		{"with sudo hook", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			existing := `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Write", "hooks": [{"type": "command", "command": "my-linter"}]},
      {"matcher": "", "hooks": [{"type": "command", "command": "/old/tmuxatlas notify -t claude -s active || true"}]}
    ],
    "SessionStart": [{"matcher": "", "hooks": [{"type": "command", "command": "echo hi"}]}]
  }
}`
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(existing), 0o644); err != nil {
				t.Fatal(err)
			}
			installSudoHook = tt.sudoHook
			defer func() { installSudoHook = false }()
			for range 2 { // running twice must not duplicate hooks
				if err := setupClaudeDir(dir, "/bin/tmuxatlas", true); err != nil {
					t.Fatal(err)
				}
			}
			data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
			text := string(data)
			var settings map[string]any
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatal(err)
			}
			if settings["model"] != "opus" {
				t.Fatal("other settings were lost")
			}
			for _, want := range []string{"my-linter", "echo hi", "/bin/tmuxatlas notify -t claude -s waiting"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %s", want, text)
				}
			}
			if strings.Contains(text, "/old/tmuxatlas") {
				t.Fatal("stale TmuxAtlas hook kept")
			}
			if got := strings.Count(text, "-s active -m 'Using tool'"); got != 1 {
				t.Fatalf("notify PreToolUse hook count = %d", got)
			}
			if got := strings.Count(text, "sudo-hook"); (got == 1) != tt.sudoHook || got > 1 {
				t.Fatalf("sudo-hook count = %d", got)
			}
			if tt.sudoHook && strings.Contains(text, "sudo-hook || true") {
				t.Fatal("sudo hook must not swallow its decision")
			}
		})
	}
}
