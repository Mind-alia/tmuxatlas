package sudocmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUsesSudo(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"sudo apt-get install jq", true},
		{"/usr/bin/sudo -i", true},
		{"cd /tmp && sudo make install", true},
		{"ls | sudo tee /etc/x", true},
		{"echo $(sudo id)", true},
		{"FOO=1 sudo env", true},
		{"tmuxatlas sudo-request -- \"apt-get install -y jq\"", false},
		{"echo 'run sudo later'", false},
		{"grep -r pseudo .", false},
		{"sudoku --solve", false},
		{"ls -la", false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := usesSudo(tt.command); got != tt.want {
				t.Fatalf("usesSudo(%q) = %v, want %v (words %q)", tt.command, got, tt.want, shellWords(tt.command))
			}
		})
	}
}

func TestHookDecision(t *testing.T) {
	tests := []struct {
		name  string
		input string
		deny  bool
	}{
		{"bash with sudo", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"sudo id"}}`, true},
		{"bash without sudo", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"id"}}`, false},
		{"other tool", `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"command":"sudo id"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := hookDecision(strings.NewReader(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			if (output != nil) != tt.deny {
				t.Fatalf("output = %s", output)
			}
			if !tt.deny {
				return
			}
			var decoded struct {
				HookSpecificOutput struct {
					HookEventName      string `json:"hookEventName"`
					PermissionDecision string `json:"permissionDecision"`
					Reason             string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(output, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.HookSpecificOutput.PermissionDecision != "deny" || decoded.HookSpecificOutput.HookEventName != "PreToolUse" ||
				!strings.Contains(decoded.HookSpecificOutput.Reason, "tmuxatlas sudo-request") {
				t.Fatalf("decision = %+v", decoded)
			}
		})
	}
}
