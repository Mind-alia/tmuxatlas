package loops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStructuredResultFallback(t *testing.T) {
	for _, s := range []string{"plain text", `ATLAS_RESULT {broken}`, `ATLAS_RESULT {"checks":"ok"}`} {
		if parsePassResult(s) != nil {
			t.Fatal("invalid result accepted")
		}
	}
	r := parsePassResult("log\nATLAS_RESULT {\"change\":\"Fix signup\",\"next\":\"Review\"}\nLOOP_DONE")
	if r == nil || r.Change != "Fix signup" {
		t.Fatal(r)
	}
}

func TestSnapshotsIncludeUntrackedAndPreserveIndex(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		o, e := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %s", e, o)
		}
		return string(o)
	}
	git("init")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "tracked"), []byte("one\n"), 0600)
	git("add", ".")
	git("commit", "-m", "initial")
	before, ok := snapshotTree(dir)
	if !ok {
		t.Fatal("snapshot failed")
	}
	os.WriteFile(filepath.Join(dir, "untracked"), []byte("new content\n"), 0600)
	indexBefore := git("diff", "--cached")
	after, ok := snapshotTree(dir)
	if !ok {
		t.Fatal("snapshot failed")
	}
	if indexBefore != git("diff", "--cached") {
		t.Fatal("index modified")
	}
	if !strings.Contains(git("diff", before, after), "new content") {
		t.Fatal("untracked content missing")
	}
}

func TestInstructionPersistenceAndReviewAuthorization(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, nil, Options{AccountsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m.loops["mission"] = &Loop{ID: "mission", OwnerID: "alice", Status: StatusPaused, Iterations: []IterationRecord{{Number: 1}}}
	if _, err = m.SetInstruction("mission", "Do next", "bob", false); err != ErrForbidden {
		t.Fatalf("unauthorized instruction: %v", err)
	}
	if _, err = m.SetInstruction("mission", "Do next", "alice", false); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	loaded, err := NewManager(dir, nil, Options{AccountsDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Shutdown()
	if loaded.loops["mission"].PendingInstruction != "Do next" {
		t.Fatal("instruction not persisted")
	}
	if _, err = loaded.Review("mission", "bob", false); err != ErrForbidden {
		t.Fatalf("unauthorized review: %v", err)
	}
	l, err := loaded.Review("mission", "alice", false)
	if err != nil || l.Outcome != "achieved" {
		t.Fatalf("review: %+v %v", l, err)
	}
}

func TestSharedAccountReservationAndCooldown(t *testing.T) {
	m := &Manager{loops: map[string]*Loop{}, active: map[string]*activeRun{}}
	a := &Loop{ID: "a", ActiveWorkerID: "claude:one", Workers: []LoopWorker{{ID: "claude:one", Enabled: true, Status: WorkerRunning}}}
	b := &Loop{ID: "b", Workers: []LoopWorker{{ID: "claude:one", Enabled: true, Status: WorkerReady}, {ID: "codex:two", Enabled: true, Status: WorkerReady}}}
	m.loops[a.ID] = a
	m.loops[b.ID] = b
	m.active[a.ID] = &activeRun{}
	idx, busy := m.pickGlobalWorkerLocked(b, "", time.Now())
	if idx != 1 || !busy {
		t.Fatalf("idx=%d busy=%v", idx, busy)
	}
	reset := time.Now().Add(time.Hour)
	a.Workers[0].Status = WorkerRateLimited
	a.Workers[0].ResetAt = &reset
	m.pickGlobalWorkerLocked(b, "", time.Now())
	if b.Workers[0].Status != WorkerRateLimited {
		t.Fatal("cooldown not shared")
	}
}

func TestStagnationDoesNotBlockDiagnostics(t *testing.T) {
	records := []IterationRecord{{Snapshot: true}, {Snapshot: true}, {Snapshot: true}}
	if reason := stagnationReason(records); reason != "" {
		t.Fatal(reason)
	}
	for i := range records {
		records[i].Verification = &Verification{Output: "same failure", Passed: false}
	}
	if reason := stagnationReason(records); reason == "" {
		t.Fatal("repeated verification failure ignored")
	}
}

func TestPlanValidationAndIsolation(t *testing.T) {
	if (MissionSpec{Plan: []PlanStep{{ID: "x", Title: "one"}, {ID: "x", Title: "two"}}}).validate() == nil {
		t.Fatal("duplicate ids accepted")
	}
	original := &Loop{MissionSpec: MissionSpec{Plan: []PlanStep{{ID: "a", Title: "one"}}}}
	copy := cloneLoop(original, nil)
	copy.Plan[0].Done = true
	if original.Plan[0].Done {
		t.Fatal("plan aliases manager state")
	}
}

func TestPlanEditingPersistenceAndAccess(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	m.loops["plan"] = &Loop{ID: "plan", OwnerID: "alice", Status: StatusPaused}
	plan := []PlanStep{{ID: "one", Title: "Verify registration"}}
	if _, err = m.SetPlan("plan", plan, "bob", false); err != ErrForbidden {
		t.Fatal(err)
	}
	m.active["plan"] = &activeRun{}
	if _, err = m.SetPlan("plan", plan, "alice", false); err == nil {
		t.Fatal("edited running plan")
	}
	delete(m.active, "plan")
	if _, err = m.SetPlan("plan", plan, "alice", false); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewManager(dir, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Shutdown()
	if loaded.loops["plan"].Plan[0].Title != plan[0].Title {
		t.Fatal("plan not persisted")
	}
}

func TestVerificationOutputIsBounded(t *testing.T) {
	w := &tailWriter{}
	data := []byte(strings.Repeat("x", 64000))
	n, err := w.Write(data)
	if err != nil || n != len(data) || len(w.data) != 32000 {
		t.Fatalf("n=%d stored=%d err=%v", n, len(w.data), err)
	}
}
