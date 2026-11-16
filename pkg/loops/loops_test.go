package loops

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/accounts"
	"github.com/LosFurina/tmuxatlas/pkg/auth"
)

func writeFakeProfile(t *testing.T, accountsDir, id, provider, name, person string, connected ...bool) {
	t.Helper()
	dir := filepath.Join(accountsDir, id)
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Profile{
		ID:       id,
		Provider: provider,
		Name:     name,
		Person:   person,
	})
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	isConn := len(connected) == 0 || connected[0]
	if isConn {
		authName := map[string]string{
			"claude": ".credentials.json",
			"codex":  "auth.json",
			"agy":    "antigravity-oauth-token",
		}[provider]
		if authName != "" {
			if err := os.WriteFile(filepath.Join(dir, "config", authName), []byte(`{"ok":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestLoadAccountsIsolatesPerCLI(t *testing.T) {
	dir := t.TempDir()
	writeFakeProfile(t, dir, "11111111111111111111111111111111", "claude", "ArisClaude", "Aris")
	writeFakeProfile(t, dir, "22222222222222222222222222222222", "claude", "Aris2Claude", "Aris2")
	writeFakeProfile(t, dir, "33333333333333333333333333333333", "codex", "ArisCodex", "Aris")
	// Unconnected codex profile must be ignored so Aris2 never shows up under codex.
	writeFakeProfile(t, dir, "55555555555555555555555555555555", "codex", "Aris2Codex", "Aris2", false)
	writeFakeProfile(t, dir, "44444444444444444444444444444444", "agy", "ArisAgy", "Aris")

	got := LoadAccounts(dir)
	if len(got["claude"]) != 2 {
		t.Fatalf("claude profiles = %d, want 2", len(got["claude"]))
	}
	if len(got["codex"]) != 1 || got["codex"][0].Person != "Aris" {
		t.Fatalf("codex profiles = %+v, want only Aris", got["codex"])
	}
	if len(got["agy"]) != 1 || got["agy"][0].Person != "Aris" {
		t.Fatalf("agy profiles = %+v, want only Aris", got["agy"])
	}
}

func TestDiscoverProjectsFindsNestedRepos(t *testing.T) {
	root := t.TempDir()
	directGit := filepath.Join(root, "atlas")
	nestedGroup := filepath.Join(root, "Aris")
	nestedGit := filepath.Join(nestedGroup, "StatQuest")
	plainDir := filepath.Join(root, "notes")
	for _, p := range []string{filepath.Join(directGit, ".git"), filepath.Join(nestedGit, ".git"), plainDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projects := DiscoverProjects(root)
	names := map[string]bool{}
	for _, p := range projects {
		names[p.Name] = true
	}
	if !names["atlas"] || !names["Aris/StatQuest"] || !names["notes"] {
		t.Fatalf("unexpected discovered projects: %+v", projects)
	}
}

func TestBuildInteractiveCommand(t *testing.T) {
	got := buildInteractiveCommand("claude", "Aris2", "sonnet", "vérifie les tests")
	if got != "AI_PERSON='Aris2' claude --model 'sonnet' 'vérifie les tests'" {
		t.Fatalf("unexpected claude command: %s", got)
	}
	gotAgy := buildInteractiveCommand("agy", "Aris", "", "hello 'world'")
	if gotAgy != `AI_PERSON='Aris' agy -i 'hello '\''world'\'''` {
		t.Fatalf("unexpected agy command: %s", gotAgy)
	}
}

func TestManagerRunsLoopPersistsAndStopsOnDone(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	accountsDir := t.TempDir()
	writeFakeProfile(t, accountsDir, "11111111111111111111111111111111", "claude", "Aris2Claude", "Aris2")

	binDir := t.TempDir()
	counterFile := filepath.Join(t.TempDir(), "count")
	fakeClaude := filepath.Join(binDir, "claude")
	script := `#!/bin/sh
echo "AI_PERSON=$AI_PERSON"
echo "TMUX_SET=${TMUX:-no}"
count=0
if [ -f "` + counterFile + `" ]; then
  count=$(cat "` + counterFile + `")
fi
count=$((count + 1))
echo "$count" > "` + counterFile + `"
echo "iteration $count ran"
if [ "$count" -ge 2 ]; then
  echo "LOOP_DONE"
fi
`
	if err := os.WriteFile(fakeClaude, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX", "/tmp/tmux-test,1,0")

	mgr, err := NewManager(dataDir, nil, Options{AccountsDir: accountsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()

	// Reject Aris2 on agy when Aris2 only has claude.
	if _, err := mgr.Create(CreateRequest{
		Name:       "bad-account",
		ProjectDir: projectDir,
		CLI:        "agy",
		Person:     "Aris2",
		Prompt:     "test",
	}); err == nil {
		t.Fatal("expected error when selecting Aris2 for agy")
	}

	loop, err := mgr.Create(CreateRequest{
		Name:          "improve-app",
		ProjectDir:    projectDir,
		CLI:           "claude",
		Person:        "Aris2",
		Prompt:        "Améliore les tests",
		MaxIterations: 5,
		DelaySeconds:  0,
		StartNow:      true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var current *Loop
	for time.Now().Before(deadline) {
		current, err = mgr.Get(loop.ID, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != StatusRunning {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if current == nil || current.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %+v", current)
	}
	if current.CurrentIteration != 2 || len(current.Iterations) != 2 {
		t.Fatalf("expected 2 iterations before LOOP_DONE, got %d (%d records)", current.CurrentIteration, len(current.Iterations))
	}
	logResp, err := mgr.ReadLog(loop.ID, 1, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logResp.Content, "AI_PERSON=Aris2") || !strings.Contains(logResp.Content, "TMUX_SET=no") {
		t.Fatalf("unexpected log content: %s", logResp.Content)
	}

	// Reload manager from disk and confirm persistence.
	reloaded, err := NewManager(dataDir, nil, Options{AccountsDir: accountsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Shutdown()
	got, err := reloaded.Get(loop.ID, "", true)
	if err != nil || got.Status != StatusCompleted || len(got.Iterations) != 2 {
		t.Fatalf("persisted reload mismatch: %+v, err=%v", got, err)
	}
}

func TestManagerWorktreeIsolationAndCleanup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "init")

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\necho 'LOOP_DONE'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr, err := NewManager(t.TempDir(), nil, Options{AccountsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()

	loop, err := mgr.Create(CreateRequest{
		Name:          "wt-test",
		ProjectDir:    repo,
		CLI:           "codex",
		Prompt:        "improve",
		MaxIterations: 1,
		UseWorktree:   true,
		StartNow:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l, _ := mgr.Get(loop.ID, "", true)
		if l != nil && l.Status != StatusRunning {
			loop = l
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if loop.WorkDir == repo || !strings.HasPrefix(loop.Branch, "loop/") {
		t.Fatalf("expected dedicated worktree and loop branch, got workdir=%q branch=%q", loop.WorkDir, loop.Branch)
	}
	if err := mgr.Delete(loop.ID, "", true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(loop.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("expected worktree %s to be removed", loop.WorkDir)
	}
}

func TestHandlerOwnershipIsolation(t *testing.T) {
	mgr, err := NewManager(t.TempDir(), nil, Options{AccountsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()
	h := NewHandler(mgr)
	project := t.TempDir()

	aliceCtx := auth.WithAccount(httptest.NewRequest(http.MethodPost, "/api/loops/create", nil).Context(), auth.AccountIdentity{
		User: accounts.User{ID: "u-alice", Username: "alice", Role: "user"},
	})
	bobCtx := auth.WithAccount(httptest.NewRequest(http.MethodGet, "/api/loops", nil).Context(), auth.AccountIdentity{
		User: accounts.User{ID: "u-bob", Username: "bob", Role: "user"},
	})
	adminCtx := auth.WithAccount(httptest.NewRequest(http.MethodGet, "/api/loops", nil).Context(), auth.AccountIdentity{
		User: accounts.User{ID: "u-admin", Username: "admin", Role: "admin"},
	})

	body := bytes.NewBufferString(`{"name":"alice-loop","project_dir":` + mustJSON(project) + `,"cli":"claude","prompt":"fix"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/loops/create", body).WithContext(aliceCtx)
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var created Loop
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Bob cannot see or delete Alice's loop.
	listReq := httptest.NewRequest(http.MethodGet, "/api/loops", nil).WithContext(bobCtx)
	listRec := httptest.NewRecorder()
	h.List(listRec, listReq)
	if strings.Contains(listRec.Body.String(), created.ID) {
		t.Fatalf("bob must not see alice's loop: %s", listRec.Body.String())
	}
	delReq := httptest.NewRequest(http.MethodPost, "/api/loops/delete", bytes.NewBufferString(`{"id":`+mustJSON(created.ID)+`}`)).WithContext(bobCtx)
	delRec := httptest.NewRecorder()
	h.Delete(delRec, delReq)
	if delRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for bob delete, got %d", delRec.Code)
	}

	// Admin sees Alice's loop.
	adminReq := httptest.NewRequest(http.MethodGet, "/api/loops", nil).WithContext(adminCtx)
	adminRec := httptest.NewRecorder()
	h.List(adminRec, adminReq)
	if !strings.Contains(adminRec.Body.String(), created.ID) {
		t.Fatalf("admin should see alice's loop: %s", adminRec.Body.String())
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDetectRateLimit(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, berlin)

	ok, resetAt, _ := detectRateLimit("  ⎿  You've hit your 5-hour limit · resets 12:20pm (Europe/Berlin)", 1, 0, false, now)
	if !ok {
		t.Fatal("expected Claude 5-hour limit to be detected")
	}
	want := time.Date(2026, 11, 16, 12, 20, 0, 0, berlin)
	if !resetAt.Equal(want) {
		t.Fatalf("resetAt = %v, want %v", resetAt, want)
	}

	ok, resetAt, _ = detectRateLimit("You've hit your usage limit. Try again in 2h 15m.", 1, 0, false, now)
	if !ok || !resetAt.Equal(now.Add(2*time.Hour+15*time.Minute)) {
		t.Fatalf("unexpected codex relative reset: ok=%v resetAt=%v", ok, resetAt)
	}

	ok, resetAt, _ = detectRateLimit("Error: RESOURCE_EXHAUSTED: Quota exceeded. Resets in 45 minutes.", 1, 0, false, now)
	if !ok || !resetAt.Equal(now.Add(45*time.Minute)) {
		t.Fatalf("unexpected agy reset: ok=%v resetAt=%v", ok, resetAt)
	}

	// Exit 0 normal output mentioning "rate limit" must NOT trigger rate limit.
	ok, _, _ = detectRateLimit("Added rate limit middleware to router.go and ran tests.", 0, 1, false, now)
	if ok {
		t.Fatal("normal exit-0 output must not false-trigger rate limit")
	}
}

func TestMultiWorkerFailoverAndContinuousMode(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	accountsDir := t.TempDir()
	writeFakeProfile(t, accountsDir, "11111111111111111111111111111111", "claude", "ArisClaude", "Aris")
	writeFakeProfile(t, accountsDir, "22222222222222222222222222222222", "claude", "Aris2Claude", "Aris2")
	writeFakeProfile(t, accountsDir, "33333333333333333333333333333333", "codex", "ArisCodex", "Aris")

	binDir := t.TempDir()
	counterFile := filepath.Join(t.TempDir(), "codex_count")
	// claude hits 5h limit for Aris and Aris2; codex works continuously for 7 passes then signals LOOP_DONE.
	claudeScript := `#!/bin/sh
echo "You've hit your 5-hour limit · resets in 2h"
exit 1
`
	codexScript := `#!/bin/sh
count=0
if [ -f "` + counterFile + `" ]; then
  count=$(cat "` + counterFile + `")
fi
count=$((count + 1))
echo "$count" > "` + counterFile + `"
echo "codex pass $count"
if [ "$count" -ge 7 ]; then
  echo "LOOP_DONE"
fi
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(claudeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(codexScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr, err := NewManager(dataDir, nil, Options{AccountsDir: accountsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()

	continuous := true
	stopDone := true
	loop, err := mgr.Create(CreateRequest{
		Name:         "continuous-relay",
		ProjectDir:   projectDir,
		Prompt:       "Continue improving",
		Continuous:   &continuous,
		StopWhenDone: &stopDone,
		DelaySeconds: 0,
		Workers: []WorkerInput{
			{CLI: "claude", Person: "Aris", Enabled: true},
			{CLI: "claude", Person: "Aris2", Enabled: true},
			{CLI: "codex", Person: "Aris", Enabled: true},
		},
		StartNow: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var current *Loop
	for time.Now().Before(deadline) {
		current, err = mgr.Get(loop.ID, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != StatusRunning && current.Status != StatusWaitingReset {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if current == nil || current.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %+v", current)
	}
	if current.CurrentIteration != 7 {
		t.Fatalf("expected 7 continuous work passes, got %d", current.CurrentIteration)
	}
	// 2 rate-limited iterations + 7 completed codex iterations = 9 records.
	if len(current.Iterations) != 9 {
		t.Fatalf("expected 9 iteration records, got %d", len(current.Iterations))
	}
	if current.Workers[0].Status != WorkerRateLimited || current.Workers[1].Status != WorkerRateLimited {
		t.Fatalf("expected both claude workers rate_limited, got %+v", current.Workers)
	}
	if current.Workers[2].RunCount != 7 {
		t.Fatalf("expected codex run_count=7, got %d", current.Workers[2].RunCount)
	}
}

func TestAllWorkersRateLimitedWaitsAndWakesOnResume(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	accountsDir := t.TempDir()
	writeFakeProfile(t, accountsDir, "11111111111111111111111111111111", "claude", "ArisClaude", "Aris")
	writeFakeProfile(t, accountsDir, "33333333333333333333333333333333", "codex", "ArisCodex", "Aris")

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\necho \"You've hit your 5-hour limit · resets in 1h\"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\necho \"codex ok\"\necho \"LOOP_DONE\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr, err := NewManager(dataDir, nil, Options{AccountsDir: accountsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()

	stopDone := true
	loop, err := mgr.Create(CreateRequest{
		Name:         "wait-and-wake",
		ProjectDir:   projectDir,
		Prompt:       "Keep going",
		StopWhenDone: &stopDone,
		Workers: []WorkerInput{
			{CLI: "claude", Person: "Aris", Enabled: true},
			{CLI: "codex", Person: "Aris", Enabled: true, Paused: true},
		},
		StartNow: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	var waiting *Loop
	for time.Now().Before(deadline) {
		waiting, _ = mgr.Get(loop.ID, "", true)
		if waiting != nil && waiting.Status == StatusWaitingReset {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if waiting == nil || waiting.Status != StatusWaitingReset || waiting.NextResetAt == nil {
		t.Fatalf("expected waiting_reset with next_reset_at, got %+v", waiting)
	}

	// Reactivate paused codex worker -> loop must wake immediately and complete!
	if _, err := mgr.WorkerAction(WorkerActionRequest{
		LoopID:   loop.ID,
		WorkerID: "codex:Aris",
		Action:   "resume",
	}, "", true); err != nil {
		t.Fatalf("WorkerAction resume: %v", err)
	}

	deadline = time.Now().Add(4 * time.Second)
	var done *Loop
	for time.Now().Before(deadline) {
		done, _ = mgr.Get(loop.ID, "", true)
		if done != nil && done.Status == StatusCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if done == nil || done.Status != StatusCompleted {
		t.Fatalf("expected loop to wake and complete, got %+v", done)
	}
}

func TestWorkerEnableAllAndAutoPauseResume(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	accountsDir := t.TempDir()
	writeFakeProfile(t, accountsDir, "11111111111111111111111111111111", "claude", "ArisClaude", "Aris")
	writeFakeProfile(t, accountsDir, "22222222222222222222222222222222", "claude", "Aris2Claude", "Aris2")
	writeFakeProfile(t, accountsDir, "33333333333333333333333333333333", "codex", "ArisCodex", "Aris")

	binDir := t.TempDir()
	for _, cli := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(binDir, cli), []byte("#!/bin/sh\nsleep 0.05\necho 'ok'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr, err := NewManager(dataDir, nil, Options{AccountsDir: accountsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Shutdown()

	loop, err := mgr.Create(CreateRequest{
		Name:       "pool-test",
		ProjectDir: projectDir,
		CLI:        "claude",
		Person:     "Aris",
		Prompt:     "Improve",
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := mgr.WorkerAction(WorkerActionRequest{
		LoopID: loop.ID,
		Action: "enable_all",
	}, "", true)
	if err != nil {
		t.Fatalf("enable_all: %v", err)
	}
	if len(updated.Workers) != 3 {
		t.Fatalf("expected 3 enabled subscriptions, got %d (%+v)", len(updated.Workers), updated.Workers)
	}

	if _, err := mgr.Start(loop.ID, "", true); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Pause all workers while running -> loop should auto-pause cleanly after current pass.
	if _, err := mgr.WorkerAction(WorkerActionRequest{
		LoopID: loop.ID,
		Action: "pause_all",
	}, "", true); err != nil {
		t.Fatalf("pause_all: %v", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		cur, _ := mgr.Get(loop.ID, "", true)
		if cur != nil && cur.Status == StatusPaused {
			if !cur.AutoPaused {
				t.Fatalf("expected AutoPaused=true when all workers paused")
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Resuming one worker should auto-resume the loop!
	resumed, err := mgr.WorkerAction(WorkerActionRequest{
		LoopID:   loop.ID,
		WorkerID: "codex:Aris",
		Action:   "resume",
	}, "", true)
	if err != nil {
		t.Fatalf("resume worker: %v", err)
	}
	if resumed.Status != StatusRunning {
		t.Fatalf("expected loop to auto-resume to running, got %s", resumed.Status)
	}
	_, _ = mgr.Stop(loop.ID, "", true)
}
