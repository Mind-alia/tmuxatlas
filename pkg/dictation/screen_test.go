package dictation

import (
	"strings"
	"testing"
)

// Rows captured with `tmux capture-pane -p -e` from a hidden Claude Code pane.
var (
	ruleRow     = "\x1b[38;5;244m" + strings.Repeat("─", 120)
	footerRow   = "\x1b[39m  \x1b[38;5;211m⏵⏵ bypass permissions on\x1b[38;5;246m (shift+tab to cycle)\x1b[39m"
	placeholder = "\x1b[39m❯ \x1b[2mTry \"how does <filepath> work?\"\x1b[0m"
)

func claudeScreen(prompt string, footer ...string) string {
	rows := []string{" ▐▛███▜▌   Claude Code v2.1.321", "", ruleRow, prompt, ruleRow, footerRow}
	return strings.Join(append(rows, footer...), "\n")
}

func TestScreenPrompt(t *testing.T) {
	cases := []struct{ name, prompt, want string }{
		{"placeholder only", placeholder, ""},
		{"transcript", "\x1b[39m❯ Je le fais aller sur le mur.", "Je le fais aller sur le mur."},
		{"recording glyph", "\x1b[39m❯ █", ""},
		{"wrapped transcript", "\x1b[39m❯ Corrige le test\n  du mur", "Corrige le test du mur"},
		// 38;2;r;g;b is a color: its 2 does not dim the text.
		{"true color", "\x1b[38;2;200;200;200m❯ Bonjour\x1b[0m", "Bonjour"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseScreen(claudeScreen(tc.prompt)).prompt(); got != tc.want {
				t.Fatalf("prompt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScreenVoiceAndStartup(t *testing.T) {
	if got := parseScreen(claudeScreen(placeholder, "  listening…")).voice(); got != voiceRecording {
		t.Errorf("recording: %v", got)
	}
	if got := parseScreen(claudeScreen(placeholder, "  No speech detected")).voice(); got != voiceFailed {
		t.Errorf("failed: %v", got)
	}
	// A conversation line quoting the footer says nothing.
	if got := parseScreen("No speech detected\n" + claudeScreen(placeholder)).voice(); got != voiceIdle {
		t.Errorf("conversation: %v", got)
	}
	startups := map[string]startupScreen{
		claudeScreen(placeholder): startupReady,
		"claude · Session : Aris2\n  1. Aris\n  2. Aris2  (Entrée)\nChoix : ":                                                                           startupMenu,
		"claude · Qui utilise cette session ?\n  1. Aris\nChoix : ":                                                                                     startupMenuWithoutDefault,
		"Novanns n'a pas encore de compte claude : connexion.":                                                                                          startupNoAccount,
		strings.Join([]string{ruleRow, " Quick safety check", " ❯ No, exit", "   Yes, I trust this folder", " Enter to confirm · Esc to cancel"}, "\n"): startupTrust,
	}
	for capture, want := range startups {
		if got := parseScreen(capture).startup(); got != want {
			t.Errorf("startup(%q) = %v, want %v", capture[:20], got, want)
		}
	}
}

func TestSessionName(t *testing.T) {
	name := sessionName("Novanns2(Camille)")
	if !strings.HasPrefix(name, "tmuxatlas-dictation-Novanns2-Camille-") || strings.ContainsAny(name, "():. ") {
		t.Fatalf("name = %q", name)
	}
	if sessionName("Aris") == sessionName("Aris2") || sessionName("") != "tmuxatlas-dictation-default" {
		t.Fatal("each person needs a hidden session of their own")
	}
}
