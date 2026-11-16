package monitor

import (
	"strings"
	"testing"
	"time"
)

func TestParseCodexAppServerQuota(t *testing.T) {
	now := time.Date(2026, 11, 16, 17, 25, 0, 0, time.UTC)
	reset5h := now.Add(15 * time.Minute).Unix()
	reset7d := now.Add(6 * 24 * time.Hour).Unix()

	rlRaw := []byte(`{
		"id": 2,
		"result": {
			"ordinaryUsageAllowed": false,
			"rateLimits": {
				"limitId": "codex",
				"primary": {"usedPercent": 100, "windowDurationMins": 300, "resetsAt": ` + strconvInt64(reset5h) + `},
				"secondary": {"usedPercent": 16, "windowDurationMins": 10080, "resetsAt": ` + strconvInt64(reset7d) + `},
				"credits": {"hasCredits": true, "unlimited": false, "balance": "12.50"},
				"planType": "plus",
				"rateLimitReachedType": "rate_limit_reached"
			}
		}
	}`)
	accRaw := []byte(`{
		"id": 3,
		"result": {
			"account": {
				"type": "chatgpt",
				"email": "camille@example.com",
				"planType": "plus"
			}
		}
	}`)

	res, errMsg := parseCodexAppServerQuota(rlRaw, accRaw, now)
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	if res.email != "camille@example.com" || res.plan != "Plus" || res.fetchedAt != now.Unix() {
		t.Fatalf("unexpected metadata: %+v", res)
	}
	if len(res.windows) != 2 {
		t.Fatalf("windows len = %d, want 2: %+v", len(res.windows), res.windows)
	}
	if res.windows[0].Label != "5 h" || res.windows[0].UsedPercent != 100 || res.windows[0].ResetsAt != reset5h || res.windows[0].Expired {
		t.Fatalf("unexpected win[0]: %+v", res.windows[0])
	}
	if res.windows[1].Label != "7 j" || res.windows[1].UsedPercent != 16 || res.windows[1].ResetsAt != reset7d {
		t.Fatalf("unexpected win[1]: %+v", res.windows[1])
	}
	if res.extra == nil || !res.extra.Enabled || res.extra.Balance != "12.50" {
		t.Fatalf("unexpected extra: %+v", res.extra)
	}
}

func TestShouldProbeCodex(t *testing.T) {
	now := time.Date(2026, 11, 16, 17, 30, 0, 0, time.UTC)
	windows := []QuotaWindow{
		{Label: "5 h", UsedPercent: 33, ResetsAt: now.Add(20 * time.Minute).Unix()},
		{Label: "7 j", UsedPercent: 5, ResetsAt: now.Add(6 * 24 * time.Hour).Unix()},
	}
	prev := codexQuotaResult{
		fetchedAt: now.Add(-2 * time.Minute).Unix(),
		windows:   windows,
	}

	// Always probe on startup when no live probe has run yet, even if JSONL has a timestamp.
	if !shouldProbeCodex(codexQuotaResult{}, false, now.Add(-30*time.Second).Unix(), windows, false, now.Add(-30*time.Second).Unix(), now) {
		t.Fatal("expected initial probe when hasPrev is false")
	}
	// Skip within 90s when no window expired.
	if shouldProbeCodex(codexQuotaResult{fetchedAt: now.Add(-45 * time.Second).Unix(), windows: windows}, true, now.Add(-45*time.Second).Unix(), windows, true, now.Unix(), now) {
		t.Fatal("expected skip within 90s min fresh age")
	}
	// Probe on 2m tick when live tmux session is bound (Novanns2(Camille) VieSocial case).
	if !shouldProbeCodex(prev, true, now.Add(-2*time.Hour).Unix(), windows, true, now.Add(-2*time.Hour).Unix(), now) {
		t.Fatal("expected probe on 2m cadence when live session is active")
	}
	// Skip idle within 4m when no live session and no recent activity.
	if shouldProbeCodex(prev, true, prev.fetchedAt, windows, false, now.Add(-2*time.Hour).Unix(), now) {
		t.Fatal("expected idle skip within 4m when no live session and no recent activity")
	}
	// Probe once idle age reaches 4m (captures remote usage on another device).
	stalePrev := prev
	stalePrev.fetchedAt = now.Add(-4 * time.Minute).Unix()
	if !shouldProbeCodex(stalePrev, true, stalePrev.fetchedAt, windows, false, now.Add(-2*time.Hour).Unix(), now) {
		t.Fatal("expected probe once idle age reaches 4m")
	}
}

func TestBuildCodexProbeEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("CODEX_API_KEY", "sk-codex")
	t.Setenv("CODEX_ACCESS_TOKEN", "tok")
	env := buildCodexProbeEnv("/tmp/codex-cfg")
	var hasHome bool
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENAI_API_KEY=") || strings.HasPrefix(kv, "CODEX_API_KEY=") || strings.HasPrefix(kv, "CODEX_ACCESS_TOKEN=") {
			t.Fatalf("expected conflicting env var stripped, found %s", kv)
		}
		if kv == "CODEX_HOME=/tmp/codex-cfg" {
			hasHome = true
		}
	}
	if !hasHome {
		t.Fatal("expected CODEX_HOME to be set")
	}
}
