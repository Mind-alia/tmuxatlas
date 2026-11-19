package agentteams

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const sid = "11111111-2222-3333-4444-555555555555"

func fixture(t *testing.T) (*Reader, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sessions", "100.json"),
		`{"pid":100,"sessionId":"`+sid+`","cwd":"/work","name":"Revue","status":"busy","tmux":"ma:session:@3.%4"}`)
	write(t, filepath.Join(dir, "sessions", "200.json"),
		`{"pid":200,"sessionId":"99999999-2222-3333-4444-555555555555","cwd":"/work","name":"Morte","status":"idle"}`)
	write(t, filepath.Join(dir, "sessions", "100.abc.key"), `secret`)
	reader := NewReader(dir)
	reader.alive = func(pid int) bool { return pid == 100 }
	return reader, filepath.Join(dir, "projects", "-work", sid, "subagents")
}

func line(kind, at, content string) string {
	return `{"type":"` + kind + `","timestamp":"` + at + `","message":{"role":"x","content":` + content + `}}` + "\n"
}

func TestTeamsListsLiveSessionsAndTheirAgents(t *testing.T) {
	reader, sub := fixture(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	reader.now = func() time.Time { return now }

	write(t, filepath.Join(sub, "agent-aaa.meta.json"), `{"agentType":"general-purpose","description":"Tests Go","pickedEffortModel":"m"}`)
	write(t, filepath.Join(sub, "agent-aaa.jsonl"),
		line("user", "2026-11-19T11:50:00Z", `"go"`)+
			line("assistant", "2026-11-19T11:51:00Z", `[{"type":"text","text":"ok"},{"type":"tool_use","name":"Read","input":{"file_path":"/home/u/src/repo/pkg/auth/accounts.go"}}]`)+
			line("assistant", "2026-11-19T11:52:00Z", `[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...\nsecond line"}}]`))
	write(t, filepath.Join(sub, "agent-bbb.meta.json"), `{"agentType":"Explore","description":"Relecture","finished":true,"parentAgentId":"aaa"}`)
	write(t, filepath.Join(sub, "agent-bbb.jsonl"), line("user", "2026-11-19T11:00:00Z", `"x"`))
	write(t, filepath.Join(sub, "agent-old.meta.json"), `{"description":"Ancien","finished":true}`)
	write(t, filepath.Join(sub, "agent-old.jsonl"), line("user", "2026-11-17T11:00:00Z", `"x"`))
	for name, at := range map[string]time.Time{"aaa": now.Add(-time.Minute), "bbb": now.Add(-time.Hour), "old": now.Add(-48 * time.Hour)} {
		if err := os.Chtimes(filepath.Join(sub, "agent-"+name+".jsonl"), at, at); err != nil {
			t.Fatal(err)
		}
	}

	teams, err := reader.Teams()
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 1 {
		t.Fatalf("teams = %d, want the single live session", len(teams))
	}
	team := teams[0]
	if team.TmuxSession != "ma:session" || team.Name != "Revue" || team.Status != "busy" {
		t.Fatalf("team = %+v", team)
	}
	if len(team.Agents) != 2 {
		t.Fatalf("agents = %+v, want 2 (the 48 h old one is dropped)", team.Agents)
	}
	running, done := team.Agents[0], team.Agents[1]
	if running.ID != "aaa" || running.State != StateRunning || running.Steps != 2 || running.Model != "m" {
		t.Fatalf("running = %+v", running)
	}
	if running.Action != "Bash go test ./..." || len(running.Log) != 1 || running.Log[0] != "Read pkg/auth/accounts.go" {
		t.Fatalf("action = %q, log = %q", running.Action, running.Log)
	}
	if !running.StartedAt.Equal(time.Date(2026, 11, 19, 11, 50, 0, 0, time.UTC)) {
		t.Fatalf("started = %v", running.StartedAt)
	}
	if done.ID != "bbb" || done.State != StateDone || done.Parent != "aaa" {
		t.Fatalf("done = %+v", done)
	}
}

func TestSilentUnfinishedAgentTurnsQuietThenStopped(t *testing.T) {
	reader, sub := fixture(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	reader.now = func() time.Time { return now }
	write(t, filepath.Join(sub, "agent-q.meta.json"), `{"description":"q"}`)
	write(t, filepath.Join(sub, "agent-q.jsonl"), line("user", "2026-11-19T11:00:00Z", `"x"`))
	for age, want := range map[time.Duration]string{10 * time.Minute: StateQuiet, 2 * time.Hour: StateStopped} {
		at := now.Add(-age)
		if err := os.Chtimes(filepath.Join(sub, "agent-q.jsonl"), at, at); err != nil {
			t.Fatal(err)
		}
		teams, _ := reader.Teams()
		if got := teams[0].Agents[0].State; got != want {
			t.Fatalf("after %v: state = %q, want %q", age, got, want)
		}
	}
}

func TestTranscriptIsReadIncrementallyAndIgnoresAHalfWrittenLine(t *testing.T) {
	reader, sub := fixture(t)
	path := filepath.Join(sub, "agent-i.jsonl")
	write(t, filepath.Join(sub, "agent-i.meta.json"), `{"description":"i"}`)
	first := line("assistant", "2026-11-19T11:00:00Z", `[{"type":"tool_use","name":"Grep","input":{"pattern":"TODO"}}]`)
	half := `{"type":"assistant","timestamp":"2026-11-19T11:01:00Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]`
	write(t, path, first+half)
	teams, _ := reader.Teams()
	if a := teams[0].Agents[0]; a.Steps != 1 || a.Action != "Grep TODO" {
		t.Fatalf("with a half-written line: %+v", a)
	}
	write(t, path, first+half+"}}\n"+line("assistant", "2026-11-19T11:02:00Z", `[{"type":"tool_use","name":"X","input":{"command":"`+strings.Repeat("é", 300)+`"}}]`))
	teams, _ = reader.Teams()
	a := teams[0].Agents[0]
	if a.Steps != 3 || len(a.Log) != 2 || a.Log[1] != "Bash ls" {
		t.Fatalf("after the line was completed: %+v", a)
	}
	if n := len([]rune(a.Action)); n != maxActionLen || !strings.HasSuffix(a.Action, "…") {
		t.Fatalf("long action not clipped: %d runes", n)
	}
}

func TestMissingClaudeDirectoryGivesNoTeams(t *testing.T) {
	teams, err := NewReader(filepath.Join(t.TempDir(), "absent")).Teams()
	if err != nil || len(teams) != 0 {
		t.Fatalf("teams = %v, err = %v", teams, err)
	}
}
