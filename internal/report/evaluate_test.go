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
		Scenario:           "vercel-source",
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
		"Report vercel-source in /Users/alice/project/vercel-source.report.json got stuck on the vercel source and requested arbitrary SQL against `segmentstream-ai-website.segmentstream_1774442186472.vercel_logs` using moonshotai/kimi-k2.6.",
		"The agent tried `.axprobe/vercel-source.yaml`, read /private/tmp/run.json, queried segmentstream-ai-website/segmentstream_1774442186472/vercel_logs, showed \"{\\\"path\\\":\\\"/home\\\",\\\"referrer\\\":\\\"https://example.com\\\"}\", and guessed keys like \"path\", \"referrer\".",
		[]string{"self-sufficiency: sources/vercel needed real rows from segmentstream-ai-website.segmentstream_1774442186472.vercel_logs"},
		"$ segmentstream warehouse query --sql \"select * from `segmentstream-ai-website.segmentstream_1774442186472.vercel_logs` limit 5\" --json",
		[]string{"Use /Users/alice/.config/key.json only through configured credentials."},
	)

	mustContain := []string{
		"Title: [AXprobe] Agentic UX: source authoring needs query",
		"<scenario>",
		"<local-path>",
		"`<warehouse-table>`",
		"<warehouse-table>",
		"sources/<source-name>",
		"<driver-model>",
		"read-only SELECT SQL",
		`"<sample-payload>"`,
		"keys like <json-key>",
		"Reported from an AXprobe agentic experience review.",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q:\n%s", want, out)
		}
	}

	mustNotContain := []string{
		"vercel-source",
		"/Users/alice",
		"/private/tmp",
		"vercel",
		"segmentstream-ai-website",
		"segmentstream_1774442186472",
		"moonshotai/kimi-k2.6",
		"arbitrary SQL",
		"/home",
		"example.com",
	}
	for _, leak := range mustNotContain {
		if strings.Contains(out, leak) {
			t.Fatalf("public issue leaked %q:\n%s", leak, out)
		}
	}
}
