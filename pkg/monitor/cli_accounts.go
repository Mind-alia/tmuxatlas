package monitor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	window5h  = 5 * time.Hour
	window24h = 24 * time.Hour
	window7d  = 7 * 24 * time.Hour
)

// TokenCounts holds aggregated token usage for a time window.
type TokenCounts struct {
	Input      int64 `json:"input"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write,omitempty"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning,omitempty"`
	Total      int64 `json:"total"`
	Requests   int   `json:"requests,omitempty"`
}

func (t *TokenCounts) add(other TokenCounts) {
	t.Input += other.Input
	t.CacheRead += other.CacheRead
	t.CacheWrite += other.CacheWrite
	t.Output += other.Output
	t.Reasoning += other.Reasoning
	t.Total += other.Total
	t.Requests += other.Requests
}

// QuotaWindow describes a provider rate-limit window (e.g. 5h or 7d).
type QuotaWindow struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	WindowMin   int     `json:"window_minutes,omitempty"`
	ResetsAt    int64   `json:"resets_at,omitempty"` // unix seconds
	Expired     bool    `json:"expired,omitempty"`
}

// ExtraUsage describes paid extra usage / overage or credit balance.
type ExtraUsage struct {
	Enabled        bool    `json:"enabled"`
	Used           float64 `json:"used,omitempty"`
	Limit          float64 `json:"limit,omitempty"`
	UsedPercent    float64 `json:"used_percent,omitempty"`
	Currency       string  `json:"currency,omitempty"`
	LimitReached   bool    `json:"limit_reached,omitempty"`
	DisabledReason string  `json:"disabled_reason,omitempty"`
	Balance        string  `json:"balance,omitempty"`
	Unlimited      bool    `json:"unlimited,omitempty"`
}

// CLILiveSession describes a running tmux session currently bound to a CLI account.
type CLILiveSession struct {
	Session  string `json:"session"`
	Command  string `json:"command,omitempty"`
	Path     string `json:"path,omitempty"`
	Attached bool   `json:"attached,omitempty"`
}

// CLISessionActivity summarizes one recent CLI session/conversation from local logs.
type CLISessionActivity struct {
	ID           string `json:"id"`
	Project      string `json:"project,omitempty"`
	Title        string `json:"title,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Model        string `json:"model,omitempty"`
	Tokens       int64  `json:"tokens"`
	Requests     int    `json:"requests"`
	LastActivity int64  `json:"last_activity"`
	Active       bool   `json:"active,omitempty"`
}

// CLIAccountUsage describes the token and quota state of one ai-accounts profile.
type CLIAccountUsage struct {
	ID             string               `json:"id"`
	CLI            string               `json:"cli"` // claude | codex | agy
	Name           string               `json:"name"`
	Person         string               `json:"person,omitempty"`
	Account        string               `json:"account,omitempty"`
	Plan           string               `json:"plan,omitempty"`
	Connected      bool                 `json:"connected"`
	QuotaSource    string               `json:"quota_source"` // provider_cache | session_events | local_logs_only
	QuotaAt        int64                `json:"quota_at,omitempty"`
	LastActivity   int64                `json:"last_activity,omitempty"`
	LastModel      string               `json:"last_model,omitempty"`
	ContextWindow  int64                `json:"context_window,omitempty"`
	LastTurn       *TokenCounts         `json:"last_turn,omitempty"`
	Windows        []QuotaWindow        `json:"windows,omitempty"`
	Extra          *ExtraUsage          `json:"extra,omitempty"`
	Tokens5h       TokenCounts          `json:"tokens_5h"`
	Tokens24h      TokenCounts          `json:"tokens_24h"`
	Tokens7d       TokenCounts          `json:"tokens_7d"`
	Models7d       []string             `json:"models_7d,omitempty"`
	LiveSessions   []CLILiveSession     `json:"live_sessions,omitempty"`
	RecentSessions []CLISessionActivity `json:"recent_sessions,omitempty"`
}

type accountProfile struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Person   string `json:"person,omitempty"`
	Account  string `json:"account,omitempty"`
}

type tokenEvent struct {
	ts     int64 // unix seconds
	model  string
	tokens TokenCounts
}

type cachedParsedFile struct {
	modTime int64
	size    int64
	events  []tokenEvent
	// Session metadata
	sessionID      string
	sessionProject string
	sessionBranch  string
	// Codex-specific trailing snapshot metadata
	lastTurn      *TokenCounts
	contextWindow int64
	rateLimits    *codexRateLimitsSnapshot
}

type codexRateLimitsSnapshot struct {
	ts               int64
	planType         string
	limitReachedType string
	primary          *codexWindowRaw
	secondary        *codexWindowRaw
	credits          *codexCreditsRaw
}

type codexWindowRaw struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexCreditsRaw struct {
	HasCredits bool   `json:"has_credits"`
	Unlimited  bool   `json:"unlimited"`
	Balance    string `json:"balance"`
}

type cliPersonKey struct {
	cli    string
	person string // lowercased
}

type cachedClaudeConfig struct {
	modTime int64
	size    int64
	email   string
	plan    string
	quotaAt int64
	windows []QuotaWindow
	extra   *ExtraUsage
}

type cachedAgyMeta struct {
	modTime int64
	size    int64
	items   map[string]agyConvSummary
}

type cliAccountsCollector struct {
	dirOverride    string
	collectMu      sync.Mutex
	fileCache      map[string]cachedParsedFile
	claudeCfgCache map[string]cachedClaudeConfig
	agyMetaCache   map[string]cachedAgyMeta
	liveSessionsFn func() map[cliPersonKey][]CLILiveSession
	agyMu          sync.RWMutex
	agyQuotas      map[string]agyQuotaResult
}

func newCLIAccountsCollector(dirOverride string) *cliAccountsCollector {
	var liveFn func() map[cliPersonKey][]CLILiveSession
	if strings.TrimSpace(dirOverride) == "" {
		liveFn = collectLiveTmuxSessions
	}
	return &cliAccountsCollector{
		dirOverride:    dirOverride,
		fileCache:      make(map[string]cachedParsedFile),
		claudeCfgCache: make(map[string]cachedClaudeConfig),
		agyMetaCache:   make(map[string]cachedAgyMeta),
		liveSessionsFn: liveFn,
		agyQuotas:      make(map[string]agyQuotaResult),
	}
}

func (c *cliAccountsCollector) refreshAgyQuotas(ctx context.Context, now time.Time, hints map[string]agyProbeTarget) (int, []string, int) {
	root := resolveAccountsDir(c.dirOverride)
	if root == "" {
		return 0, nil, 0
	}
	c.agyMu.RLock()
	prevSnap := make(map[string]agyQuotaResult, len(c.agyQuotas))
	for k, v := range c.agyQuotas {
		prevSnap[k] = v
	}
	c.agyMu.RUnlock()

	res, errs, skipped := probeAllAgyQuotas(ctx, root, now, prevSnap, hints)
	if len(res) == 0 {
		return 0, errs, skipped
	}
	c.agyMu.Lock()
	for id, q := range res {
		c.agyQuotas[id] = q
	}
	c.agyMu.Unlock()
	return len(res), errs, skipped
}

func (c *cliAccountsCollector) refreshClaudeQuotas(ctx context.Context, now time.Time, hints map[string]agyProbeTarget) (int, []string, int) {
	root := resolveAccountsDir(c.dirOverride)
	if root == "" {
		return 0, nil, 0
	}
	return probeAllClaudeQuotas(ctx, root, now, hints)
}

func resolveAccountsDir(override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	if env := strings.TrimSpace(os.Getenv("AI_ACCOUNTS_DIR")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "ai-accounts")
}

func (c *cliAccountsCollector) collect(now time.Time) []CLIAccountUsage {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	root := resolveAccountsDir(c.dirOverride)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var liveMap map[cliPersonKey][]CLILiveSession
	if c.liveSessionsFn != nil {
		liveMap = c.liveSessionsFn()
	}

	seenPaths := make(map[string]bool)
	out := make([]CLIAccountUsage, 0, len(entries))
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
		if json.Unmarshal(raw, &prof) != nil {
			continue
		}
		if prof.Provider != "claude" && prof.Provider != "codex" && prof.Provider != "agy" {
			continue
		}
		if prof.ID == "" {
			prof.ID = entry.Name()
		}

		usage := CLIAccountUsage{
			ID:        prof.ID,
			CLI:       prof.Provider,
			Name:      prof.Name,
			Person:    prof.Person,
			Account:   prof.Account,
			Connected: hasAuthFile(profileDir, prof.Provider),
		}
		if prof.Person != "" && liveMap != nil {
			if sessions := liveMap[cliPersonKey{cli: prof.Provider, person: strings.ToLower(prof.Person)}]; len(sessions) > 0 {
				usage.LiveSessions = append([]CLILiveSession(nil), sessions...)
			}
		}
		configDir := filepath.Join(profileDir, "config")
		switch prof.Provider {
		case "claude":
			c.collectClaude(&usage, configDir, now, seenPaths)
		case "codex":
			c.collectCodex(&usage, configDir, now, seenPaths)
		case "agy":
			c.collectAgy(&usage, configDir, now, seenPaths)
		}
		out = append(out, usage)
	}

	// Evict deleted files from cache so long-running processes do not leak memory.
	for path := range c.fileCache {
		if !seenPaths[path] {
			delete(c.fileCache, path)
		}
	}

	cliOrder := map[string]int{"claude": 0, "codex": 1, "agy": 2}
	sort.Slice(out, func(i, j int) bool {
		if cliOrder[out[i].CLI] != cliOrder[out[j].CLI] {
			return cliOrder[out[i].CLI] < cliOrder[out[j].CLI]
		}
		pi, pj := strings.ToLower(out[i].Person), strings.ToLower(out[j].Person)
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func hasAuthFile(profileDir, provider string) bool {
	var file string
	switch provider {
	case "claude":
		file = filepath.Join(profileDir, "config", ".credentials.json")
	case "codex":
		file = filepath.Join(profileDir, "config", "auth.json")
	case "agy":
		file = filepath.Join(profileDir, "config", "antigravity-oauth-token")
	default:
		return false
	}
	info, err := os.Stat(file)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// --- Claude ------------------------------------------------------------------

func (c *cliAccountsCollector) collectClaude(u *CLIAccountUsage, configDir string, now time.Time, seen map[string]bool) {
	u.QuotaSource = "provider_cache"
	c.readClaudeConfig(u, filepath.Join(configDir, ".claude.json"), now)

	files, newest := findRecentFiles(filepath.Join(configDir, "projects"), ".jsonl", now.Add(-window7d-time.Hour))
	if newest != "" {
		files = appendUnique(files, newest)
	}
	var allEvents []tokenEvent
	var recentSessions []CLISessionActivity
	for _, f := range files {
		seen[f] = true
		parsed := c.loadCachedFile(f, parseClaudeJSONL)
		allEvents = append(allEvents, parsed.events...)
		if act, ok := buildRecentSession(parsed, "", now); ok {
			recentSessions = append(recentSessions, act)
		}
	}
	u.RecentSessions = topRecentSessions(recentSessions, 5)
	aggregateEvents(u, allEvents, now)
}

func (c *cliAccountsCollector) readClaudeConfig(u *CLIAccountUsage, path string, now time.Time) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	mod := info.ModTime().UnixNano()
	size := info.Size()
	nowUnix := now.Unix()

	apply := func(cached cachedClaudeConfig) {
		if u.Account == "" && cached.email != "" {
			u.Account = cached.email
		}
		if cached.plan != "" {
			u.Plan = cached.plan
		}
		if cached.quotaAt > 0 {
			u.QuotaAt = cached.quotaAt
		}
		if len(cached.windows) > 0 {
			wins := make([]QuotaWindow, len(cached.windows))
			for i, w := range cached.windows {
				w.Expired = w.ResetsAt > 0 && w.ResetsAt <= nowUnix
				wins[i] = w
			}
			u.Windows = wins
		}
		if cached.extra != nil {
			cp := *cached.extra
			u.Extra = &cp
		}
	}

	if cached, ok := c.claudeCfgCache[path]; ok && cached.modTime == mod && cached.size == size {
		apply(cached)
		return
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg struct {
		OAuthAccount *struct {
			EmailAddress     string `json:"emailAddress"`
			OrganizationType string `json:"organizationType"`
		} `json:"oauthAccount"`
		CachedUsageUtilization *struct {
			FetchedAtMs int64 `json:"fetchedAtMs"`
			Utilization *struct {
				FiveHour *struct {
					Utilization float64 `json:"utilization"`
					ResetsAt    string  `json:"resets_at"`
				} `json:"five_hour"`
				SevenDay *struct {
					Utilization float64 `json:"utilization"`
					ResetsAt    string  `json:"resets_at"`
				} `json:"seven_day"`
				ExtraUsage *struct {
					IsEnabled         bool    `json:"is_enabled"`
					MonthlyLimit      float64 `json:"monthly_limit"`
					UsedCredits       float64 `json:"used_credits"`
					Utilization       float64 `json:"utilization"`
					Currency          string  `json:"currency"`
					DecimalPlaces     int     `json:"decimal_places"`
					DisabledReason    string  `json:"disabled_reason"`
					SpendLimitReached bool    `json:"spend_limit_reached"`
				} `json:"extra_usage"`
				Limits []struct {
					Kind     string  `json:"kind"`
					Percent  float64 `json:"percent"`
					ResetsAt string  `json:"resets_at"`
					IsActive bool    `json:"is_active"`
					Scope    *struct {
						Model *struct {
							DisplayName string `json:"display_name"`
						} `json:"model"`
					} `json:"scope"`
				} `json:"limits"`
			} `json:"utilization"`
		} `json:"cachedUsageUtilization"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return
	}
	var parsed cachedClaudeConfig
	parsed.modTime = mod
	parsed.size = size
	if cfg.OAuthAccount != nil {
		parsed.email = cfg.OAuthAccount.EmailAddress
		parsed.plan = formatClaudePlan(cfg.OAuthAccount.OrganizationType)
	}
	if cu := cfg.CachedUsageUtilization; cu != nil && cu.Utilization != nil {
		if cu.FetchedAtMs > 0 {
			parsed.quotaAt = cu.FetchedAtMs / 1000
		}
		if w := cu.Utilization.FiveHour; w != nil {
			resets := parseTimestampUnix(w.ResetsAt)
			parsed.windows = append(parsed.windows, QuotaWindow{
				Label:       "5 h",
				UsedPercent: w.Utilization,
				WindowMin:   300,
				ResetsAt:    resets,
			})
		}
		if w := cu.Utilization.SevenDay; w != nil {
			resets := parseTimestampUnix(w.ResetsAt)
			parsed.windows = append(parsed.windows, QuotaWindow{
				Label:       "7 j",
				UsedPercent: w.Utilization,
				WindowMin:   10080,
				ResetsAt:    resets,
			})
		}
		for _, lim := range cu.Utilization.Limits {
			if lim.Kind != "weekly_scoped" || lim.Scope == nil || lim.Scope.Model == nil || lim.Scope.Model.DisplayName == "" {
				continue
			}
			if lim.Percent <= 0 && !lim.IsActive {
				continue
			}
			resets := parseTimestampUnix(lim.ResetsAt)
			parsed.windows = append(parsed.windows, QuotaWindow{
				Label:       lim.Scope.Model.DisplayName + " (7 j)",
				UsedPercent: lim.Percent,
				WindowMin:   10080,
				ResetsAt:    resets,
			})
		}
		if ex := cu.Utilization.ExtraUsage; ex != nil && (ex.IsEnabled || ex.MonthlyLimit > 0 || ex.UsedCredits > 0) {
			scale := math.Pow10(ex.DecimalPlaces)
			if scale <= 0 {
				scale = 1
			}
			parsed.extra = &ExtraUsage{
				Enabled:        ex.IsEnabled,
				Used:           ex.UsedCredits / scale,
				Limit:          ex.MonthlyLimit / scale,
				UsedPercent:    ex.Utilization,
				Currency:       ex.Currency,
				LimitReached:   ex.SpendLimitReached,
				DisabledReason: ex.DisabledReason,
			}
		}
	}
	c.claudeCfgCache[path] = parsed
	apply(parsed)
}

func formatClaudePlan(orgType string) string {
	switch strings.ToLower(strings.TrimSpace(orgType)) {
	case "claude_pro":
		return "Pro"
	case "claude_max":
		return "Max"
	case "claude_team":
		return "Team"
	case "claude_enterprise":
		return "Enterprise"
	case "":
		return ""
	default:
		return orgType
	}
}

func parseClaudeJSONL(path string) (cachedParsedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return cachedParsedFile{}, err
	}
	defer f.Close()

	type entry struct {
		key string
		ev  tokenEvent
	}
	var ordered []entry
	idxByKey := make(map[string]int)
	var sessionID, sessionCWD, sessionBranch string

	err = scanLines(f, func(line []byte) {
		if sessionCWD == "" && bytes.Contains(line, []byte(`"cwd"`)) {
			var meta struct {
				SessionID string `json:"sessionId"`
				CWD       string `json:"cwd"`
				GitBranch string `json:"gitBranch"`
			}
			if json.Unmarshal(line, &meta) == nil {
				if meta.SessionID != "" {
					sessionID = meta.SessionID
				}
				if meta.CWD != "" {
					sessionCWD = meta.CWD
				}
				if meta.GitBranch != "" {
					sessionBranch = meta.GitBranch
				}
			}
		}
		if !bytes.Contains(line, []byte(`"usage"`)) {
			return
		}
		var row struct {
			Timestamp string `json:"timestamp"`
			RequestID string `json:"requestId"`
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			GitBranch string `json:"gitBranch"`
			Message   *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *struct {
					InputTokens              int64 `json:"input_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil || row.Message == nil || row.Message.Usage == nil {
			return
		}
		if sessionID == "" && row.SessionID != "" {
			sessionID = row.SessionID
		}
		if sessionCWD == "" && row.CWD != "" {
			sessionCWD = row.CWD
		}
		if sessionBranch == "" && row.GitBranch != "" {
			sessionBranch = row.GitBranch
		}
		if row.Message.Model == "" || row.Message.Model == "<synthetic>" {
			return
		}
		ts := parseTimestampUnix(row.Timestamp)
		if ts == 0 {
			return
		}
		u := row.Message.Usage
		total := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.OutputTokens
		if total <= 0 {
			return
		}
		ev := tokenEvent{
			ts:    ts,
			model: row.Message.Model,
			tokens: TokenCounts{
				Input:      u.InputTokens,
				CacheRead:  u.CacheReadInputTokens,
				CacheWrite: u.CacheCreationInputTokens,
				Output:     u.OutputTokens,
				Total:      total,
				Requests:   1,
			},
		}
		key := ""
		if row.RequestID != "" || row.Message.ID != "" {
			key = row.RequestID + "\x00" + row.Message.ID
		}
		if key != "" {
			if idx, ok := idxByKey[key]; ok {
				ordered[idx].ev = ev
				return
			}
			idxByKey[key] = len(ordered)
		}
		ordered = append(ordered, entry{key: key, ev: ev})
	})
	if err != nil {
		return cachedParsedFile{}, err
	}

	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	events := make([]tokenEvent, len(ordered))
	for i, item := range ordered {
		events[i] = item.ev
	}
	return cachedParsedFile{
		events:         events,
		sessionID:      sessionID,
		sessionProject: sessionCWD,
		sessionBranch:  sessionBranch,
	}, nil
}

// --- Codex -------------------------------------------------------------------

func (c *cliAccountsCollector) collectCodex(u *CLIAccountUsage, configDir string, now time.Time, seen map[string]bool) {
	u.QuotaSource = "session_events"
	files, newest := findRecentFiles(filepath.Join(configDir, "sessions"), ".jsonl", now.Add(-window7d-time.Hour))
	if newest != "" {
		files = appendUnique(files, newest)
	}

	var allEvents []tokenEvent
	var recentSessions []CLISessionActivity
	var latestRL *codexRateLimitsSnapshot
	var latestTurn *TokenCounts
	var latestTurnTS int64
	var latestCtx int64

	for _, f := range files {
		seen[f] = true
		parsed := c.loadCachedFile(f, parseCodexJSONL)
		allEvents = append(allEvents, parsed.events...)
		if act, ok := buildRecentSession(parsed, "", now); ok {
			recentSessions = append(recentSessions, act)
		}
		if parsed.rateLimits != nil && (latestRL == nil || parsed.rateLimits.ts >= latestRL.ts) {
			latestRL = parsed.rateLimits
		}
		if parsed.lastTurn != nil && len(parsed.events) > 0 {
			fileLastTS := parsed.events[len(parsed.events)-1].ts
			if fileLastTS >= latestTurnTS {
				latestTurnTS = fileLastTS
				latestTurn = parsed.lastTurn
				latestCtx = parsed.contextWindow
			}
		}
	}

	u.RecentSessions = topRecentSessions(recentSessions, 5)
	aggregateEvents(u, allEvents, now)
	if latestTurn != nil {
		cp := *latestTurn
		u.LastTurn = &cp
	}
	u.ContextWindow = latestCtx

	if latestRL != nil {
		u.QuotaAt = latestRL.ts
		u.Plan = formatCodexPlan(latestRL.planType)
		nowUnix := now.Unix()
		for _, w := range []*codexWindowRaw{latestRL.primary, latestRL.secondary} {
			if w == nil {
				continue
			}
			u.Windows = append(u.Windows, QuotaWindow{
				Label:       formatWindowMinutes(w.WindowMinutes),
				UsedPercent: w.UsedPercent,
				WindowMin:   w.WindowMinutes,
				ResetsAt:    w.ResetsAt,
				Expired:     w.ResetsAt > 0 && w.ResetsAt <= nowUnix,
			})
		}
		if latestRL.credits != nil && (latestRL.credits.HasCredits || latestRL.credits.Unlimited) {
			u.Extra = &ExtraUsage{
				Enabled:   true,
				Balance:   latestRL.credits.Balance,
				Unlimited: latestRL.credits.Unlimited,
			}
		}
	}
}

func formatCodexPlan(plan string) string {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	case "free":
		return "Free"
	case "team":
		return "Team"
	case "enterprise":
		return "Enterprise"
	case "":
		return ""
	default:
		return plan
	}
}

func formatWindowMinutes(mins int) string {
	if mins <= 0 {
		return "Fenêtre"
	}
	if mins%(24*60) == 0 {
		return strconv.Itoa(mins/(24*60)) + " j"
	}
	if mins%60 == 0 {
		return strconv.Itoa(mins/60) + " h"
	}
	return strconv.Itoa(mins) + " min"
}

type codexUsageRaw struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func normalizeCodexDelta(cur, prev codexUsageRaw) TokenCounts {
	dInTotal := maxInt64(0, cur.InputTokens-prev.InputTokens)
	dCacheRead := maxInt64(0, cur.CachedInputTokens-prev.CachedInputTokens)
	dCacheWrite := maxInt64(0, cur.CacheWriteInputTokens-prev.CacheWriteInputTokens)
	dIn := maxInt64(0, dInTotal-dCacheRead)
	dOut := maxInt64(0, cur.OutputTokens-prev.OutputTokens)
	dReason := maxInt64(0, cur.ReasoningOutputTokens-prev.ReasoningOutputTokens)
	dTotal := maxInt64(0, cur.TotalTokens-prev.TotalTokens)
	if dTotal == 0 {
		dTotal = dIn + dCacheRead + dCacheWrite + dOut
	}
	return TokenCounts{
		Input:      dIn,
		CacheRead:  dCacheRead,
		CacheWrite: dCacheWrite,
		Output:     dOut,
		Reasoning:  dReason,
		Total:      dTotal,
		Requests:   1,
	}
}

func parseCodexJSONL(path string) (cachedParsedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return cachedParsedFile{}, err
	}
	defer f.Close()

	var out cachedParsedFile
	var currentModel string
	var prevTotal codexUsageRaw

	err = scanLines(f, func(line []byte) {
		isMeta := out.sessionID == "" && bytes.Contains(line, []byte(`"session_meta"`))
		isTurnCtx := bytes.Contains(line, []byte(`"turn_context"`))
		isTokenCount := bytes.Contains(line, []byte(`"token_count"`))
		if !isMeta && !isTurnCtx && !isTokenCount {
			return
		}
		var row struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			return
		}
		if row.Type == "session_meta" {
			var p struct {
				ID  string `json:"id"`
				CWD string `json:"cwd"`
				Git *struct {
					Branch string `json:"branch"`
				} `json:"git"`
			}
			if json.Unmarshal(row.Payload, &p) == nil {
				if p.ID != "" {
					out.sessionID = p.ID
				}
				if p.CWD != "" {
					out.sessionProject = p.CWD
				}
				if p.Git != nil && p.Git.Branch != "" {
					out.sessionBranch = p.Git.Branch
				}
			}
			return
		}
		if row.Type == "turn_context" {
			var p struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(row.Payload, &p) == nil && p.Model != "" {
				currentModel = p.Model
			}
			return
		}
		if row.Type != "event_msg" {
			return
		}
		var p struct {
			Type string `json:"type"`
			Info *struct {
				TotalTokenUsage   *codexUsageRaw `json:"total_token_usage"`
				LastTokenUsage    *codexUsageRaw `json:"last_token_usage"`
				ModelContextWin   int64          `json:"model_context_window"`
			} `json:"info"`
			RateLimits *struct {
				PlanType             string           `json:"plan_type"`
				RateLimitReachedType string           `json:"rate_limit_reached_type"`
				Primary              *codexWindowRaw  `json:"primary"`
				Secondary            *codexWindowRaw  `json:"secondary"`
				Credits              *codexCreditsRaw `json:"credits"`
			} `json:"rate_limits"`
		}
		if json.Unmarshal(row.Payload, &p) != nil || p.Type != "token_count" {
			return
		}
		ts := parseTimestampUnix(row.Timestamp)
		if p.RateLimits != nil && ts > 0 {
			if out.rateLimits == nil || ts >= out.rateLimits.ts {
				out.rateLimits = &codexRateLimitsSnapshot{
					ts:               ts,
					planType:         p.RateLimits.PlanType,
					limitReachedType: p.RateLimits.RateLimitReachedType,
					primary:          p.RateLimits.Primary,
					secondary:        p.RateLimits.Secondary,
					credits:          p.RateLimits.Credits,
				}
			}
		}
		if p.Info == nil || p.Info.TotalTokenUsage == nil || ts == 0 {
			return
		}
		if p.Info.ModelContextWin > 0 {
			out.contextWindow = p.Info.ModelContextWin
		}
		if p.Info.LastTokenUsage != nil {
			lt := normalizeCodexDelta(*p.Info.LastTokenUsage, codexUsageRaw{})
			out.lastTurn = &lt
		}
		cur := *p.Info.TotalTokenUsage
		if cur.TotalTokens > prevTotal.TotalTokens || cur.InputTokens > prevTotal.InputTokens || cur.OutputTokens > prevTotal.OutputTokens {
			delta := normalizeCodexDelta(cur, prevTotal)
			prevTotal = cur
			if delta.Total > 0 {
				out.events = append(out.events, tokenEvent{
					ts:     ts,
					model:  currentModel,
					tokens: delta,
				})
			}
		}
	})
	if err != nil {
		return cachedParsedFile{}, err
	}
	if out.sessionID == "" {
		out.sessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	return out, nil
}

// --- Antigravity (agy) -------------------------------------------------------

type agyConvSummary struct {
	title   string
	project string
}

func (c *cliAccountsCollector) readAgyConversationMeta(configDir string) map[string]agyConvSummary {
	path := filepath.Join(configDir, "cache", "conversation_metadata.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mod := info.ModTime().UnixNano()
	size := info.Size()
	if cached, ok := c.agyMetaCache[path]; ok && cached.modTime == mod && cached.size == size {
		return cached.items
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var data struct {
		Conversations map[string]struct {
			Summary struct {
				Title         string   `json:"Title"`
				Preview       string   `json:"Preview"`
				WorkspaceURIs []string `json:"WorkspaceURIs"`
			} `json:"summary"`
		} `json:"conversations"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	out := make(map[string]agyConvSummary, len(data.Conversations))
	for id, item := range data.Conversations {
		title := strings.TrimSpace(item.Summary.Title)
		if title == "" {
			title = strings.TrimSpace(item.Summary.Preview)
			if len(title) > 72 {
				title = title[:72] + "…"
			}
		}
		proj := ""
		if len(item.Summary.WorkspaceURIs) > 0 {
			proj = strings.TrimPrefix(item.Summary.WorkspaceURIs[0], "file://")
		}
		out[id] = agyConvSummary{title: title, project: proj}
	}
	c.agyMetaCache[path] = cachedAgyMeta{modTime: mod, size: size, items: out}
	return out
}

func (c *cliAccountsCollector) collectAgy(u *CLIAccountUsage, configDir string, now time.Time, seen map[string]bool) {
	u.QuotaSource = "local_logs_only"
	c.agyMu.RLock()
	cachedQuota, hasQuota := c.agyQuotas[u.ID]
	c.agyMu.RUnlock()
	if hasQuota && len(cachedQuota.windows) > 0 {
		u.QuotaSource = "agy_usage_cli"
		u.QuotaAt = cachedQuota.fetchedAt
		if cachedQuota.plan != "" {
			u.Plan = cachedQuota.plan
		}
		nowUnix := now.Unix()
		wins := make([]QuotaWindow, len(cachedQuota.windows))
		for i, w := range cachedQuota.windows {
			w.Expired = w.ResetsAt > 0 && w.ResetsAt <= nowUnix
			wins[i] = w
		}
		u.Windows = wins
	}

	if raw, err := os.ReadFile(filepath.Join(configDir, "settings.json")); err == nil {
		var settings struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(raw, &settings) == nil && settings.Model != "" {
			u.LastModel = settings.Model
		}
	}

	metaMap := c.readAgyConversationMeta(configDir)
	files, newest := findRecentFiles(filepath.Join(configDir, "brain"), "transcript.jsonl", now.Add(-window7d-time.Hour))
	if newest != "" {
		files = appendUnique(files, newest)
	}
	var allEvents []tokenEvent
	var recentSessions []CLISessionActivity
	for _, f := range files {
		// Only read the canonical .system_generated/logs/transcript.jsonl, not chunks.
		if filepath.Base(filepath.Dir(f)) != "logs" {
			continue
		}
		seen[f] = true
		parsed := c.loadCachedFile(f, parseAgyTranscript)
		allEvents = append(allEvents, parsed.events...)
		convID := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(f))))
		if parsed.sessionID == "" {
			parsed.sessionID = convID
		}
		title := ""
		if m, ok := metaMap[convID]; ok {
			title = m.title
			if parsed.sessionProject == "" {
				parsed.sessionProject = m.project
			}
		}
		if act, ok := buildRecentSession(parsed, title, now); ok {
			recentSessions = append(recentSessions, act)
		}
	}
	u.RecentSessions = topRecentSessions(recentSessions, 5)
	aggregateEvents(u, allEvents, now)
}

func parseAgyTranscript(path string) (cachedParsedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return cachedParsedFile{}, err
	}
	defer f.Close()

	type entry struct {
		ev tokenEvent
	}
	var ordered []entry
	idxByStep := make(map[int]int)

	err = scanLines(f, func(line []byte) {
		if !bytes.Contains(line, []byte(`"PLANNER_RESPONSE"`)) {
			return
		}
		var row struct {
			StepIndex       int    `json:"step_index"`
			Type            string `json:"type"`
			CreatedAt       string `json:"created_at"`
			Model           string `json:"model"`
			InputTokens     int64  `json:"input_tokens"`
			CacheReadTokens int64  `json:"cache_read_tokens"`
			OutputTokens    int64  `json:"output_tokens"`
		}
		if json.Unmarshal(line, &row) != nil || row.Type != "PLANNER_RESPONSE" {
			return
		}
		ts := parseTimestampUnix(row.CreatedAt)
		if ts == 0 {
			return
		}
		total := row.InputTokens + row.CacheReadTokens + row.OutputTokens
		if total <= 0 {
			return
		}
		ev := tokenEvent{
			ts:    ts,
			model: row.Model,
			tokens: TokenCounts{
				Input:     row.InputTokens,
				CacheRead: row.CacheReadTokens,
				Output:    row.OutputTokens,
				Total:     total,
				Requests:  1,
			},
		}
		if idx, ok := idxByStep[row.StepIndex]; ok {
			ordered[idx].ev = ev
			return
		}
		idxByStep[row.StepIndex] = len(ordered)
		ordered = append(ordered, entry{ev: ev})
	})
	if err != nil {
		return cachedParsedFile{}, err
	}
	events := make([]tokenEvent, len(ordered))
	for i, item := range ordered {
		events[i] = item.ev
	}
	return cachedParsedFile{events: events}, nil
}

// --- Shared helpers ----------------------------------------------------------

func (c *cliAccountsCollector) loadCachedFile(path string, parser func(string) (cachedParsedFile, error)) cachedParsedFile {
	info, err := os.Stat(path)
	if err != nil {
		return cachedParsedFile{}
	}
	mod := info.ModTime().UnixNano()
	size := info.Size()
	if cached, ok := c.fileCache[path]; ok && cached.modTime == mod && cached.size == size {
		return cached
	}
	parsed, err := parser(path)
	if err != nil {
		return cachedParsedFile{}
	}
	parsed.modTime = mod
	parsed.size = size
	c.fileCache[path] = parsed
	return parsed
}

func findRecentFiles(root, suffix string, minMod time.Time) (recent []string, newestPath string) {
	var newestTime time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if suffix != "" && !strings.HasSuffix(name, suffix) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		mt := info.ModTime()
		if mt.After(newestTime) {
			newestTime = mt
			newestPath = path
		}
		if !mt.Before(minMod) {
			recent = append(recent, path)
		}
		return nil
	})
	return recent, newestPath
}

func appendUnique(list []string, item string) []string {
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}

func aggregateEvents(u *CLIAccountUsage, events []tokenEvent, now time.Time) {
	nowUnix := now.Unix()
	cut5h := nowUnix - int64(window5h/time.Second)
	cut24h := nowUnix - int64(window24h/time.Second)
	cut7d := nowUnix - int64(window7d/time.Second)

	modelSet := make(map[string]bool)
	var latestTS int64
	var latestModel string
	var latestTurn *TokenCounts

	for _, ev := range events {
		if ev.ts >= cut5h {
			u.Tokens5h.add(ev.tokens)
		}
		if ev.ts >= cut24h {
			u.Tokens24h.add(ev.tokens)
		}
		if ev.ts >= cut7d {
			u.Tokens7d.add(ev.tokens)
			if ev.model != "" {
				modelSet[ev.model] = true
			}
		}
		if ev.ts >= latestTS {
			latestTS = ev.ts
			if ev.model != "" {
				latestModel = ev.model
			}
			t := ev.tokens
			latestTurn = &t
		}
	}

	if latestTS > 0 {
		u.LastActivity = latestTS
	}
	if latestModel != "" {
		u.LastModel = latestModel
	}
	if u.LastTurn == nil && latestTurn != nil {
		u.LastTurn = latestTurn
	}
	if len(modelSet) > 0 {
		models := make([]string, 0, len(modelSet))
		for m := range modelSet {
			models = append(models, m)
		}
		sort.Strings(models)
		u.Models7d = models
	}
}

func parseTimestampUnix(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix()
	}
	return 0
}

func scanLines(r io.Reader, fn func(line []byte)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			fn(trimmed)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func buildRecentSession(parsed cachedParsedFile, title string, now time.Time) (CLISessionActivity, bool) {
	if len(parsed.events) == 0 {
		return CLISessionActivity{}, false
	}
	var total int64
	var lastTS int64
	var lastModel string
	for _, ev := range parsed.events {
		total += ev.tokens.Total
		if ev.ts >= lastTS {
			lastTS = ev.ts
			if ev.model != "" {
				lastModel = ev.model
			}
		}
	}
	if lastTS == 0 {
		return CLISessionActivity{}, false
	}
	return CLISessionActivity{
		ID:           parsed.sessionID,
		Project:      parsed.sessionProject,
		Title:        title,
		Branch:       parsed.sessionBranch,
		Model:        lastModel,
		Tokens:       total,
		Requests:     len(parsed.events),
		LastActivity: lastTS,
		Active:       now.Unix()-lastTS <= 300,
	}, true
}

func topRecentSessions(items []CLISessionActivity, limit int) []CLISessionActivity {
	if len(items) == 0 {
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastActivity > items[j].LastActivity
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func collectLiveTmuxSessions() map[cliPersonKey][]CLILiveSession {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(
		ctx,
		"tmux",
		"list-panes",
		"-a",
		"-F",
		"#{session_name}\t#{session_attached}\t#{@ai-person-claude}\t#{@ai-person-codex}\t#{@ai-person-agy}\t#{pane_current_command}\t#{pane_current_path}",
	).Output()
	if err != nil {
		return nil
	}

	result := make(map[cliPersonKey][]CLILiveSession)
	seen := make(map[string]int) // key(cli|person|session) -> index in result[k]
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		session := parts[0]
		if session == "" || session == "tmuxatlas-ctrl" || strings.HasPrefix(session, "tmuxatlas-dictation-") {
			continue
		}
		attached := parts[1] != "0" && parts[1] != ""
		persons := map[string]string{
			"claude": strings.TrimSpace(parts[2]),
			"codex":  strings.TrimSpace(parts[3]),
			"agy":    strings.TrimSpace(parts[4]),
		}
		cmd := strings.TrimSpace(parts[5])
		path := strings.TrimSpace(parts[6])

		for cli, person := range persons {
			if person == "" {
				continue
			}
			k := cliPersonKey{cli: cli, person: strings.ToLower(person)}
			dedupKey := cli + "\x00" + k.person + "\x00" + session
			if idx, ok := seen[dedupKey]; ok {
				if cmd == cli || (cli == "agy" && cmd == "python3") {
					result[k][idx].Command = cmd
					if path != "" {
						result[k][idx].Path = path
					}
				}
				continue
			}
			seen[dedupKey] = len(result[k])
			result[k] = append(result[k], CLILiveSession{
				Session:  session,
				Command:  cmd,
				Path:     path,
				Attached: attached,
			})
		}
	}
	return result
}
