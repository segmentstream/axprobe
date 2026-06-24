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

// RequestItem is one public issue request plus the AX rationale for it.
type RequestItem struct {
	Change string `json:"change"`
	Why    string `json:"why,omitempty"`
}

// RequestItems accepts both the current review-agent shape:
//
//	[{"change":"...", "why":"..."}]
//
// and the older shape:
//
//	["..."]
//
// so older review-model responses still render.
type RequestItems []RequestItem

func (items *RequestItems) UnmarshalJSON(data []byte) error {
	var objects []struct {
		Change    string `json:"change"`
		Why       string `json:"why"`
		Request   string `json:"request"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal(data, &objects); err == nil {
		out := make([]RequestItem, 0, len(objects))
		for _, obj := range objects {
			change := strings.TrimSpace(firstNonEmpty(obj.Change, obj.Request))
			why := strings.TrimSpace(firstNonEmpty(obj.Why, obj.Rationale))
			if change == "" && why == "" {
				continue
			}
			out = append(out, RequestItem{Change: change, Why: why})
		}
		*items = out
		return nil
	}

	var lines []string
	if err := json.Unmarshal(data, &lines); err != nil {
		return err
	}
	out := make([]RequestItem, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, RequestItem{Change: line})
		}
	}
	*items = out
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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

// maxAttemptTranscriptSteps caps the deterministic fallback used by `review`.
// The review model should provide a curated path transcript, but the renderer
// still needs a useful fallback when the model omits it.
const maxAttemptTranscriptSteps = 30

const maxAttemptResultChars = 700

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
func RenderFinding(r Report, title, summary, goal, agentPath, worked, stopped, wouldHaveHelped, observed, pathTranscript, failedTranscript string, why []string, desiredTranscript string, request RequestItems) string {
	var b strings.Builder
	title = sanitizePublicText(stripTitlePrefixes(title), r)
	fmt.Fprintf(&b, "Title: [AXprobe] %s\n\n", title)
	fmt.Fprintf(&b, "## Summary\n%s\n\n", sanitizePublicText(summary, r))
	b.WriteString("## What Happened\n")
	writeNarrativeField(&b, r, "Goal", goal, fallbackGoal(r))
	writeNarrativeField(&b, r, "Agent path", firstNonEmpty(agentPath, observed), sanitizedObserved(r, observed))
	writeNarrativeField(&b, r, "What worked", worked, "_TODO (operator): useful affordances the agent successfully used._")
	writeNarrativeField(&b, r, "Where it stopped", stopped, fallbackStopped(r))
	writeNarrativeField(&b, r, "What would have helped", wouldHaveHelped, "_TODO (operator): the missing tool behavior that would have let the agent continue._")

	b.WriteString("\n## Attempt Transcript\n")
	b.WriteString(renderAttemptTranscript(r, pathTranscript))

	b.WriteString("\n## Failed Transcript\n")
	if strings.TrimSpace(failedTranscript) == "" {
		b.WriteString("_TODO (operator): the minimal sanitized command/output excerpt proving the failure._\n")
	} else {
		b.WriteString(strings.TrimRight(sanitizePublicText(failedTranscript, r), "\n") + "\n")
	}

	b.WriteString("\n## Why it matters (Agentic Experience)\n")
	if len(why) == 0 {
		b.WriteString("_(state which principle(s) this breaks)_\n")
	} else {
		for _, w := range why {
			fmt.Fprintf(&b, "- %s\n", sanitizePublicText(w, r))
		}
	}

	b.WriteString("\n## Desired Transcript\n")
	if strings.TrimSpace(desiredTranscript) == "" {
		b.WriteString("_TODO (operator): the ideal transcript — what the interaction should look like._\n")
	} else {
		b.WriteString(strings.TrimRight(sanitizePublicText(desiredTranscript, r), "\n") + "\n")
	}

	b.WriteString("\n## Request\n")
	if len(request) == 0 {
		b.WriteString("_TODO (operator): concrete, numbered changes._\n")
	} else {
		for i, rq := range request {
			change := sanitizeRequestText(rq.Change, r)
			if strings.TrimSpace(change) == "" {
				change = "_TODO (operator): concrete change._"
			}
			fmt.Fprintf(&b, "%d. %s\n", i+1, change)
			if rationale := strings.TrimSpace(sanitizePublicText(rq.Why, r)); rationale != "" {
				fmt.Fprintf(&b, "   Why: %s\n", rationale)
			}
		}
	}

	b.WriteString("\n---\n\nReported from an AXprobe agentic experience review.\n")
	return b.String()
}

func writeNarrativeField(b *strings.Builder, r Report, label, value, fallback string) {
	text := strings.TrimSpace(value)
	if text == "" {
		text = strings.TrimSpace(fallback)
	}
	fmt.Fprintf(b, "**%s:** %s\n\n", label, sanitizePublicText(text, r))
}

func fallbackGoal(r Report) string {
	return fmt.Sprintf("Complete the `%s` scenario goal.", r.Scenario)
}

func fallbackStopped(r Report) string {
	if strings.TrimSpace(r.Summary) != "" {
		return r.Summary
	}
	return fmt.Sprintf("The run ended with outcome `%s` after %d steps.", r.Outcome, r.Steps)
}

func renderAttemptTranscript(r Report, pathTranscript string) string {
	if strings.TrimSpace(pathTranscript) != "" {
		return strings.TrimRight(sanitizePublicText(pathTranscript, r), "\n") + "\n"
	}
	if len(r.Transcript) == 0 {
		return "_TODO (operator): chronological command/result path the agent attempted._\n"
	}

	var b strings.Builder
	b.WriteString("```text\n")
	steps := r.Transcript
	if len(steps) > maxAttemptTranscriptSteps {
		omitted := len(steps) - maxAttemptTranscriptSteps
		head := maxAttemptTranscriptSteps / 3
		tail := maxAttemptTranscriptSteps - head
		for _, s := range steps[:head] {
			writeAttemptStep(&b, r, s)
		}
		fmt.Fprintf(&b, "… (%d middle steps omitted) …\n", omitted)
		for _, s := range steps[len(steps)-tail:] {
			writeAttemptStep(&b, r, s)
		}
	} else {
		for _, s := range steps {
			writeAttemptStep(&b, r, s)
		}
	}
	b.WriteString("```\n")
	return b.String()
}

func writeAttemptStep(b *strings.Builder, r Report, s driver.Step) {
	cmd := strings.TrimSpace(sanitizePublicText(s.Command, r))
	if cmd == "" {
		return
	}
	fmt.Fprintf(b, "$ %s\n", cmd)
	result := strings.TrimSpace(s.Result)
	if result == "" {
		return
	}
	result = sanitizePublicText(result, r)
	if len(result) > maxAttemptResultChars {
		result = strings.TrimSpace(result[:maxAttemptResultChars]) + " …"
	}
	if s.ExitCode != 0 {
		fmt.Fprintf(b, "→ %s (exit %d)\n", result, s.ExitCode)
	} else {
		fmt.Fprintf(b, "→ %s\n", result)
	}
}

// Draft is the no-LLM finding: sanitized run shape + observations as
// why-it-matters, with transcript/request sections scaffolded for the operator.
func Draft(r Report) string {
	why := make([]string, 0, len(r.Observations))
	var suggestions []string
	for _, o := range r.Observations {
		why = append(why, fmt.Sprintf("**%s** — %s", o.Category, o.Note))
		if s := strings.TrimSpace(o.Suggestion); s != "" {
			suggestions = append(suggestions, s)
		}
	}
	summary := r.Summary
	if strings.TrimSpace(summary) == "" {
		summary = "The agent could not complete the goal — see the transcript below."
	}
	agentPath := strings.TrimSpace(r.PostMortem)
	if agentPath == "" {
		agentPath = "See the Attempt Transcript for the chronological command path."
	}
	worked := "The Attempt Transcript shows which commands produced useful state before the run stopped."
	stopped := fallbackStopped(r)
	wouldHaveHelped := "A machine-actionable next step from the final observed state, so the agent can continue without guessing."
	if len(suggestions) > 0 {
		wouldHaveHelped = strings.Join(suggestions, " ")
	}
	desired := extractIdealSequence(r.PostMortem)
	return RenderFinding(r, draftTitle(r), summary, "", agentPath, worked, stopped, wouldHaveHelped, "", "", failureTranscriptFallback(r), why, desired, nil)
}

func failureTranscriptFallback(r Report) string {
	if len(r.Transcript) == 0 {
		return ""
	}
	start := len(r.Transcript) - 5
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	b.WriteString("```text\n")
	for _, s := range r.Transcript[start:] {
		writeAttemptStep(&b, r, s)
	}
	b.WriteString("```\n")
	return b.String()
}

func extractIdealSequence(pm string) string {
	pm = strings.TrimSpace(pm)
	if pm == "" {
		return ""
	}
	lower := strings.ToLower(pm)
	for _, marker := range []string{"**ideal sequence", "ideal sequence", "desired sequence"} {
		if idx := strings.Index(lower, marker); idx >= 0 {
			return strings.TrimSpace(pm[idx:])
		}
	}
	return ""
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
	localPathRE                 = regexp.MustCompile(`(?m)(/Users/[^\s'"` + "`" + `]+|/private/tmp/[^\s'"` + "`" + `]+|/tmp/[^\s'"` + "`" + `]+)`)
	threePartQuotedIdentifierRE = regexp.MustCompile("`[A-Za-z][A-Za-z0-9_-]*\\.[A-Za-z_][A-Za-z0-9_\\-]*\\.[A-Za-z_][A-Za-z0-9_\\-]*`")
	threePartBareIdentifierRE   = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_-]*\.[A-Za-z_][A-Za-z0-9_\-]*\.[A-Za-z_][A-Za-z0-9_\-]*\b`)
	threePartSlashIdentifierRE  = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_-]*/[A-Za-z_][A-Za-z0-9_\-]*/[A-Za-z_][A-Za-z0-9_\-]*\b`)
	sourcePackageRE             = regexp.MustCompile(`\bsources/[A-Za-z0-9_-]+\b`)
	homeBinToolPathRE           = regexp.MustCompile(`(?:/root|/home/[A-Za-z0-9_-]+)/\.[A-Za-z0-9_-]+/bin/([A-Za-z0-9_-]+)`)
	reportPathRE                = regexp.MustCompile(`\b[A-Za-z0-9_.-]+\.report\.json\b`)
	credentialPathRE            = regexp.MustCompile(`(?i)\b[A-Za-z0-9_.-]*(credential|credentials|token|secret|key)[A-Za-z0-9_.-]*\.json\b`)
	manifestPathRE              = regexp.MustCompile(`\B\.axprobe/[A-Za-z0-9_.-]+\.ya?ml\b`)
	arbitrarySQLRE              = regexp.MustCompile(`(?i)\barbitrary SQL\b`)
	escapedPayloadRE            = regexp.MustCompile(`"\{\\?"[A-Za-z0-9_.-]+\\?":[^\n]*?\\?\}"`)
	keysLikeRE                  = regexp.MustCompile(`(?i)keys like\s+"[^"]+"(?:\s*,\s*"[^"]+")*`)
	generatedDocsRE             = regexp.MustCompile(`(?i)(\b[A-Za-z0-9_.-]+\.md\b|\bdocs/)`)
	sourceSuffixRE              = regexp.MustCompile(`<source-name>[-_][A-Za-z0-9_-]+`)
	doubleSourceRE              = regexp.MustCompile(`<<source-name>>`)
)

func sanitizePublicText(s string, r Report) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = homeBinToolPathRE.ReplaceAllString(s, "$1")
	s = localPathRE.ReplaceAllString(s, "<local-path>")
	s = threePartQuotedIdentifierRE.ReplaceAllString(s, "`<external-resource>`")
	s = threePartBareIdentifierRE.ReplaceAllString(s, "<external-resource>")
	s = threePartSlashIdentifierRE.ReplaceAllString(s, "<external-resource>")
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
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(term))
		s = re.ReplaceAllString(s, "<source-name>")
	}
	s = sourceSuffixRE.ReplaceAllString(s, "<source-name>")
	s = doubleSourceRE.ReplaceAllString(s, "<source-name>")
	return s
}

func sanitizeRequestText(s string, r Report) string {
	s = sanitizePublicText(s, r)
	// Keep this product-agnostic: generated docs/markdown can be an AX smell for
	// any manifest-tested tool, but the core reviewer must not know specific
	// tools or filenames beyond the generic docs/markdown pattern.
	if generatedDocsRE.MatchString(s) {
		return "Do not generate docs or markdown as the scaffold's agent-facing guidance; move source-authoring guidance into structured CLI outputs, generated implementation files, next_actions, and diagnostics."
	}
	return s
}

func scenarioPrivateTerms(scenario string) []string {
	parts := strings.FieldsFunc(strings.ToLower(scenario), func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/'
	})
	generic := map[string]bool{
		"":            true,
		"adapter":     true,
		"agent":       true,
		"axprobe":     true,
		"cli":         true,
		"custom":      true,
		"event":       true,
		"events":      true,
		"flow":        true,
		"harness":     true,
		"init":        true,
		"integration": true,
		"manifest":    true,
		"project":     true,
		"review":      true,
		"run":         true,
		"scenario":    true,
		"smoke":       true,
		"source":      true,
		"test":        true,
		"tool":        true,
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
