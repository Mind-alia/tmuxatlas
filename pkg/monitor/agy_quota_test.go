package monitor

import (
	"testing"
	"time"
)

func TestParseAgyUsageScreen(t *testing.T) {
	now := time.Date(2026, 11, 16, 14, 25, 0, 0, time.UTC)
	sample := "\x1b[2J\x1b[1;1H" +
		"Antigravity CLI 1.5.9\n" +
		"alice@example.com (Google AI Pro)\n" +
		"└ Models & Quota\n" +
		"GEMINI MODELS\n" +
		"Models within this group: Gemini 4 Hydrogen, Gemini 3.8 Flash\n" +
		"Weekly Limit Remaining\n" +
		"[████████████████████████████████████████████░░░░░░] 88.97%\n" +
		"Refreshes in 165h 15m\n" +
		"Five Hour Limit Remaining\n" +
		"[███████████████████████████████░░░░░░░░░░░░░░░░░░░] 61.40%\n" +
		"Refreshes in 2h 15m\n" +
		"CLAUDE AND GPT MODELS\n" +
		"Models within this group: Claude Opus 5.5, Claude Sonnet 5.5, GPT-OSS 120B\n" +
		"Weekly Limit Remaining\n" +
		"[█████████████████████████░░░░░░░░░░░░░░░░░░░░░░░░░] 49.96%\n" +
		"Refreshes in 165h 8m\n" +
		"Five Hour Limit Remaining\n" +
		"[░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░] 0.00%\n" +
		"Refreshes in 2h 8m\n" +
		"GEMINI 4 ARGON\n" +
		"Gemini 4 Argon Weekly Limit\n" +
		"[██████████████████████████████████████████████████] 100.00%\n" +
		"Quota available\n"

	res := parseAgyUsageScreen(sample, now)
	if res.plan != "Pro" {
		t.Fatalf("plan = %q, want Pro", res.plan)
	}
	if len(res.windows) != 5 {
		t.Fatalf("windows len = %d, want 5: %+v", len(res.windows), res.windows)
	}

	// Order: Gemini 5 h, Gemini 7 j, Claude/GPT 5 h, Claude/GPT 7 j, Argon 7 j
	if res.windows[0].Label != "Gemini 5 h" || res.windows[0].UsedPercent != 38.6 {
		t.Fatalf("win[0] = %+v, want Gemini 5 h at 38.6%%", res.windows[0])
	}
	wantReset := now.Add(2*time.Hour + 15*time.Minute).Unix()
	if res.windows[0].ResetsAt != wantReset {
		t.Fatalf("win[0].ResetsAt = %d, want %d", res.windows[0].ResetsAt, wantReset)
	}
	if res.windows[1].Label != "Gemini 7 j" || res.windows[1].UsedPercent != 11.03 {
		t.Fatalf("win[1] = %+v, want Gemini 7 j at 11.03%%", res.windows[1])
	}
	if res.windows[2].Label != "Claude/GPT 5 h" || res.windows[2].UsedPercent != 100 {
		t.Fatalf("win[2] = %+v, want Claude/GPT 5 h at 100%%", res.windows[2])
	}
	if res.windows[3].Label != "Claude/GPT 7 j" || res.windows[3].UsedPercent != 50.04 {
		t.Fatalf("win[3] = %+v, want Claude/GPT 7 j at 50.04%%", res.windows[3])
	}
	if res.windows[4].Label != "Argon 7 j" || res.windows[4].UsedPercent != 0 {
		t.Fatalf("win[4] = %+v, want Argon 7 j at 0%%", res.windows[4])
	}
}

func TestShouldProbeAgy(t *testing.T) {
	now := time.Date(2026, 11, 16, 14, 45, 0, 0, time.UTC)
	prev := agyQuotaResult{
		fetchedAt: now.Add(-2 * time.Minute).Unix(),
		windows: []QuotaWindow{
			{Label: "Gemini 5 h", UsedPercent: 25, ResetsAt: now.Add(2 * time.Hour).Unix()},
		},
	}

	if !shouldProbeAgy(agyQuotaResult{}, false, false, 0, now) {
		t.Fatal("expected probe when no previous result")
	}
	if shouldProbeAgy(prev, true, false, now.Add(-1*time.Hour).Unix(), now) {
		t.Fatal("expected idle skip within 10m when no activity, no live session, and no reset")
	}
	if !shouldProbeAgy(prev, true, true, 0, now) {
		t.Fatal("expected probe when live tmux session is active")
	}
	if !shouldProbeAgy(prev, true, false, now.Add(-1*time.Minute).Unix(), now) {
		t.Fatal("expected probe when recent activity occurred since last probe")
	}

	resetPrev := prev
	resetPrev.windows = []QuotaWindow{
		{Label: "Gemini 5 h", UsedPercent: 40, ResetsAt: now.Add(-10 * time.Second).Unix()},
	}
	if !shouldProbeAgy(resetPrev, true, false, 0, now) {
		t.Fatal("expected probe when a non-zero quota window just reset")
	}

	stalePrev := prev
	stalePrev.fetchedAt = now.Add(-10 * time.Minute).Unix()
	if !shouldProbeAgy(stalePrev, true, false, 0, now) {
		t.Fatal("expected probe once idle age reaches 10m")
	}
}
