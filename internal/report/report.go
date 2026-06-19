// Package report turns a driver.Result into the approved v0 AX report — both a
// machine-readable JSON artifact and a human-readable summary. The JSON field
// names are the approved metric names (see CLAUDE.md), so the artifact is the
// stable contract for CI/regression.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/segmentstream/axprobe/internal/driver"
	"github.com/segmentstream/axprobe/internal/manifest"
)

// schemaVersion is the version of the report contract (schema/report.schema.json).
// Bump on breaking changes so regression consumers can adapt.
const schemaVersion = "1"

// Tokens is the token/cost accounting block.
type Tokens struct {
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	Total      int     `json:"total"`
	CostUSD    float64 `json:"cost_usd"`
}

// Report is the approved v0 telemetry schema. See schema/report.schema.json —
// field names, types and semantics are a public contract.
type Report struct {
	SchemaVersion      string               `json:"schema_version"`
	Scenario           string               `json:"scenario"`
	Model              string               `json:"model"`
	Outcome            string               `json:"outcome"`
	GoalReached        bool                 `json:"goal_reached"`
	HumanInterventions int                  `json:"human_interventions"`
	GateDetails        []string             `json:"gate_details"`
	Steps              int                  `json:"steps"`
	CommandsRun        int                  `json:"commands_run"`
	DurationSeconds    float64              `json:"duration_seconds"`
	Observations       []driver.Observation `json:"observations"`
	FalseErrors        []driver.FalseError  `json:"false_errors"`
	Tokens             Tokens               `json:"tokens"`
	Summary            string               `json:"summary"`
}

// From builds a Report from a run result.
func From(scenario string, r *driver.Result) Report {
	return Report{
		SchemaVersion:      schemaVersion,
		Scenario:           scenario,
		Model:              r.Model,
		Outcome:            r.Outcome,
		GoalReached:        r.GoalReached,
		HumanInterventions: len(r.Gates),
		GateDetails:        nonNil(r.Gates),
		Steps:              r.Steps,
		CommandsRun:        r.CommandsRun,
		DurationSeconds:    round1(r.DurationSec),
		Observations:       r.Observations,
		FalseErrors:        r.FalseErrors,
		Tokens: Tokens{
			Prompt:     r.Tokens.PromptTokens,
			Completion: r.Tokens.CompletionTokens,
			Total:      r.Tokens.TotalTokens,
			CostUSD:    r.Tokens.Cost,
		},
		Summary: r.Summary,
	}
}

// Evaluate checks a report against a scenario's AX bar and returns one message
// per failed expectation (empty = passed / nothing asserted).
func Evaluate(r Report, e *manifest.Expect) []string {
	if e == nil {
		return nil
	}
	var fails []string
	if e.GoalReached != nil && r.GoalReached != *e.GoalReached {
		fails = append(fails, fmt.Sprintf("goal_reached: want %v, got %v", *e.GoalReached, r.GoalReached))
	}
	if e.Outcome != "" && r.Outcome != e.Outcome {
		fails = append(fails, fmt.Sprintf("outcome: want %q, got %q", e.Outcome, r.Outcome))
	}
	if e.MaxHumanInterventions != nil && r.HumanInterventions > *e.MaxHumanInterventions {
		fails = append(fails, fmt.Sprintf("human_interventions: want <= %d, got %d", *e.MaxHumanInterventions, r.HumanInterventions))
	}
	if e.MaxFalseErrors != nil && len(r.FalseErrors) > *e.MaxFalseErrors {
		fails = append(fails, fmt.Sprintf("false_errors: want <= %d, got %d", *e.MaxFalseErrors, len(r.FalseErrors)))
	}
	return fails
}

// WriteJSON writes the report as indented JSON.
func (r Report) WriteJSON(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// PrintHuman writes a readable summary.
func (r Report) PrintHuman(w io.Writer) {
	fmt.Fprintln(w, "\n── AX report ───────────────────────────────")
	fmt.Fprintf(w, "scenario:            %s\n", r.Scenario)
	fmt.Fprintf(w, "model:               %s\n", r.Model)
	fmt.Fprintf(w, "outcome:             %s\n", r.Outcome)
	fmt.Fprintf(w, "goal_reached:        %v\n", r.GoalReached)
	fmt.Fprintf(w, "human_interventions: %d\n", r.HumanInterventions)
	for _, g := range r.GateDetails {
		fmt.Fprintf(w, "    gate: %s\n", g)
	}
	fmt.Fprintf(w, "steps:               %d\n", r.Steps)
	fmt.Fprintf(w, "commands_run:        %d\n", r.CommandsRun)
	fmt.Fprintf(w, "duration_seconds:    %.1f\n", r.DurationSeconds)
	fmt.Fprintf(w, "false_errors:        %d\n", len(r.FalseErrors))
	for _, fe := range r.FalseErrors {
		fmt.Fprintf(w, "    exit %d: %s\n", fe.ExitCode, fe.Command)
	}
	fmt.Fprintf(w, "observations:        %d\n", len(r.Observations))
	for i, o := range r.Observations {
		fmt.Fprintf(w, "    %d. %s\n", i+1, o.Note)
		if s := strings.TrimSpace(o.Suggestion); s != "" {
			fmt.Fprintf(w, "       ↳ fix: %s\n", s)
		}
	}
	fmt.Fprintf(w, "tokens:              %d (prompt %d + completion %d)",
		r.Tokens.Total, r.Tokens.Prompt, r.Tokens.Completion)
	if r.Tokens.CostUSD > 0 {
		fmt.Fprintf(w, ", cost $%.4f", r.Tokens.CostUSD)
	}
	fmt.Fprintln(w)
	if s := strings.TrimSpace(r.Summary); s != "" {
		fmt.Fprintf(w, "summary:             %s\n", s)
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
