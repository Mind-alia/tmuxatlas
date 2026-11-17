package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/creack/pty/v2"
)

const claudeIdleMaxAge = 15 * time.Minute

var claudeClearedEnv = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_PROFILE",
	"ANTHROPIC_FEDERATION_RULE_ID",
	"ANTHROPIC_ORGANIZATION_ID",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
}

func readClaudeFetchedAtMs(claudeJSON string) int64 {
	raw, err := os.ReadFile(claudeJSON)
	if err != nil {
		return 0
	}
	var doc struct {
		CachedUsageUtilization *struct {
			FetchedAtMs int64 `json:"fetchedAtMs"`
		} `json:"cachedUsageUtilization"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.CachedUsageUtilization == nil {
		return 0
	}
	return doc.CachedUsageUtilization.FetchedAtMs
}

func trustedWorkspaceForClaude(claudeJSON string) string {
	raw, err := os.ReadFile(claudeJSON)
	if err == nil {
		var doc struct {
			Projects map[string]struct {
				HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
			} `json:"projects"`
		}
		if json.Unmarshal(raw, &doc) == nil && len(doc.Projects) > 0 {
			paths := make([]string, 0, len(doc.Projects))
			for p, entry := range doc.Projects {
				if entry.HasTrustDialogAccepted {
					paths = append(paths, p)
				}
			}
			sort.Strings(paths)
			for _, p := range paths {
				if info, err := os.Stat(p); err == nil && info.IsDir() {
					return p
				}
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

func buildClaudeProbeEnv(configDir string) []string {
	blocked := make(map[string]bool, len(claudeClearedEnv))
	for _, k := range claudeClearedEnv {
		blocked[k] = true
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || blocked[key] || key == "CLAUDE_CONFIG_DIR" || key == "TERM" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "CLAUDE_CONFIG_DIR="+configDir, "TERM=xterm-256color")
	return env
}

func shouldProbeClaude(quotaAt int64, windows []QuotaWindow, live bool, lastActivity int64, now time.Time) bool {
	return shouldProbeQuota(quotaAt, windows, claudeIdleMaxAge, live, lastActivity, now)
}

func probeAllClaudeQuotas(ctx context.Context, root string, now time.Time, hints map[string]agyProbeTarget) (int, []string, int) {
	claudeBin := findNonShimBinary("claude")
	if claudeBin == "" {
		return 0, []string{"claude binary not found"}, 0
	}
	if root == "" {
		return 0, []string{"accounts root empty"}, 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, []string{"readdir failed: " + err.Error()}, 0
	}

	var targets []agyProbeTarget
	skipped := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		profileDir := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(profileDir, "profile.json"))
		if err != nil {
			continue
		}
		var prof accountProfile
		if json.Unmarshal(raw, &prof) != nil || prof.Provider != "claude" {
			continue
		}
		if !hasAuthFile(profileDir, "claude") {
			continue
		}
		id := prof.ID
		if id == "" {
			id = entry.Name()
		}
		hint := hints[id]
		quotaAt := hint.quotaAt
		if quotaAt <= 0 {
			quotaAt = readClaudeFetchedAtMs(filepath.Join(profileDir, "config", ".claude.json")) / 1000
		}
		if !hint.force && !shouldProbeClaude(quotaAt, hint.windows, hint.live, hint.lastActivity, now) {
			skipped++
			continue
		}
		targets = append(targets, agyProbeTarget{id: id, profileDir: profileDir})
	}
	if len(targets) == 0 {
		return 0, nil, skipped
	}

	refreshed := 0
	var errs []string
	// Probe sequentially to keep RAM and CPU usage low on 2-minute ticks.
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		errMsg := probeSingleClaudeQuota(probeCtx, claudeBin, t.profileDir)
		cancel()
		if errMsg == "" {
			refreshed++
		} else {
			errs = append(errs, t.id[:min(8, len(t.id))]+": "+errMsg)
		}
	}
	return refreshed, errs, skipped
}

func probeSingleClaudeQuota(ctx context.Context, claudeBin, profileDir string) string {
	configDir := filepath.Join(profileDir, "config")
	claudeJSON := filepath.Join(configDir, ".claude.json")
	beforeMs := readClaudeFetchedAtMs(claudeJSON)

	cmd := exec.CommandContext(ctx, claudeBin, "--safe-mode", "--tools", "", "--strict-mcp-config")
	cmd.Dir = trustedWorkspaceForClaude(claudeJSON)
	cmd.Env = buildClaudeProbeEnv(configDir)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 100})
	if err != nil {
		return "pty start: " + err.Error()
	}
	defer func() {
		_ = ptmx.Close()
		killProcessGroup(cmd)
		_ = cmd.Wait()
	}()

	chunkCh := make(chan []byte, 32)
	go func() {
		defer close(chunkCh)
		buf := make([]byte, 16384)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				chunkCh <- cp
			}
			if err != nil {
				return
			}
		}
	}()

	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()

	var acc bytes.Buffer
	sentTrust := false
	sentUsage := false
	sentConfirm := false

	for {
		select {
		case <-ctx.Done():
			if readClaudeFetchedAtMs(claudeJSON) > beforeMs {
				return ""
			}
			lines := cleanTerminalText(acc.String())
			tail := strings.Join(lines, " | ")
			if len(tail) > 200 {
				tail = tail[len(tail)-200:]
			}
			return "timeout waiting for .claude.json usage update: " + tail
		case <-poll.C:
			if sentUsage && readClaudeFetchedAtMs(claudeJSON) > beforeMs {
				return ""
			}
		case chunk, ok := <-chunkCh:
			if !ok {
				if readClaudeFetchedAtMs(claudeJSON) > beforeMs {
					return ""
				}
				return "claude exited before updating usage"
			}
			if bytes.Contains(chunk, []byte("\x1b[6n")) {
				_, _ = ptmx.Write([]byte("\x1b[1;1R"))
			}
			acc.Write(chunk)
			clean := strings.Join(cleanTerminalText(acc.String()), "\n")

			if !sentTrust && !sentUsage && (strings.Contains(clean, "trust the contents") || strings.Contains(clean, "Is this a project you created")) {
				sentTrust = true
				time.Sleep(150 * time.Millisecond)
				_, _ = ptmx.Write([]byte("\x1b[B\r"))
				continue
			}
			if !sentUsage && (strings.Contains(clean, "Claude Code") || strings.Contains(clean, "Welcome") || strings.Contains(clean, "Try ") || strings.Contains(clean, "❯")) {
				sentUsage = true
				_, _ = ptmx.Write([]byte("/usage"))
				time.Sleep(250 * time.Millisecond)
				_, _ = ptmx.Write([]byte("\r"))
				continue
			}
			if sentUsage && !sentConfirm && strings.Contains(clean, "Show plan usage limits") {
				sentConfirm = true
				_, _ = ptmx.Write([]byte("\r"))
			}
		}
	}
}
