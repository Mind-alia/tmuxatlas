package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty/v2"
)

type agyQuotaResult struct {
	fetchedAt int64
	plan      string
	windows   []QuotaWindow
}

var (
	ansiCSIRe  = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	ansiCSIPos = regexp.MustCompile(`\x1b\[[0-9;?]*[Hf]`)
	ansiOSCRe  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiEscRe  = regexp.MustCompile(`\x1b.`)
	agyPctRe   = regexp.MustCompile(`(\d+(?:\.\d+)?)%`)
	agyDurRe   = regexp.MustCompile(`(\d+)\s*([hm])`)
	agyPlanRe  = regexp.MustCompile(`\((Google AI [^)]+)\)`)
)

func findNonShimBinary(name string) string {
	home, _ := os.UserHomeDir()
	shimDir := ""
	if home != "" {
		shimDir = filepath.Join(home, ".local", "share", "ai-accounts", "shims")
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || (shimDir != "" && filepath.Clean(dir) == filepath.Clean(shimDir)) {
			continue
		}
		cand := filepath.Join(dir, name)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand
		}
	}
	if home != "" {
		cand := filepath.Join(home, ".local", "bin", name)
		if info, err := os.Stat(cand); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return cand
		}
	}
	return ""
}

func findAgyHelper() string {
	if env := strings.TrimSpace(os.Getenv("AGY_ACCOUNTS_HELPER")); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env
		}
		return ""
	}
	home, _ := os.UserHomeDir()
	for _, cand := range []string{
		filepath.Join(home, "cli-accounts", "agy_accounts.py"),
		"/root/cli-accounts/agy_accounts.py",
	} {
		if cand != "" {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	return ""
}

func trustedWorkspaceForAgy(configDir string) string {
	raw, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err == nil {
		var s struct {
			TrustedWorkspaces []string `json:"trustedWorkspaces"`
		}
		if json.Unmarshal(raw, &s) == nil {
			for _, ws := range s.TrustedWorkspaces {
				if info, err := os.Stat(ws); err == nil && info.IsDir() {
					return ws
				}
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

const (
	agyIdleMaxAge    = 10 * time.Minute
	quotaMinFreshAge = 90 * time.Second
)

type agyProbeTarget struct {
	id           string
	profileDir   string
	live         bool
	lastActivity int64
	quotaAt      int64
	windows      []QuotaWindow
	force        bool
}

// shouldProbeQuota determines whether a CLI account needs a /usage quota refresh.
func shouldProbeQuota(quotaAt int64, windows []QuotaWindow, idleMaxAge time.Duration, live bool, lastActivity int64, now time.Time) bool {
	if quotaAt <= 0 || len(windows) == 0 {
		return true
	}
	nowUnix := now.Unix()
	expiredAfterFetch := false
	expiredStillUsed := false
	for _, w := range windows {
		if w.ResetsAt > quotaAt && w.ResetsAt <= nowUnix && (w.UsedPercent > 0 || live || lastActivity >= w.ResetsAt) {
			expiredAfterFetch = true
			break
		}
		if w.ResetsAt > 0 && w.ResetsAt <= nowUnix && w.UsedPercent > 0 {
			expiredStillUsed = true
		}
	}
	if !expiredAfterFetch && nowUnix-quotaAt < int64(quotaMinFreshAge/time.Second) {
		return false
	}
	if expiredAfterFetch || expiredStillUsed {
		return true
	}
	if nowUnix-quotaAt >= int64(idleMaxAge/time.Second) {
		return true
	}
	if live {
		return true
	}
	if lastActivity > 0 && lastActivity >= quotaAt-60 {
		return true
	}
	return false
}

func shouldProbeAgy(prev agyQuotaResult, hasPrev bool, live bool, lastActivity int64, now time.Time) bool {
	if !hasPrev {
		return true
	}
	return shouldProbeQuota(prev.fetchedAt, prev.windows, agyIdleMaxAge, live, lastActivity, now)
}

func probeAllAgyQuotas(ctx context.Context, root string, now time.Time, prev map[string]agyQuotaResult, hints map[string]agyProbeTarget) (map[string]agyQuotaResult, []string, int) {
	agyBin := findNonShimBinary("agy")
	helper := findAgyHelper()
	if agyBin == "" {
		return nil, []string{"agy binary not found"}, 0
	}
	if helper == "" {
		return nil, []string{"agy_accounts.py helper not found"}, 0
	}
	if root == "" {
		return nil, []string{"accounts root empty"}, 0
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		return nil, []string{"unshare not found: " + err.Error()}, 0
	}
	if _, err := exec.LookPath("python3"); err != nil {
		return nil, []string{"python3 not found: " + err.Error()}, 0
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, []string{"readdir failed: " + err.Error()}, 0
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
		if json.Unmarshal(raw, &prof) != nil || prof.Provider != "agy" {
			continue
		}
		if !hasAuthFile(profileDir, "agy") {
			continue
		}
		id := prof.ID
		if id == "" {
			id = entry.Name()
		}
		hint := hints[id]
		prevRes, hasPrev := prev[id]
		if !hint.force && !shouldProbeAgy(prevRes, hasPrev, hint.live, hint.lastActivity, now) {
			skipped++
			continue
		}
		targets = append(targets, agyProbeTarget{id: id, profileDir: profileDir})
	}
	if len(targets) == 0 {
		return nil, nil, skipped
	}

	// Probe sequentially to keep peak RAM and CPU spikes low on 2-minute ticks.
	results := make(map[string]agyQuotaResult, len(targets))
	var errs []string
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		res, errMsg := probeSingleAgyQuota(probeCtx, agyBin, helper, t.profileDir, now)
		cancel()
		if errMsg == "" {
			results[t.id] = res
		} else {
			errs = append(errs, t.id[:min(8, len(t.id))]+": "+errMsg)
		}
	}
	return results, errs, skipped
}

func probeSingleAgyQuota(ctx context.Context, agyBin, helper, profileDir string, now time.Time) (agyQuotaResult, string) {
	cmd := exec.CommandContext(
		ctx,
		"unshare", "--mount", "--propagation", "private",
		"python3", helper, profileDir, "run", agyBin, "--log-file", "/dev/null",
	)
	cmd.Dir = trustedWorkspaceForAgy(filepath.Join(profileDir, "config"))
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 65, Cols: 120})
	if err != nil {
		return agyQuotaResult{}, "pty start: " + err.Error()
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

	readUntil := func(markers [][]byte, abortMarkers [][]byte, timeout time.Duration) (string, bool) {
		var acc bytes.Buffer
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return acc.String(), false
			case <-timer.C:
				return acc.String(), false
			case chunk, ok := <-chunkCh:
				if !ok {
					return acc.String(), false
				}
				acc.Write(chunk)
				data := acc.Bytes()
				for _, am := range abortMarkers {
					if bytes.Contains(data, am) {
						return acc.String(), false
					}
				}
				for _, m := range markers {
					if bytes.Contains(data, m) {
						return acc.String(), true
					}
				}
			}
		}
	}

	headerRaw, ready := readUntil(
		[][]byte{[]byte("for shortcuts")},
		[][]byte{[]byte("trust the contents")},
		12*time.Second,
	)
	if !ready {
		lines := cleanTerminalText(headerRaw)
		tail := strings.Join(lines, " | ")
		if len(tail) > 200 {
			tail = tail[len(tail)-200:]
		}
		return agyQuotaResult{}, "ready timeout/abort: " + tail
	}

	if _, err := ptmx.Write([]byte("/usage\r")); err != nil {
		return agyQuotaResult{}, "write /usage: " + err.Error()
	}

	usageRaw, gotUsage := readUntil(
		[][]byte{[]byte("Within each group")},
		nil,
		10*time.Second,
	)
	// Allow final bar redraws to settle briefly.
	settleTimer := time.NewTimer(250 * time.Millisecond)
	var extra bytes.Buffer
settleLoop:
	for {
		select {
		case <-ctx.Done():
			break settleLoop
		case <-settleTimer.C:
			break settleLoop
		case chunk, ok := <-chunkCh:
			if !ok {
				break settleLoop
			}
			extra.Write(chunk)
		}
	}
	settleTimer.Stop()

	_, _ = ptmx.Write([]byte("\x1b"))
	time.Sleep(100 * time.Millisecond)
	_, _ = ptmx.Write([]byte("/quit\r"))

	if !gotUsage && !strings.Contains(usageRaw+extra.String(), "Models & Quota") {
		return agyQuotaResult{}, "usage screen timeout"
	}
	res := parseAgyUsageScreen(headerRaw+"\n"+usageRaw+extra.String(), now)
	if len(res.windows) == 0 {
		return res, "parsed 0 windows"
	}
	return res, ""
}

func cleanTerminalText(raw string) []string {
	s := ansiCSIPos.ReplaceAllString(raw, "\n")
	s = ansiOSCRe.ReplaceAllString(s, "")
	s = ansiCSIRe.ReplaceAllString(s, "")
	s = ansiEscRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseAgyUsageScreen(raw string, now time.Time) agyQuotaResult {
	lines := cleanTerminalText(raw)
	res := agyQuotaResult{fetchedAt: now.Unix()}

	type winKey struct {
		group string
		win   string
	}
	parsed := make(map[winKey]QuotaWindow)

	var currentGroup string
	var pendingWin string
	var lastKey winKey

	setWindow := func(group, win string, usedPct float64) {
		key := winKey{group: group, win: win}
		winMin := 10080
		if win == "5 h" {
			winMin = 300
		}
		parsed[key] = QuotaWindow{
			Label:       group + " " + win,
			UsedPercent: math.Round(usedPct*100) / 100,
			WindowMin:   winMin,
		}
		lastKey = key
	}

	for _, line := range lines {
		if res.plan == "" {
			if m := agyPlanRe.FindStringSubmatch(line); len(m) == 2 {
				plan := strings.TrimPrefix(m[1], "Google AI ")
				res.plan = plan
			}
		}

		switch {
		case strings.Contains(line, "GEMINI MODELS"):
			currentGroup = "Gemini"
			pendingWin = ""
		case strings.Contains(line, "CLAUDE AND GPT MODELS"):
			currentGroup = "Claude/GPT"
			pendingWin = ""
		case strings.Contains(line, "GEMINI 4 ARGON"):
			currentGroup = "Argon"
			pendingWin = ""
		case strings.Contains(line, "Weekly Limit"):
			pendingWin = "7 j"
		case strings.Contains(line, "Five Hour Limit"):
			pendingWin = "5 h"
		}

		if currentGroup != "" && pendingWin != "" {
			if m := agyPctRe.FindStringSubmatch(line); len(m) == 2 {
				if rem, err := strconv.ParseFloat(m[1], 64); err == nil {
					used := math.Max(0, math.Min(100, 100-rem))
					setWindow(currentGroup, pendingWin, used)
					pendingWin = ""
					continue
				}
			}
		}

		if lastKey.group != "" && strings.Contains(line, "Refreshes in") {
			if d := parseAgyRefreshDuration(line); d > 0 {
				w := parsed[lastKey]
				w.ResetsAt = now.Add(d).Unix()
				parsed[lastKey] = w
			}
		}
	}

	for _, group := range []string{"Gemini", "Claude/GPT", "Argon"} {
		for _, win := range []string{"5 h", "7 j"} {
			if w, ok := parsed[winKey{group: group, win: win}]; ok {
				res.windows = append(res.windows, w)
			}
		}
	}
	return res
}

func parseAgyRefreshDuration(line string) time.Duration {
	matches := agyDurRe.FindAllStringSubmatch(line, -1)
	if len(matches) == 0 {
		return 0
	}
	var total time.Duration
	for _, m := range matches {
		val, err := strconv.Atoi(m[1])
		if err != nil || val < 0 {
			continue
		}
		switch m[2] {
		case "h":
			total += time.Duration(val) * time.Hour
		case "m":
			total += time.Duration(val) * time.Minute
		}
	}
	return total
}
