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
	"regexp"
	"strings"

	"github.com/segmentstream/axprobe/internal/driver"
	"github.com/segmentstream/axprobe/internal/manifest"
)

// schemaVersion is the version of the report contract (schema/report.schema.json).
// Bump on breaking changes so regression consumers can adapt.
const schemaVersion = "2"

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
	DriverModel        string               `json:"driver_model"`
	Outcome            string               `json:"outcome"`
	GoalReached        bool                 `json:"goal_reached"`
	HumanInterventions int                  `json:"human_interventions"`
	GateDetails        []string             `json:"gate_details"`
	Steps              int                  `json:"steps"`
	CommandsRun        int                  `json:"commands_run"`
	DurationSeconds    float64              `json:"duration_seconds"`
	Observations       []driver.Observation `json:"observations"`
	FalseErrors        []driver.FalseError  `json:"false_errors"`
	Transcript         []driver.Step        `json:"transcript"`
	PostMortem         string               `json:"post_mortem,omitempty"`
	Tokens             Tokens               `json:"tokens"`
	Summary            string               `json:"summary"`
}

// From builds a Report from a run result.
func From(scenario string, r *driver.Result) Report {
	return Report{
		SchemaVersion:      schemaVersion,
		Scenario:           scenario,
		DriverModel:        r.DriverModel,
		Outcome:            r.Outcome,
		GoalReached:        r.GoalReached,
		HumanInterventions: len(r.Gates),
		GateDetails:        nonNil(r.Gates),
		Steps:              r.Steps,
		CommandsRun:        r.CommandsRun,
		DurationSeconds:    round1(r.DurationSec),
		Observations:       nonNilObs(r.Observations),
		FalseErrors:        nonNilFE(r.FalseErrors),
		Transcript:         nonNilSteps(r.Transcript),
		PostMortem:         r.PostMortem,
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
	fmt.Fprintf(w, "driver_model:        %s\n", r.DriverModel)
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
	fmt.Fprintf(w, "observations:        %d%s\n", len(r.Observations), categoryTally(r.Observations))
	for i, o := range r.Observations {
		fmt.Fprintf(w, "    %d. [%s] %s\n", i+1, o.Category, o.Note)
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
	if pm := strings.TrimSpace(r.PostMortem); pm != "" {
		fmt.Fprintln(w, "\n── driver post-mortem ──────────────────────")
		fmt.Fprintln(w, pm)
	}
}

// categoryTally renders a compact per-category breakdown like
// "  (missing_guidance 1, friction 2)" — the actionable feedback at a glance.
func categoryTally(obs []driver.Observation) string {
	if len(obs) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, o := range obs {
		counts[o.Category]++
	}
	var parts []string
	for _, cat := range driver.ObservationCategories {
		if n := counts[cat]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", cat, n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

// maxDraftSteps caps the transcript shown in a draft to the endgame, where the
// wall usually is; earlier steps are summarized as omitted.
const maxDraftSteps = 15

// ObservedBlock renders the run transcript as the Observed evidence (a fenced
// block of `$ command → result`), trimmed to the endgame. This is the real,
// verbatim evidence — never model-generated.
func ObservedBlock(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Driving `%s` with %s (outcome: %s, HIC: %d, false_errors: %d):\n\n```\n",
		r.Scenario, r.DriverModel, r.Outcome, r.HumanInterventions, len(r.FalseErrors))
	steps := r.Transcript
	if len(steps) > maxDraftSteps {
		fmt.Fprintf(&b, "… (%d earlier steps omitted) …\n", len(steps)-maxDraftSteps)
		steps = steps[len(steps)-maxDraftSteps:]
	}
	for _, s := range steps {
		fmt.Fprintf(&b, "$ %s\n", s.Command)
		if s.Result != "" {
			note := ""
			if s.ExitCode != 0 {
				note = fmt.Sprintf("  (exit %d)", s.ExitCode)
			}
			fmt.Fprintf(&b, "→ %s%s\n", s.Result, note)
		}
	}
	b.WriteString("```\n")
	return b.String()
}

// RenderFinding assembles a public-safe finding. Earlier versions printed the
// raw transcript verbatim; that made good private diagnostics but poor public
// issues. Keep the public issue sanitized by default and iterate review quality
// from here as real AXprobe findings teach us better patterns.
func RenderFinding(r Report, title, summary, observed string, why []string, ideal string, request []string) string {
	var b strings.Builder
	title = sanitizePublicText(stripTitlePrefixes(title), r)
	fmt.Fprintf(&b, "Title: [AXprobe] Agentic UX: %s\n\n", title)
	fmt.Fprintf(&b, "## Summary\n%s\n\n", sanitizePublicText(summary, r))
	fmt.Fprintf(&b, "## Observed\n%s\n", sanitizedObserved(r, observed))

	b.WriteString("\n## Why it matters (Agentic Experience)\n")
	if len(why) == 0 {
		b.WriteString("_(state which principle(s) this breaks)_\n")
	} else {
		for _, w := range why {
			fmt.Fprintf(&b, "- %s\n", sanitizePublicText(w, r))
		}
	}

	b.WriteString("\n## Desired Flow\n")
	if strings.TrimSpace(ideal) == "" {
		b.WriteString("_TODO (operator): the ideal transcript — what the interaction should look like._\n")
	} else {
		b.WriteString(strings.TrimRight(sanitizePublicText(ideal, r), "\n") + "\n")
	}

	b.WriteString("\n## Request\n")
	if len(request) == 0 {
		b.WriteString("_TODO (operator): concrete, numbered changes._\n")
	} else {
		for i, rq := range request {
			fmt.Fprintf(&b, "%d. %s\n", i+1, sanitizePublicText(rq, r))
		}
	}

	b.WriteString("\n---\n\nReported from an AXprobe agentic experience review.\n")
	return b.String()
}

// Draft is the no-LLM finding: real Observed + observations as why-it-matters,
// with ideal flow and request scaffolded for the operator to complete.
func Draft(r Report) string {
	why := make([]string, 0, len(r.Observations))
	for _, o := range r.Observations {
		why = append(why, fmt.Sprintf("**%s** — %s", o.Category, o.Note))
	}
	summary := r.Summary
	if strings.TrimSpace(summary) == "" {
		summary = "The agent could not complete the goal — see the transcript below."
	}
	return RenderFinding(r, draftTitle(r), summary, "", why, "", nil)
}

func draftTitle(r Report) string {
	src := ""
	if len(r.Observations) > 0 {
		src = r.Observations[0].Note
	} else if strings.TrimSpace(r.Summary) != "" {
		src = r.Summary
	} else {
		src = "the agent got stuck driving " + r.Scenario
	}
	src = strings.Join(strings.Fields(src), " ")
	if len(src) > 80 {
		src = strings.TrimSpace(src[:80]) + "…"
	}
	return src
}

func stripTitlePrefixes(title string) string {
	title = strings.TrimSpace(title)
	for {
		next := strings.TrimSpace(strings.TrimPrefix(title, "[AXprobe]"))
		next = strings.TrimSpace(strings.TrimPrefix(next, "Agentic UX:"))
		if next == title {
			break
		}
		title = next
	}
	return title
}

func sanitizedObserved(r Report, observed string) string {
	if strings.TrimSpace(observed) != "" {
		return sanitizePublicText(strings.TrimSpace(observed), r) + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "An autonomous agentic harness attempted the workflow and ended with outcome `%s` before completing the goal.\n\n", sanitizePublicText(r.Outcome, r))
	fmt.Fprintf(&b, "Run shape: goal reached `%v`, steps `%d`, human interventions `%d`, false errors `%d`.\n\n",
		r.GoalReached, r.Steps, r.HumanInterventions, len(r.FalseErrors))
	b.WriteString("The public issue draft should describe the minimal sanitized command sequence that proves the product gap without exposing private project identifiers or raw data.\n")
	return b.String()
}

var (
	localPathRE      = regexp.MustCompile(`(?m)(/Users/[^\s'"` + "`" + `]+|/private/tmp/[^\s'"` + "`" + `]+|/tmp/[^\s'"` + "`" + `]+)`)
	bigQueryQuotedRE = regexp.MustCompile("`[A-Za-z][A-Za-z0-9_-]*\\.[A-Za-z_][A-Za-z0-9_\\-]*\\.[A-Za-z_][A-Za-z0-9_\\-]*`")
	bigQueryBareRE   = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_-]*\.[A-Za-z_][A-Za-z0-9_\-]*\.[A-Za-z_][A-Za-z0-9_\-]*\b`)
	bigQuerySlashRE  = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_-]*/[A-Za-z_][A-Za-z0-9_\-]*/[A-Za-z_][A-Za-z0-9_\-]*\b`)
	sourcePackageRE  = regexp.MustCompile(`\bsources/[A-Za-z0-9_-]+\b`)
	reportPathRE     = regexp.MustCompile(`\b[A-Za-z0-9_.-]+\.report\.json\b`)
	credentialPathRE = regexp.MustCompile(`(?i)\b[A-Za-z0-9_.-]*(credential|credentials|token|secret|key)[A-Za-z0-9_.-]*\.json\b`)
	manifestPathRE   = regexp.MustCompile(`\B\.axprobe/[A-Za-z0-9_.-]+\.ya?ml\b`)
	arbitrarySQLRE   = regexp.MustCompile(`(?i)\barbitrary SQL\b`)
	escapedPayloadRE = regexp.MustCompile(`"\{\\?"[A-Za-z0-9_.-]+\\?":[^\n]*?\\?\}"`)
	keysLikeRE       = regexp.MustCompile(`(?i)keys like\s+"[^"]+"(?:\s*,\s*"[^"]+")*`)
)

func sanitizePublicText(s string, r Report) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = localPathRE.ReplaceAllString(s, "<local-path>")
	s = bigQueryQuotedRE.ReplaceAllString(s, "`<warehouse-table>`")
	s = bigQueryBareRE.ReplaceAllString(s, "<warehouse-table>")
	s = bigQuerySlashRE.ReplaceAllString(s, "<warehouse-table>")
	s = sourcePackageRE.ReplaceAllString(s, "sources/<source-name>")
	s = reportPathRE.ReplaceAllString(s, "<report.json>")
	s = credentialPathRE.ReplaceAllString(s, "<credential-file>")
	s = manifestPathRE.ReplaceAllString(s, ".axprobe/<scenario>.yaml")
	s = arbitrarySQLRE.ReplaceAllString(s, "read-only SELECT SQL")
	s = escapedPayloadRE.ReplaceAllString(s, `"<sample-payload>"`)
	s = keysLikeRE.ReplaceAllString(s, "keys like <json-key>")
	if strings.TrimSpace(r.Scenario) != "" {
		s = strings.ReplaceAll(s, r.Scenario, "<scenario>")
	}
	if strings.TrimSpace(r.DriverModel) != "" {
		s = strings.ReplaceAll(s, r.DriverModel, "<driver-model>")
	}
	for _, term := range scenarioPrivateTerms(r.Scenario) {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(term) + `\b`)
		s = re.ReplaceAllString(s, "<source-name>")
	}
	return s
}

func scenarioPrivateTerms(scenario string) []string {
	parts := strings.FieldsFunc(strings.ToLower(scenario), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/'
	})
	generic := map[string]bool{
		"":              true,
		"adapter":       true,
		"agent":         true,
		"axprobe":       true,
		"cli":           true,
		"custom":        true,
		"event":         true,
		"events":        true,
		"flow":          true,
		"harness":       true,
		"init":          true,
		"integration":   true,
		"manifest":      true,
		"project":       true,
		"review":        true,
		"run":           true,
		"scenario":      true,
		"segmentstream": true,
		"smoke":         true,
		"source":        true,
		"test":          true,
		"tool":          true,
		"warehouse":     true,
	}
	var terms []string
	for _, p := range parts {
		if len(p) < 4 || generic[p] {
			continue
		}
		terms = append(terms, p)
	}
	return terms
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilObs(s []driver.Observation) []driver.Observation {
	if s == nil {
		return []driver.Observation{}
	}
	return s
}

func nonNilFE(s []driver.FalseError) []driver.FalseError {
	if s == nil {
		return []driver.FalseError{}
	}
	return s
}

func nonNilSteps(s []driver.Step) []driver.Step {
	if s == nil {
		return []driver.Step{}
	}
	return s
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
