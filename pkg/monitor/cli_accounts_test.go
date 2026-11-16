package monitor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCollectCLIAccounts(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 16, 13, 30, 0, 0, time.UTC)

	// 1. Claude profile (active 5h limit + deduplicated JSONL usage)
	claudeDir := filepath.Join(root, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	mustMkdir(t, filepath.Join(claudeDir, "config", "projects", "proj"))
	writeTestFile(t, filepath.Join(claudeDir, "profile.json"), `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","provider":"claude","name":"Aris2 Claude","person":"Aris2"}`)
	writeTestFile(t, filepath.Join(claudeDir, "config", ".credentials.json"), `{"ok":true}`)
	writeTestFile(t, filepath.Join(claudeDir, "config", ".claude.json"), `{
		"oauthAccount": {"emailAddress": "aris2@example.com", "organizationType": "claude_pro"},
		"cachedUsageUtilization": {
			"fetchedAtMs": `+strconvInt64(now.Add(-5*time.Minute).UnixMilli())+`,
			"utilization": {
				"five_hour": {"utilization": 100, "resets_at": "`+now.Add(20*time.Minute).Format(time.RFC3339)+`"},
				"seven_day": {"utilization": 40, "resets_at": "`+now.Add(48*time.Hour).Format(time.RFC3339)+`"},
				"extra_usage": {"is_enabled": true, "monthly_limit": 4000, "used_credits": 464, "utilization": 11.6, "currency": "USD", "decimal_places": 2, "spend_limit_reached": false}
			}
		}
	}`)
	writeTestFile(t, filepath.Join(claudeDir, "config", "projects", "proj", "s1.jsonl"), ""+
		`{"timestamp":"`+now.Add(-2*time.Hour).Format(time.RFC3339)+`","sessionId":"s1","cwd":"/root/Project/VieCore","gitBranch":"main","requestId":"r1","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":50}}}`+"\n"+
		`{"timestamp":"`+now.Add(-2*time.Hour).Format(time.RFC3339)+`","sessionId":"s1","cwd":"/root/Project/VieCore","gitBranch":"main","requestId":"r1","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":150}}}`+"\n"+
		`{"timestamp":"`+now.Add(-10*time.Hour).Format(time.RFC3339)+`","requestId":"r2","message":{"id":"m2","model":"<synthetic>","usage":{"input_tokens":999,"output_tokens":999}}}`+"\n"+
		`{"timestamp":"`+now.Add(-3*24*time.Hour).Format(time.RFC3339)+`","requestId":"r3","message":{"id":"m3","model":"claude-haiku-5-5","usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":30}}}`+"\n")

	// 2. Codex profile (cumulative token_count deltas + duplicate event + rate_limits)
	codexDir := filepath.Join(root, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	mustMkdir(t, filepath.Join(codexDir, "config", "sessions", "2026", "11", "16"))
	writeTestFile(t, filepath.Join(codexDir, "profile.json"), `{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","provider":"codex","name":"Aris Codex","person":"Aris"}`)
	writeTestFile(t, filepath.Join(codexDir, "config", "auth.json"), `{"ok":true}`)
	writeTestFile(t, filepath.Join(codexDir, "config", "sessions", "2026", "11", "16", "rollout-1.jsonl"), ""+
		`{"timestamp":"`+now.Add(-1*time.Hour).Format(time.RFC3339)+`","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`+"\n"+
		`{"timestamp":"`+now.Add(-50*time.Minute).Format(time.RFC3339)+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":100,"total_tokens":1100},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":100,"total_tokens":1100},"model_context_window":475000},"rate_limits":{"plan_type":"plus","primary":{"used_percent":27,"window_minutes":300,"resets_at":`+strconvInt64(now.Add(2*time.Hour).Unix())+`},"secondary":{"used_percent":6,"window_minutes":10080,"resets_at":`+strconvInt64(now.Add(5*24*time.Hour).Unix())+`}}}}`+"\n"+
		`{"timestamp":"`+now.Add(-49*time.Minute).Format(time.RFC3339)+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":100,"total_tokens":1100},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":600,"output_tokens":100,"total_tokens":1100},"model_context_window":475000}}}`+"\n"+
		`{"timestamp":"`+now.Add(-10*time.Minute).Format(time.RFC3339)+`","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1500,"cached_input_tokens":900,"output_tokens":250,"total_tokens":1750},"last_token_usage":{"input_tokens":500,"cached_input_tokens":300,"output_tokens":150,"total_tokens":650},"model_context_window":475000},"rate_limits":{"plan_type":"plus","primary":{"used_percent":35,"window_minutes":300,"resets_at":`+strconvInt64(now.Add(2*time.Hour).Unix())+`},"secondary":{"used_percent":7,"window_minutes":10080,"resets_at":`+strconvInt64(now.Add(5*24*time.Hour).Unix())+`}}}}`+"\n")

	// 3. Agy profile (brain transcript + chunk ignored + step deduplication)
	agyDir := filepath.Join(root, "cccccccccccccccccccccccccccccccc")
	mustMkdir(t, filepath.Join(agyDir, "config", "brain", "conv1", ".system_generated", "logs", "chunks", "transcript"))
	writeTestFile(t, filepath.Join(agyDir, "profile.json"), `{"id":"cccccccccccccccccccccccccccccccc","provider":"agy","name":"Aris Agy","person":"Aris","account":"aris@example.com"}`)
	writeTestFile(t, filepath.Join(agyDir, "config", "antigravity-oauth-token"), `token`)
	writeTestFile(t, filepath.Join(agyDir, "config", "settings.json"), `{"model":"Gemini 4 Hydrogen (High)"}`)
	transcriptBody := "" +
		`{"step_index":1,"type":"USER_INPUT","created_at":"` + now.Add(-30*time.Minute).Format(time.RFC3339) + `"}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"` + now.Add(-20*time.Minute).Format(time.RFC3339) + `","model":"gemini-4-hydrogen-high","input_tokens":400,"cache_read_tokens":2000,"output_tokens":100}` + "\n" +
		`{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"` + now.Add(-20*time.Minute).Format(time.RFC3339) + `","model":"gemini-4-hydrogen-high","input_tokens":400,"cache_read_tokens":2000,"output_tokens":150}` + "\n"
	writeTestFile(t, filepath.Join(agyDir, "config", "brain", "conv1", ".system_generated", "logs", "transcript.jsonl"), transcriptBody)
	writeTestFile(t, filepath.Join(agyDir, "config", "brain", "conv1", ".system_generated", "logs", "chunks", "transcript", "0000.jsonl"), transcriptBody)

	c := newCLIAccountsCollector(root)
	c.liveSessionsFn = func() map[cliPersonKey][]CLILiveSession {
		return map[cliPersonKey][]CLILiveSession{
			{cli: "claude", person: "aris2"}: {{Session: "fable_swift", Command: "claude", Path: "/root/Project/VieCore"}},
		}
	}
	got := c.collect(now)
	if len(got) != 3 {
		t.Fatalf("collect() len = %d, want 3", len(got))
	}

	// Claude assertions
	cl := got[0]
	if cl.CLI != "claude" || cl.Plan != "Pro" || cl.Account != "aris2@example.com" || !cl.Connected {
		t.Fatalf("unexpected claude metadata: %+v", cl)
	}
	if cl.Tokens5h.Total != 1260 || cl.Tokens5h.Requests != 1 || cl.Tokens5h.Output != 150 {
		t.Fatalf("unexpected claude 5h tokens (dedup failed?): %+v", cl.Tokens5h)
	}
	if cl.Tokens7d.Total != 1310 || len(cl.Models7d) != 2 {
		t.Fatalf("unexpected claude 7d tokens/models: %+v %v", cl.Tokens7d, cl.Models7d)
	}
	if len(cl.Windows) != 2 || cl.Windows[0].UsedPercent != 100 || cl.Windows[0].Expired {
		t.Fatalf("unexpected claude windows: %+v", cl.Windows)
	}
	if cl.Extra == nil || cl.Extra.Used != 4.64 || cl.Extra.Limit != 40 || cl.Extra.Currency != "USD" {
		t.Fatalf("unexpected claude extra usage: %+v", cl.Extra)
	}
	if len(cl.LiveSessions) != 1 || cl.LiveSessions[0].Session != "fable_swift" {
		t.Fatalf("unexpected claude live sessions: %+v", cl.LiveSessions)
	}
	if len(cl.RecentSessions) != 1 || cl.RecentSessions[0].Project != "/root/Project/VieCore" || cl.RecentSessions[0].Branch != "main" {
		t.Fatalf("unexpected claude recent sessions: %+v", cl.RecentSessions)
	}

	// Codex assertions
	cx := got[1]
	if cx.CLI != "codex" || cx.Plan != "Plus" || cx.LastModel != "gpt-6.1-sol" || cx.ContextWindow != 475000 {
		t.Fatalf("unexpected codex metadata: %+v", cx)
	}
	if cx.Tokens5h.Total != 1750 || cx.Tokens5h.Input != 600 || cx.Tokens5h.CacheRead != 900 || cx.Tokens5h.Output != 250 || cx.Tokens5h.Requests != 2 {
		t.Fatalf("unexpected codex 5h tokens: %+v", cx.Tokens5h)
	}
	if len(cx.Windows) != 2 || cx.Windows[0].Label != "5 h" || cx.Windows[0].UsedPercent != 35 || cx.Windows[1].Label != "7 j" || cx.Windows[1].UsedPercent != 7 {
		t.Fatalf("unexpected codex windows: %+v", cx.Windows)
	}

	// Agy assertions
	ag := got[2]
	if ag.CLI != "agy" || ag.QuotaSource != "local_logs_only" || ag.LastModel != "gemini-4-hydrogen-high" {
		t.Fatalf("unexpected agy metadata: %+v", ag)
	}
	if ag.Tokens5h.Total != 2550 || ag.Tokens5h.Requests != 1 || ag.Tokens5h.Output != 150 {
		t.Fatalf("unexpected agy 5h tokens: %+v", ag.Tokens5h)
	}

	// Second call hits cache cleanly
	got2 := c.collect(now)
	if len(got2) != 3 || got2[0].Tokens5h.Total != 1260 || got2[1].Tokens5h.Total != 1750 || got2[2].Tokens5h.Total != 2550 {
		t.Fatalf("cached collect mismatch: %+v", got2)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func strconvInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
