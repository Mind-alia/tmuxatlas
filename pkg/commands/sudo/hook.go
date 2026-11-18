package sudocmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/LosFurina/tmuxatlas/pkg/common"
)

// hookInput is the part of Claude Code's PreToolUse payload the hook reads.
type hookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

const denyReason = `sudo ne peut pas marcher ici : pas de TTY, et ce compte n'a pas de droits sudo.
Demande à un humain avec :
  tmuxatlas sudo-request --reason "<pourquoi>" -- "<commande sans sudo>"
Un administrateur ouvrira un terminal où ta commande sera proposée ; il la lancera lui-même (ou autre chose).
Tu recevras seulement {status: done|refused|expired|cancelled, note} : jamais la sortie. Vérifie ensuite toi-même l'effet.
Si la commande sort avec 75, relance : tmuxatlas sudo-request --wait <id>`

// shellWords splits a command line into words, keeping quoted strings whole
// and treating shell operators as separators. It is a heuristic, not a
// parser: the hook only reminds agents, the Unix account is the boundary.
func shellWords(command string) []string {
	var words []string
	var current strings.Builder
	quote := rune(0)
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		switch {
		case quote != 0:
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			current.WriteRune(r)
		case strings.ContainsRune(" \t\n;&|()`{}<>", r):
			flush()
		case r == '$' && current.Len() == 0:
			// "$(sudo …)": the opening parenthesis separates.
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return words
}

// usesSudo reports whether a shell command runs sudo directly.
func usesSudo(command string) bool {
	for _, word := range shellWords(command) {
		if word == "sudo" || strings.HasSuffix(word, "/sudo") {
			return true
		}
	}
	return false
}

func hookDecision(input io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return nil, err
	}
	var payload hookInput
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("entrée du hook illisible : %w", err)
	}
	if payload.ToolName != "Bash" || !usesSudo(payload.ToolInput.Command) {
		return nil, nil
	}
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": denyReason,
		},
	})
}

func init() {
	common.RegisterCommand(&cli.Command{
		Name:  "sudo-hook",
		Usage: "Claude Code PreToolUse hook that redirects sudo to sudo-request",
		Description: `Reads the PreToolUse JSON on stdin. When a Bash command calls sudo, it
denies the call and tells the agent to use "tmuxatlas sudo-request".
Installed by "tmuxatlas agent-setup --sudo-hook".`,
		Action: func(_ context.Context, _ *cli.Command) error {
			output, err := hookDecision(os.Stdin)
			if err != nil {
				return err
			}
			if output != nil {
				fmt.Println(string(output))
			}
			return nil
		},
	})
}
