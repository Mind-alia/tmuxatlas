package monitor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const codexIdleMaxAge = 4 * time.Minute

var codexClearedEnv = []string{
	"OPENAI_API_KEY",
	"CODEX_API_KEY",
	"CODEX_ACCESS_TOKEN",
}

type codexQuotaResult struct {
	fetchedAt int64
	email     string
	plan      string
	windows   []QuotaWindow
	extra     *ExtraUsage
}

func buildCodexProbeEnv(configDir string) []string {
	blocked := make(map[string]bool, len(codexClearedEnv))
	for _, k := range codexClearedEnv {
		blocked[k] = true
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || blocked[key] || key == "CODEX_HOME" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "CODEX_HOME="+configDir)
	return env
}

func shouldProbeCodex(prev codexQuotaResult, hasPrev bool, quotaAt int64, windows []QuotaWindow, live bool, lastActivity int64, now time.Time) bool {
	if !hasPrev {
		return true
	}
	effectiveQuotaAt := maxInt64(prev.fetchedAt, quotaAt)
	effectiveWins := windows
	if len(effectiveWins) == 0 {
		effectiveWins = prev.windows
	}
	return shouldProbeQuota(effectiveQuotaAt, effectiveWins, codexIdleMaxAge, live, lastActivity, now)
}

func probeAllCodexQuotas(ctx context.Context, root string, now time.Time, prev map[string]codexQuotaResult, hints map[string]agyProbeTarget) (map[string]codexQuotaResult, []string, int) {
	codexBin := findNonShimBinary("codex")
	if codexBin == "" {
		return nil, []string{"codex binary not found"}, 0
	}
	if root == "" {
		return nil, []string{"accounts root empty"}, 0
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
		if json.Unmarshal(raw, &prof) != nil || prof.Provider != "codex" {
			continue
		}
		if !hasAuthFile(profileDir, "codex") {
			continue
		}
		id := prof.ID
		if id == "" {
			id = entry.Name()
		}
		hint := hints[id]
		prevRes, hasPrev := prev[id]
		if !hint.force && !shouldProbeCodex(prevRes, hasPrev, hint.quotaAt, hint.windows, hint.live, hint.lastActivity, now) {
			skipped++
			continue
		}
		targets = append(targets, agyProbeTarget{id: id, profileDir: profileDir})
	}
	if len(targets) == 0 {
		return nil, nil, skipped
	}

	results := make(map[string]codexQuotaResult, len(targets))
	var errs []string
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, errMsg := probeSingleCodexQuota(probeCtx, codexBin, t.profileDir, now)
		cancel()
		if errMsg == "" {
			results[t.id] = res
		} else {
			errs = append(errs, t.id[:min(8, len(t.id))]+": "+errMsg)
		}
	}
	return results, errs, skipped
}

func probeSingleCodexQuota(ctx context.Context, codexBin, profileDir string, now time.Time) (codexQuotaResult, string) {
	configDir := filepath.Join(profileDir, "config")
	cmd := exec.CommandContext(
		ctx,
		codexBin,
		"-c", `cli_auth_credentials_store="file"`,
		"app-server",
		"--stdio",
	)
	cmd.Dir = os.TempDir()
	cmd.Env = buildCodexProbeEnv(configDir)
	setProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return codexQuotaResult{}, "stdin pipe: " + err.Error()
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return codexQuotaResult{}, "stdout pipe: " + err.Error()
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return codexQuotaResult{}, "start: " + err.Error()
	}
	defer func() {
		_ = stdin.Close()
		killProcessGroup(cmd)
		_ = cmd.Wait()
	}()

	lineCh := make(chan []byte, 32)
	go func() {
		defer close(lineCh)
		_ = scanLines(stdout, func(line []byte) {
			cp := make([]byte, len(line))
			copy(cp, line)
			select {
			case lineCh <- cp:
			case <-ctx.Done():
			}
		})
	}()

	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"tmuxatlas","version":"1.0"}}}` + "\n"
	if _, err := stdin.Write([]byte(initReq)); err != nil {
		return codexQuotaResult{}, "write initialize: " + err.Error()
	}

	var (
		sentFollowup  bool
		rateLimitsRaw []byte
		accountRaw    []byte
		accountTimer  <-chan time.Time
	)

	for {
		select {
		case <-ctx.Done():
			if len(rateLimitsRaw) > 0 {
				return parseCodexAppServerQuota(rateLimitsRaw, accountRaw, now)
			}
			return codexQuotaResult{}, "timeout waiting for codex app-server response"
		case <-accountTimer:
			if len(rateLimitsRaw) > 0 {
				return parseCodexAppServerQuota(rateLimitsRaw, accountRaw, now)
			}
		case line, ok := <-lineCh:
			if !ok {
				if len(rateLimitsRaw) > 0 {
					return parseCodexAppServerQuota(rateLimitsRaw, accountRaw, now)
				}
				return codexQuotaResult{}, "codex app-server exited before returning rateLimits"
			}
			var env struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(line, &env) != nil || env.ID == 0 {
				continue
			}
			switch env.ID {
			case 1:
				if !sentFollowup {
					sentFollowup = true
					followup := `{"jsonrpc":"2.0","method":"initialized","params":{}}` + "\n" +
						`{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read","params":{}}` + "\n" +
						`{"jsonrpc":"2.0","id":3,"method":"account/read","params":{}}` + "\n"
					if _, err := stdin.Write([]byte(followup)); err != nil {
						return codexQuotaResult{}, "write requests: " + err.Error()
					}
				}
			case 2:
				rateLimitsRaw = line
				if len(accountRaw) > 0 {
					return parseCodexAppServerQuota(rateLimitsRaw, accountRaw, now)
				}
				t := time.NewTimer(1500 * time.Millisecond)
				defer t.Stop()
				accountTimer = t.C
			case 3:
				accountRaw = line
				if len(rateLimitsRaw) > 0 {
					return parseCodexAppServerQuota(rateLimitsRaw, accountRaw, now)
				}
			}
		}
	}
}

type codexAppServerWinRaw struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

func parseCodexAppServerQuota(rateLimitsRaw, accountRaw []byte, now time.Time) (codexQuotaResult, string) {
	var rlResp struct {
		Result *struct {
			OrdinaryUsageAllowed *bool `json:"ordinaryUsageAllowed"`
			RateLimits           *struct {
				PlanType             string                `json:"planType"`
				RateLimitReachedType string                `json:"rateLimitReachedType"`
				Primary              *codexAppServerWinRaw `json:"primary"`
				Secondary            *codexAppServerWinRaw `json:"secondary"`
				Credits              *struct {
					HasCredits bool   `json:"hasCredits"`
					Unlimited  bool   `json:"unlimited"`
					Balance    string `json:"balance"`
				} `json:"credits"`
			} `json:"rateLimits"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rateLimitsRaw, &rlResp); err != nil {
		return codexQuotaResult{}, "unmarshal rateLimits: " + err.Error()
	}
	if rlResp.Error != nil && rlResp.Error.Message != "" {
		return codexQuotaResult{}, "rpc error: " + rlResp.Error.Message
	}
	if rlResp.Result == nil || rlResp.Result.RateLimits == nil {
		return codexQuotaResult{}, "missing rateLimits in response"
	}

	res := codexQuotaResult{
		fetchedAt: now.Unix(),
		plan:      formatCodexPlan(rlResp.Result.RateLimits.PlanType),
	}

	rl := rlResp.Result.RateLimits
	limitReached := rl.RateLimitReachedType == "rate_limit_reached" ||
		(rlResp.Result.OrdinaryUsageAllowed != nil && !*rlResp.Result.OrdinaryUsageAllowed)

	nowUnix := now.Unix()
	for i, w := range []*codexAppServerWinRaw{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		used := w.UsedPercent
		if i == 0 && limitReached && used < 100 && (rl.Secondary == nil || rl.Secondary.UsedPercent < 100) {
			used = 100
		}
		res.windows = append(res.windows, QuotaWindow{
			Label:       formatWindowMinutes(w.WindowDurationMins),
			UsedPercent: used,
			WindowMin:   w.WindowDurationMins,
			ResetsAt:    w.ResetsAt,
			Expired:     w.ResetsAt > 0 && w.ResetsAt <= nowUnix,
		})
	}
	if len(res.windows) == 0 {
		return res, "parsed 0 windows"
	}

	if rl.Credits != nil && (rl.Credits.HasCredits || rl.Credits.Unlimited) {
		res.extra = &ExtraUsage{
			Enabled:   true,
			Balance:   rl.Credits.Balance,
			Unlimited: rl.Credits.Unlimited,
		}
	}

	if len(accountRaw) > 0 {
		var accResp struct {
			Result *struct {
				Account *struct {
					Email    string `json:"email"`
					PlanType string `json:"planType"`
				} `json:"account"`
			} `json:"result"`
		}
		if json.Unmarshal(accountRaw, &accResp) == nil && accResp.Result != nil && accResp.Result.Account != nil {
			res.email = strings.TrimSpace(accResp.Result.Account.Email)
			if res.plan == "" && accResp.Result.Account.PlanType != "" {
				res.plan = formatCodexPlan(accResp.Result.Account.PlanType)
			}
		}
	}

	return res, ""
}
