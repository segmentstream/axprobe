package report

import (
	"strings"
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

func TestRenderFindingProducesPublicSafeIssue(t *testing.T) {
	rep := Report{
		Scenario:           "aurora-source",
		DriverModel:        "moonshotai/kimi-k2.6",
		Outcome:            "stuck",
		GoalReached:        false,
		HumanInterventions: 0,
		Steps:              30,
		FalseErrors:        []driver.FalseError{{}},
	}
	out := RenderFinding(
		rep,
		"Agentic UX: source authoring needs query",
		"Report aurora-source in /Users/alice/project/aurora-source.report.json got stuck on the aurora source and requested arbitrary SQL against `acme-project.raw_events.clicks` using moonshotai/kimi-k2.6.",
		"Add aurora as a source from `acme-project.raw_events.clicks`.",
		"The agent tried `.axprobe/aurora-source.yaml`, read /private/tmp/run.json, queried acme-project/raw_events/clicks, showed \"{\\\"path\\\":\\\"/home\\\",\\\"referrer\\\":\\\"https://example.com\\\"}\", and guessed keys like \"path\", \"referrer\".",
		"The source scaffold command returned files and a contract.",
		"The agent could not inspect rows from `acme-project.raw_events.clicks`.",
		"A read-only SELECT query tool with provider diagnostics.",
		"",
		"$ /root/.example/bin/example-cli --help\n→ Example CLI\n$ example-cli source scaffold aurora --json\n→ created sources/aurora\n$ example-cli data query --sql \"select * from `acme-project.raw_events.clicks` limit 5\" --json\n→ Referenced resource was not found in configured location US",
		"$ example-cli data query --sql \"select * from `acme-project.raw_events.clicks` limit 5\" --json\n→ Referenced resource was not found in configured location US",
		[]string{"self-sufficiency: sources/aurora needed real rows from acme-project.raw_events.clicks"},
		"$ example-cli data query --sql \"select * from `acme-project.raw_events.clicks` limit 5\" --json\n# Edit model source named aurora_raw and placeholder <aurora-logs-table>",
		RequestItems{{
			Change: "Update SCAFFOLD_GUIDE.md with instructions for /Users/alice/.config/key.json.",
			Why:    "The agent needs machine-readable recovery guidance instead of reading /Users/alice/project docs.",
		}},
	)

	mustContain := []string{
		"Title: [AXprobe] source authoring needs query",
		"## What Happened",
		"**Goal:**",
		"**Agent path:**",
		"**What worked:**",
		"**Where it stopped:**",
		"**What would have helped:**",
		"## Attempt Transcript",
		"$ example-cli --help",
		"## Failed Transcript",
		"## Desired Transcript",
		"<scenario>",
		"<local-path>",
		"`<external-resource>`",
		"<external-resource>",
		"sources/<source-name>",
		"<driver-model>",
		"read-only SELECT SQL",
		`"<sample-payload>"`,
		"keys like <json-key>",
		"Do not generate docs or markdown",
		"Why: The agent needs machine-readable recovery guidance",
		"Reported from an AXprobe agentic experience review.",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q:\n%s", want, out)
		}
	}

	mustNotContain := []string{
		"aurora-source",
		"/Users/alice",
		"/private/tmp",
		"/root/.example",
		"aurora",
		"aurora_raw",
		"<source-name>-logs-table",
		"<<source-name>",
		"acme-project",
		"raw_events",
		"moonshotai/kimi-k2.6",
		"arbitrary SQL",
		"SCAFFOLD_GUIDE.md",
		"/home",
		"example.com",
	}
	for _, leak := range mustNotContain {
		if strings.Contains(out, leak) {
			t.Fatalf("public issue leaked %q:\n%s", leak, out)
		}
	}
}

func TestSanitizeKeepsGenericReadyFromScenarioName(t *testing.T) {
	rep := Report{Scenario: "vercel-run-ready"}
	got := sanitizePublicText("The project is ready; next action is run.", rep)
	if strings.Contains(got, "<source-name>") {
		t.Fatalf("generic ready was redacted as source name: %q", got)
	}
	if !strings.Contains(got, "ready") {
		t.Fatalf("expected ready to remain readable: %q", got)
	}
}

func TestDraftDoesNotPublishPostMortemSpeculation(t *testing.T) {
	rep := Report{
		Scenario: "server-run",
		Outcome:  "stuck",
		Summary:  "The command failed after starting services.",
		PostMortem: `**Post-mortem**
I was trying to run the tool.

**Ideal command sequence**
$ tool run --json --health-host 127.0.0.1
→ Not offered by the CLI`,
		Transcript: []driver.Step{{
			Command:  "tool run --json",
			Result:   `json: command="run" status="error" diagnostics.message="connection refused"`,
			ExitCode: 1,
		}},
	}

	out := Draft(rep)
	for _, leak := range []string{"Post-mortem", "Ideal command sequence", "--health-host", "Not offered by the CLI"} {
		if strings.Contains(out, leak) {
			t.Fatalf("fallback draft leaked post-mortem speculation %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "$ tool run --json") {
		t.Fatalf("fallback draft should keep transcript evidence:\n%s", out)
	}
}
