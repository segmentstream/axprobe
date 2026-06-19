package report

import (
	"testing"

	"github.com/segmentstream/axprobe/internal/driver"
	"github.com/segmentstream/axprobe/internal/manifest"
)

func pBool(b bool) *bool { return &b }
func pInt(i int) *int    { return &i }

func TestEvaluatePasses(t *testing.T) {
	rep := Report{Outcome: "goal_reached", GoalReached: true, HumanInterventions: 1}
	e := &manifest.Expect{GoalReached: pBool(true), MaxHumanInterventions: pInt(1), MaxFalseErrors: pInt(0)}
	if f := Evaluate(rep, e); len(f) != 0 {
		t.Fatalf("expected pass, got failures: %v", f)
	}
	if f := Evaluate(rep, nil); f != nil {
		t.Fatalf("nil expect must pass, got %v", f)
	}
}

func TestEvaluateFails(t *testing.T) {
	rep := Report{
		Outcome:            "stopped_at_gate",
		GoalReached:        false,
		HumanInterventions: 2,
		FalseErrors:        []driver.FalseError{{}, {}},
	}
	e := &manifest.Expect{
		GoalReached:           pBool(true),
		Outcome:               "goal_reached",
		MaxHumanInterventions: pInt(1),
		MaxFalseErrors:        pInt(0),
	}
	f := Evaluate(rep, e)
	if len(f) != 4 {
		t.Fatalf("expected 4 failures (goal_reached, outcome, HIC, false_errors), got %d: %v", len(f), f)
	}
}
