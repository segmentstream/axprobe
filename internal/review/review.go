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
	Observed     string   `json:"observed"`
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
	return report.RenderFinding(r, f.Title, f.Summary, f.Observed, f.WhyItMatters, f.IdealFlow, f.Request), nil
}

const reviewerInstructions = `You are an AX reviewer. Given a run report (an agent driving a CLI toward a goal), produce a finding about the agentic-experience defect(s) the run reveals.
Return ONLY a JSON object, no prose:
{"title":"<the defect in one line, WITHOUT '[AXprobe]' or 'Agentic UX:' prefixes>","summary":"<one paragraph>","observed":"<public-safe observed section>","why_it_matters":["<principle broken + impact>", "..."],"ideal_flow":"<see ideal_flow rules>","request":["<concrete change>", "..."]}
Rules:
- The output is a PUBLIC GitHub issue draft for the tested tool's repository. Do not reveal private provenance: no manifest paths, local paths, report paths, usernames, project IDs, dataset/table names, credential paths, raw payload values, secrets, tokens, or exact internal repo names unless the transcript proves they are public tool names.
- Do not reveal private source names either. If the run used a named source, package, adapter, customer, campaign, project, dataset, table, or integration as test data, replace it with placeholders such as <source-name>, <adapter-name>, or <warehouse-table>.
- Mention that an autonomous/agentic harness attempted the workflow, but do not name the private scenario or report file. AXprobe attribution is added by the renderer; do not add your own footer.
- observed is a short sanitized account of what the agent could do and where it got stuck. Use placeholders like <source-name>, <warehouse-table>, <payload-column>, and <local-path> when concrete identifiers are not needed. Include only minimal command examples needed to prove the product gap.
- ideal_flow is a CONCRETE tool-call transcript — the reader must see exactly what is called and why. Each step: a "# why" comment (one line), the command line ("$ ..."), and the "→ result" the tool would return. Show real command/flag names and realistic results; if a step edits a file, show the key lines. Not prose — a runnable-looking sequence.
- GROUND the ideal_flow in reality, do not invent the interface. If a "driver post-mortem" is present, build the ideal_flow from it — the driver actually ran the tool and saw its real commands, flags, and outputs. Otherwise use only command/flag names that appear in the transcript. Mark any step that needs a capability the tool does NOT yet offer with "# PROPOSED". Never fabricate a flag, command, or output the run gives no evidence for.
- Keep ideal_flow public-safe too: use placeholders for private identifiers and do not show real raw data. Do not invent sample payload contents or JSON keys; if row values were not observed, write "→ sample rows with payload values redacted" or use <json-key>.
- If the request is about SQL execution, frame it as read-only SELECT SQL with limits/timeouts, not "arbitrary SQL".
- Do not add separate requests for unsupported commands the driver guessed unless the real tool output falsely claimed success or hid the next action. Focus the request on the deepest missing capability needed to complete the goal.
- TRACE TO COMPLETION, not to the first friction. Identify the wall that actually blocks reaching the GOAL — it is often a step BEYOND where the agent stopped (e.g. the agent fixed the binding but still could not write the transform). Cover every blocking gap, not just the first.
- NAME THE DEEPEST MISSING CAPABILITY the agent would have needed — including ones it never reached. Ask: to finish, what did it have to know or do that the tool gave no way to (e.g. could it even inspect the data it must transform?).
- Base everything ONLY on what the transcript shows — do not invent commands the run did not imply.
- Name the principle(s) broken (self-sufficiency, honest-state, missing-guidance, discover-don't-ask, …). If the run reveals no real defect, set "title" to "".`

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
	if pm := strings.TrimSpace(r.PostMortem); pm != "" {
		b.WriteString("\ndriver post-mortem (the agent's own grounded reflection — build the ideal_flow from THIS, it saw the real interface):\n")
		b.WriteString(pm)
		b.WriteString("\n")
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
