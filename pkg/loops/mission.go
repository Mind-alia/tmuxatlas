package loops

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MissionSpec is optional so persisted legacy loops retain their behavior.
type PlanStep struct {
	State             string   `json:"state,omitempty"`
	Expected          string   `json:"expected,omitempty"`
	DependsOn         []string `json:"depends_on,omitempty"`
	EvidenceIteration int      `json:"evidence_iteration,omitempty"`
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Done              bool     `json:"done"`
}

type MissionSpec struct {
	Plan             []PlanStep `json:"plan,omitempty"`
	TimeLimitMinutes int        `json:"time_limit_minutes,omitempty"`
	VerifyCommand    string     `json:"verify_command,omitempty"`
	Mode             string     `json:"mode,omitempty"`
	Scope            string     `json:"scope,omitempty"`
	Criteria         string     `json:"criteria,omitempty"`
}

type PassResult struct {
	CompletedSteps []string `json:"completed_steps,omitempty"`
	Problem        string   `json:"problem"`
	Change         string   `json:"change"`
	Checks         string   `json:"checks"`
	Limitations    string   `json:"limitations"`
	Next           string   `json:"next"`
	Decision       string   `json:"decision"`
}

func boundedMemory(s string) string {
	if len(s) > 24000 {
		return strings.ToValidUTF8(s[len(s)-24000:], "")
	}
	return s
}

func parsePassResult(output string) *PassResult {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "ATLAS_RESULT ") {
			var r PassResult
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "ATLAS_RESULT ")), &r) == nil && r.Change != "" {
				return &r
			}
			return nil
		}
	}
	return nil
}

// A temporary index captures tracked, staged and untracked content without changing
// the user's index or creating commits. Tree objects also cover commits made in a pass.
func snapshotTree(dir string) (string, bool) {
	f, err := os.CreateTemp("", "atlas-index-*")
	if err != nil {
		return "", false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	defer os.Remove(name)
	run := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+name)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err = run("read-tree", "HEAD"); err != nil {
		return "", false
	}
	if _, err = run("add", "-A", "--"); err != nil {
		return "", false
	}
	tree, err := run("write-tree")
	return tree, err == nil
}

func (m *Manager) SetInstruction(id, text, owner string, admin bool) (*Loop, error) {
	text = strings.TrimSpace(text)
	if len(text) > 8000 {
		return nil, fmt.Errorf("%w: consigne trop longue", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.loops[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !canAccess(l, owner, admin) {
		return nil, ErrForbidden
	}
	before := cloneLoop(l, nil)
	l.Directives = append(l.Directives, Directive{Text: text, At: time.Now()})
	l.PendingInstruction = text
	l.UpdatedAt = time.Now()
	if err := m.saveLocked(); err != nil {
		*l = before
		return nil, err
	}
	copy := cloneLoop(l, m.active[id])
	return &copy, nil
}

// Verification is an observed command result, distinct from agent declarations.
type Verification struct {
	DurationMs int64  `json:"duration_ms"`
	Command    string `json:"command"`
	Output     string `json:"output"`
	Passed     bool   `json:"passed"`
}

func (s MissionSpec) validate() error {
	if s.Mode != "" && s.Mode != "mission" && s.Mode != "maintenance" {
		return fmt.Errorf("%w: mode inconnu", ErrInvalid)
	}
	if s.TimeLimitMinutes < 0 || s.TimeLimitMinutes > 10080 || len(s.Criteria) > 8000 || len(s.Scope) > 8000 || len(s.VerifyCommand) > 2000 {
		return fmt.Errorf("%w: paramètres de mission invalides", ErrInvalid)
	}
	if len(s.Plan) > 100 {
		return fmt.Errorf("%w: maximum 100 étapes", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, step := range s.Plan {
		if step.ID == "" || seen[step.ID] || len(step.Title) > 2000 || strings.TrimSpace(step.Title) == "" {
			return fmt.Errorf("%w: étape invalide", ErrInvalid)
		}
		seen[step.ID] = true
	}
	return validatePlan(s.Plan)
}

func (m *Manager) Review(id, owner string, admin bool) (*Loop, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.loops[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !canAccess(l, owner, admin) {
		return nil, ErrForbidden
	}
	if m.active[id] != nil || len(l.Iterations) == 0 {
		return nil, fmt.Errorf("%w: résultat indisponible pour revue", ErrConflict)
	}
	before := cloneLoop(l, nil)
	l.Reviews = append(l.Reviews, ReviewRecord{Author: owner, At: time.Now(), Iteration: l.Iterations[len(l.Iterations)-1].Number, Criteria: l.Criteria})
	l.Outcome = "achieved"
	l.UpdatedAt = time.Now()
	if err := m.saveLocked(); err != nil {
		*l = before
		return nil, err
	}
	copy := cloneLoop(l, nil)
	return &copy, nil
}

// SetPlan edits an inactive loop; running agents always see a stable plan.
func (m *Manager) SetPlan(id string, plan []PlanStep, owner string, admin bool) (*Loop, error) {
	if err := (MissionSpec{Plan: plan}).validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.loops[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !canAccess(l, owner, admin) {
		return nil, ErrForbidden
	}
	if m.active[id] != nil {
		return nil, fmt.Errorf("%w: mettez la boucle en pause pour modifier le plan", ErrConflict)
	}
	before := cloneLoop(l, nil)
	l.Plan = append([]PlanStep(nil), plan...)
	l.UpdatedAt = time.Now()
	if err := m.saveLocked(); err != nil {
		*l = before
		return nil, err
	}
	copy := cloneLoop(l, nil)
	return &copy, nil
}

// pickGlobalWorkerLocked reserves accounts serially under the manager lock.
// Other owners' names and work are never included in the public loop state.
func (m *Manager) pickGlobalWorkerLocked(l *Loop, last string, now time.Time) (int, bool) {
	for _, other := range m.loops {
		if other.ID != l.ID && m.active[other.ID] != nil && other.ActiveWorkerID != "" && l.WorkDir != "" && canonicalDir(other.WorkDir) == canonicalDir(l.WorkDir) {
			return -1, true
		}
	}
	candidate := *l
	candidate.Workers = append([]LoopWorker(nil), l.Workers...)
	busy := false
	for i, w := range candidate.Workers {
		if !w.Enabled || w.Paused || w.Status != WorkerReady {
			continue
		}
		for _, other := range m.loops {
			if other.ID == l.ID {
				continue
			}
			for _, ow := range other.Workers {
				if ow.ID != w.ID {
					continue
				}
				if ow.ResetAt != nil && ow.ResetAt.After(now) && ow.Status == WorkerRateLimited {
					l.Workers[i].ResetAt = cloneTimePtr(ow.ResetAt)
					l.Workers[i].RateLimitedAt = cloneTimePtr(ow.RateLimitedAt)
					l.Workers[i].Status = WorkerRateLimited
					candidate.Workers[i].Status = WorkerRateLimited
				}
				if m.active[other.ID] != nil && other.ActiveWorkerID == w.ID && ow.Status == WorkerRunning {
					candidate.Workers[i].Status = WorkerRunning
					busy = true
				}
			}
		}
	}
	return pickNextWorkerLocked(&candidate, last), busy
}

// stagnationReason uses repeated failures or unchanged/reverting snapshots;
// a diagnostic pass without code changes alone never triggers a block.
func stagnationReason(records []IterationRecord) string {
	n := len(records)
	if n < 3 {
		return ""
	}
	a, b, c := records[n-3], records[n-2], records[n-1]
	if a.Error != "" && a.Error == b.Error && b.Error == c.Error {
		return "Même échec sur trois passes"
	}
	if a.Verification != nil && b.Verification != nil && c.Verification != nil && !a.Verification.Passed && !b.Verification.Passed && !c.Verification.Passed && a.Verification.Output == b.Verification.Output && b.Verification.Output == c.Verification.Output {
		return "Même échec de vérification sur trois passes"
	}
	if a.BeforeTree != "" && a.BeforeTree == c.AfterTree && a.AfterTree == b.BeforeTree && b.AfterTree == c.BeforeTree && a.BeforeTree != a.AfterTree {
		return "Les modifications reviennent à un état antérieur sans progrès durable"
	}
	if a.Result != nil && b.Result != nil && c.Result != nil && c.Result.Decision != "" && a.Result.Decision == b.Result.Decision && b.Result.Decision == c.Result.Decision {
		return "Décision demandée sur trois passes : " + c.Result.Decision
	}
	return ""
}

// tailWriter bounds verification output even for noisy commands.
type tailWriter struct{ data []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	w.data = appendTail(w.data, p, 32000)
	return len(p), nil
}
