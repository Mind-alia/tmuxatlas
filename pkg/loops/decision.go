package loops

import (
	"fmt"
	"path/filepath"
	"time"
)

type Directive struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}
type ReviewRecord struct {
	Author    string    `json:"author"`
	At        time.Time `json:"at"`
	Iteration int       `json:"iteration"`
	Criteria  string    `json:"criteria"`
}

func canonicalDir(dir string) string {
	if dir == "" {
		return ""
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}

func stepState(s PlanStep) string {
	if s.State != "" {
		return s.State
	}
	if s.Done {
		return "review"
	}
	return "todo"
}
func nextStep(plan []PlanStep) *PlanStep {
	validated := map[string]bool{}
	for _, s := range plan {
		validated[s.ID] = stepState(s) == "validated"
	}
	for _, s := range plan {
		state := stepState(s)
		if state == "validated" || state == "blocked" || state == "review" {
			continue
		}
		ready := true
		for _, id := range s.DependsOn {
			if !validated[id] {
				ready = false
			}
		}
		if ready {
			copy := s
			return &copy
		}
	}
	return nil
}

// decidePass is independent of processes, persistence and UI.
func decidePass(rec *IterationRecord) string {
	if rec.ExitCode != 0 || rec.Error != "" || rec.RateLimited {
		return "retry"
	}
	if rec.Verification != nil && !rec.Verification.Passed {
		return "correct"
	}
	if rec.Result == nil {
		return "inspect"
	}
	if rec.Result.Decision != "" {
		return "ask"
	}
	return "review"
}
func applyStepResult(loop *Loop, rec *IterationRecord) {
	step := nextStep(loop.Plan)
	if step == nil {
		return
	}
	rec.StepID = step.ID
	for i := range loop.Plan {
		if loop.Plan[i].ID != step.ID {
			continue
		}
		s := &loop.Plan[i]
		if rec.Decision == "correct" || rec.Decision == "retry" {
			s.State = "todo"
			s.Done = false
			return
		}
		if rec.Result != nil {
			for _, id := range rec.Result.CompletedSteps {
				if id == s.ID {
					s.State = "review"
					s.Done = false
					s.EvidenceIteration = rec.Number
				}
			}
		}
	}
}

func validatePlan(plan []PlanStep) error {
	byID := map[string]PlanStep{}
	for _, s := range plan {
		byID[s.ID] = s
		switch s.State {
		case "", "todo", "working", "review", "validated", "blocked":
		default:
			return fmt.Errorf("%w: état d’étape invalide", ErrInvalid)
		}
	}
	visiting := map[string]bool{}
	done := map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: dépendances cycliques", ErrInvalid)
		}
		if done[id] {
			return nil
		}
		s, ok := byID[id]
		if !ok {
			return fmt.Errorf("%w: dépendance inconnue", ErrInvalid)
		}
		visiting[id] = true
		for _, dep := range s.DependsOn {
			if err := walk(dep); err != nil {
				return err
			}
		}
		visiting[id] = false
		done[id] = true
		return nil
	}
	for id := range byID {
		if err := walk(id); err != nil {
			return err
		}
	}
	return nil
}
