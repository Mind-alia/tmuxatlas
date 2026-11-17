package loops

import (
	"strings"
	"testing"
)

func TestFailedPassNeverCompletesStep(t *testing.T) {
	for _, rec := range []IterationRecord{{ExitCode: 1, Result: &PassResult{CompletedSteps: []string{"a"}}}, {Verification: &Verification{Passed: false}, Result: &PassResult{CompletedSteps: []string{"a"}}}} {
		l := &Loop{MissionSpec: MissionSpec{Plan: []PlanStep{{ID: "a", Title: "fix"}}}}
		rec.Decision = decidePass(&rec)
		applyStepResult(l, &rec)
		if l.Plan[0].Done || l.Plan[0].State == "validated" {
			t.Fatal("false completion")
		}
	}
}
func TestObservedFailureInNextPrompt(t *testing.T) {
	l := Loop{Iterations: []IterationRecord{{Verification: &Verification{Command: "test", Output: "real failure", Passed: false}}, {Status: "running"}}}
	if !strings.Contains(buildIterationPrompt(l, 2), "real failure") {
		t.Fatal("observed failure omitted")
	}
}
func TestDependenciesAndLegacyCompletion(t *testing.T) {
	plan := []PlanStep{{ID: "a", Title: "one", Done: true}, {ID: "b", Title: "two", DependsOn: []string{"a"}}}
	if nextStep(plan) != nil {
		t.Fatal("legacy declaration treated as proof")
	}
	plan[0].State = "validated"
	if nextStep(plan).ID != "b" {
		t.Fatal("eligible step missing")
	}
	plan[0].DependsOn = []string{"b"}
	if validatePlan(plan) == nil {
		t.Fatal("cycle accepted")
	}
}
