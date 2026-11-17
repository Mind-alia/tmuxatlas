package monitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldProbeClaude(t *testing.T) {
	now := time.Date(2026, 11, 16, 15, 54, 0, 0, time.UTC)
	windows := []QuotaWindow{
		{Label: "5 h", UsedPercent: 45, ResetsAt: now.Add(2 * time.Hour).Unix()},
		{Label: "7 j", UsedPercent: 40, ResetsAt: now.Add(72 * time.Hour).Unix()},
	}

	if !shouldProbeClaude(0, windows, false, 0, now) {
		t.Fatal("expected probe when quotaAt is missing")
	}
	if !shouldProbeClaude(now.Add(-2*time.Minute).Unix(), nil, false, 0, now) {
		t.Fatal("expected probe when windows are missing")
	}
	if shouldProbeClaude(now.Add(-45*time.Second).Unix(), windows, true, now.Unix(), now) {
		t.Fatal("expected freshness skip within 90s when no window expired since fetch")
	}
	if shouldProbeClaude(now.Add(-5*time.Minute).Unix(), windows, false, now.Add(-1*time.Hour).Unix(), now) {
		t.Fatal("expected idle skip within 15m when no live session, no recent activity, and no reset")
	}
	if !shouldProbeClaude(now.Add(-2*time.Minute).Unix(), windows, true, 0, now) {
		t.Fatal("expected probe on 2m cadence when live tmux session is active")
	}
	if !shouldProbeClaude(now.Add(-5*time.Minute).Unix(), windows, false, now.Add(-1*time.Minute).Unix(), now) {
		t.Fatal("expected probe when tokens were consumed after last quota fetch")
	}
	if !shouldProbeClaude(now.Add(-15*time.Minute).Unix(), windows, false, 0, now) {
		t.Fatal("expected probe once idle age reaches 15m")
	}

	// Exact Aris2 screenshot case: fetched 3h ago, 5h reset 1h ago, tokens used 8m ago.
	expiredWindows := []QuotaWindow{
		{Label: "5 h", UsedPercent: 100, ResetsAt: now.Add(-1 * time.Hour).Unix()},
		{Label: "7 j", UsedPercent: 40, ResetsAt: now.Add(144 * time.Hour).Unix()},
	}
	if !shouldProbeClaude(now.Add(-3*time.Hour).Unix(), expiredWindows, false, now.Add(-8*time.Minute).Unix(), now) {
		t.Fatal("expected probe when 5h window expired after last fetch")
	}
}

func TestClaudeProbeHelpers(t *testing.T) {
	dir := t.TempDir()
	trusted := filepath.Join(dir, "repo")
	if err := os.MkdirAll(trusted, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, ".claude.json")
	payload := `{"cachedUsageUtilization":{"fetchedAtMs":1794841017571},"projects":{"/nonexistent/path":{"hasTrustDialogAccepted":true},"` + trusted + `":{"hasTrustDialogAccepted":true}}}`
	if err := os.WriteFile(cfgPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readClaudeFetchedAtMs(cfgPath); got != 1794841017571 {
		t.Fatalf("readClaudeFetchedAtMs = %d, want 1794841017571", got)
	}
	if got := trustedWorkspaceForClaude(cfgPath); got != trusted {
		t.Fatalf("trustedWorkspaceForClaude = %q, want %q", got, trusted)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-secret")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret")
	env := buildClaudeProbeEnv(dir)
	var hasCfgDir bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatalf("expected conflicting env var stripped, found %s", kv)
		}
		if kv == "CLAUDE_CONFIG_DIR="+dir {
			hasCfgDir = true
		}
	}
	if !hasCfgDir {
		t.Fatal("expected CLAUDE_CONFIG_DIR to be set")
	}
}
