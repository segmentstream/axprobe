// Package review is the AX review agent: given a run report, it identifies the
// agentic-experience defect the run reveals and drafts a finding — guided by the
// axprobe-author skill as its rubric. The Observed transcript is taken verbatim
// from the report; the model supplies judgment only. It drafts; it never files.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/report"
	"github.com/segmentstream/axprobe/internal/skill"
)

type finding struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	WhyItMatters []string `json:"why_it_matters"`
	IdealFlow    string   `json:"ideal_flow"`
	Request      []string `json:"request"`
}

// WithModel reviews a run report with an LLM acting as an AX reviewer and returns
// a paste-ready finding draft. Falls back to the mechanical draft if the model
// does not return a usable finding.
func WithModel(ctx context.Context, client *llm.Client, r report.Report) (string, error) {
	msg, _, err := client.Chat(ctx, []llm.Message{
		{Role: "system", Content: skill.Body + "\n\n" + reviewerInstructions},
		{Role: "user", Content: reportContext(r)},
	}, nil)
	if err != nil {
		return "", err
	}
	var f finding
	if err := json.Unmarshal([]byte(extractJSON(msg.Content)), &f); err != nil || strings.TrimSpace(f.Title) == "" {
		return report.Draft(r), nil
	}
	return report.RenderFinding(r, f.Title, f.Summary, f.WhyItMatters, f.IdealFlow, f.Request), nil
}

const reviewerInstructions = `You are an AX reviewer. Given a run report (an agent driving a CLI toward a goal), identify the single most important agentic-experience defect the run reveals and produce a finding.
Return ONLY a JSON object, no prose:
{"title":"<the defect in one line>","summary":"<one paragraph>","why_it_matters":["<principle broken + impact>", "..."],"ideal_flow":"<a short fenced transcript of what SHOULD have happened>","request":["<concrete change>", "..."]}
Base everything ONLY on what the transcript shows — do not invent commands the run did not imply. Name the principle(s) broken (self-sufficiency, honest-state, missing-guidance, discover-don't-ask, …). If the run reveals no real defect, set "title" to "".`

func reportContext(r report.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario: %s\noutcome: %s\nhuman_interventions: %d\nfalse_errors: %d\n\ntranscript (command → result):\n",
		r.Scenario, r.Outcome, r.HumanInterventions, len(r.FalseErrors))
	for _, s := range r.Transcript {
		fmt.Fprintf(&b, "$ %s\n", s.Command)
		if s.Result != "" {
			fmt.Fprintf(&b, "  → %s (exit %d)\n", s.Result, s.ExitCode)
		}
	}
	if len(r.Observations) > 0 {
		b.WriteString("\nagent observations:\n")
		for _, o := range r.Observations {
			fmt.Fprintf(&b, "- [%s] %s\n", o.Category, o.Note)
		}
	}
	return b.String()
}

func extractJSON(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}
