// Package review is the AX review agent: given a run report, it identifies the
// agentic-experience defect the run reveals and drafts a public-safe finding —
// guided by the axprobe-author skill as its rubric. The model selects a minimal
// failed transcript excerpt and desired transcript; the renderer sanitizes the
// draft before printing. It drafts; it never files.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/report"
	"github.com/segmentstream/axprobe/internal/skill"
)

type finding struct {
	Title             string              `json:"title"`
	Summary           string              `json:"summary"`
	Observed          string              `json:"observed"`
	FailedTranscript  string              `json:"failed_transcript"`
	WhyItMatters      []string            `json:"why_it_matters"`
	DesiredTranscript string              `json:"desired_transcript"`
	IdealFlow         string              `json:"ideal_flow"` // legacy review-model field name
	Request           report.RequestItems `json:"request"`
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
	desired := f.DesiredTranscript
	if strings.TrimSpace(desired) == "" {
		desired = f.IdealFlow
	}
	return report.RenderFinding(r, f.Title, f.Summary, f.Observed, f.FailedTranscript, f.WhyItMatters, desired, f.Request), nil
}

const reviewerInstructions = `You are an AX reviewer. Given a run report (an agent driving a CLI toward a goal), produce a finding about the agentic-experience defect(s) the run reveals.
Return ONLY a JSON object, no prose:
{"title":"<the defect in one line, WITHOUT '[AXprobe]' or 'Agentic UX:' prefixes>","summary":"<one paragraph>","observed":"<public-safe observed section>","failed_transcript":"<see failed_transcript rules>","why_it_matters":["<principle broken + impact>", "..."],"desired_transcript":"<see desired_transcript rules>","request":[{"change":"<concrete change>","why":"<why this improves the agentic workflow>"}]}
Rules:
- The output is a PUBLIC GitHub issue draft for the tested tool's repository. Do not reveal private provenance: no manifest paths, local paths, report paths, usernames, project IDs, dataset/table names, credential paths, raw payload values, secrets, tokens, or exact internal repo names unless the transcript proves they are public tool names.
- Do not reveal private source names either. If the run used a named source, package, adapter, customer, campaign, project, dataset, table, resource, or integration as test data, replace it with placeholders such as <source-name>, <adapter-name>, or <external-resource>.
- Mention that an autonomous/agentic harness attempted the workflow, but do not name the private scenario or report file. AXprobe attribution is added by the renderer; do not add your own footer.
- observed is a short sanitized account of what the agent could do and where it got stuck. Use placeholders like <source-name>, <external-resource>, <field-name>, and <local-path> when concrete identifiers are not needed. Include only minimal command examples needed to prove the product gap.
- failed_transcript is a SHORT, PUBLIC-SAFE transcript excerpt of the real failure. It must be commands and results, not prose. Use placeholders for private identifiers. Include the command that proves the wall and any immediately preceding command that should have supplied enough state to avoid it.
- desired_transcript is a CONCRETE tool-call transcript — the reader must see exactly what is called and why. Each step: a "# why" comment (one line), the command line ("$ ..."), and the "→ result" the tool would return. Show real command/flag names and realistic results; if a step edits a file, show the key lines. Not prose — a runnable-looking sequence.
- GROUND desired_transcript in reality, do not invent the interface. If a "driver post-mortem" is present, build the desired_transcript from it — the driver actually ran the tool and saw its real commands, flags, and outputs. Otherwise use only command/flag names that appear in the transcript. Mark any step that needs a capability the tool does NOT yet offer with "# PROPOSED". Never fabricate a flag or output.
- Keep failed_transcript and desired_transcript public-safe too: use placeholders for private identifiers and do not show real raw data. Do not invent sample payload contents or JSON keys; if row values were not observed, write "→ sample rows with payload values redacted" or use <json-key>.
- If the request involves a broad or potentially risky capability, frame the minimal safe capability needed using only what the transcript proves: scoped, read-only, limited, dry-run, or confirmation-gated as appropriate.
- Do NOT request project/account/workspace override flags unless the transcript proves that selecting the project/account/workspace is itself missing. If the failed command already uses a fully qualified resource identifier that includes the project/account/workspace, do not add a separate override merely because the resource is external or cross-project.
- Do not add separate requests for unsupported commands the driver guessed unless the real tool output falsely claimed success or hid the next action. Focus the request on the deepest missing capability needed to complete the goal.
- TRACE TO COMPLETION, not to the first friction. Identify the wall that actually blocks reaching the GOAL — it is often a step BEYOND where the agent stopped (e.g. the agent fixed the binding but still could not write the transform). Cover every blocking gap, not just the first.
- NAME THE DEEPEST MISSING CAPABILITY the agent would have needed — including ones it never reached. Ask: to finish, what did it have to know or do that the tool gave no way to (e.g. could it even inspect the data it must transform?).
- If the tool already exposed structured state (for example a resource location/region, resource ID, auth state, or next action), downstream commands should use it or provide a diagnostic that connects the dots. Requiring the agent to rediscover or manually reconcile known state is an AX defect.
- Only ask for more specific config/flag names when the transcript proves that one generic term is being used for different scopes and that ambiguity contributed to the failure. Otherwise keep the tested tool's existing domain term.
- A CLI should be understandable without generated docs, markdown guides, skills, or prose-only instructions. In scaffold/authoring workflows, generated docs and .md files are an AX anti-pattern by default because they become stale prose skills instead of live tool behavior. Agentic workflows should be driven by help text, structured JSON, contracts, next_actions, generated file TODO markers, and diagnostics.
- Do NOT request adding more instructions to generated docs or markdown guides as the fix. Do NOT request keeping generated docs/markdown as scaffold guidance "in addition to" structured output. Do NOT include human_docs, docs, or markdown artifacts in the desired scaffold output unless the user's goal explicitly asks to generate documentation. If generated docs or markdown appeared in the run, request moving that guidance into structured CLI outputs, generated implementation files, help, next_actions, and diagnostics.
- Do NOT request translating every upstream/provider error into a custom error. When an upstream/provider error appears in the failure, the request or desired_transcript must preserve the original provider error and add structured diagnostics only when the tool has deterministic context from config, discovery, or prior commands. Prefer fields like provider_error/raw_error, known_state, and affordances over hiding the original error.
- If the transcript includes a scaffold/generate/create-package command, assess scaffold AX even if a later command is the final blocker. The desired scaffold output should be structured state such as created_files, unresolved implementation items, verify command, and contract summary when the transcript proves those concepts. Do NOT ask for tutorial-style next_steps, and do NOT include docs/markdown outputs as a desired scaffold artifact.
- If a browse/discovery command or driver post-mortem reveals scoped state that the failing command did not use, failed_transcript must show both facts: the discovered state and the conflicting state used by the failing command.
- Every request item must include "change" and "why". The "why" must explain how that change improves the desired transcript or removes the observed AX failure, not just restate the change.
- Base everything ONLY on what the transcript shows — do not invent commands the run did not imply.
- If the report context includes hard review guardrails, treat them as mandatory constraints. Do not include a request or desired_transcript step that violates a guardrail.
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
	if hints := reviewHints(r); len(hints) > 0 {
		b.WriteString("\nhard review guardrails (must obey; derived from the transcript, not pre-written findings):\n")
		for _, h := range hints {
			fmt.Fprintf(&b, "- %s\n", h)
		}
	}
	return b.String()
}

func reviewHints(r report.Report) []string {
	body := strings.ToLower(r.PostMortem)
	for _, s := range r.Transcript {
		body += "\n" + strings.ToLower(s.Command) + "\n" + strings.ToLower(s.Result)
	}
	var hints []string
	// Keep review hints product-agnostic. AXprobe core must not know the tool
	// under test; manifests and run reports are the only source for product,
	// command, and domain vocabulary.
	if looksLikeGeneratedDocsDependency(body) {
		hints = append(hints, "The run used generated docs or markdown. Treat scaffold-generated docs as an AX anti-pattern unless documentation was the user's goal. Do not request keeping generated docs/markdown as scaffold guidance; request moving guidance into structured CLI outputs, generated files, next_actions, and diagnostics.")
	}
	if containsAny(body, "error", "failed", "not found", "provider") {
		hints = append(hints, "The run contains an upstream/provider-style error. Preserve the original error in desired output and add structured diagnostics only when deterministic context is available.")
	}
	if containsAny(body, " scaffold ", " generate ", " create-package ", " created ") {
		hints = append(hints, "The run involved scaffold/generation. The desired_transcript must include the scaffold/generation command and show machine-actionable output such as created_files, unresolved implementation items, verify command, and contract summary when those concepts appear in the transcript; do not request tutorial-style next_steps or docs/markdown artifacts.")
	}
	if containsAny(body, "location", "region", "zone", "scope") {
		hints = append(hints, "The run mentioned location/region/scope. Only request more specific naming if the transcript proves one generic term was used for different scopes and that ambiguity contributed to the failure.")
	}
	if containsFullyQualifiedResource(r) {
		hints = append(hints, "A failing command already used a fully qualified resource identifier. Forbidden unless explicitly proven by the transcript: requesting a separate project/account/workspace override merely because the resource is external or cross-project.")
	}
	return hints
}

func containsFullyQualifiedResource(r report.Report) bool {
	body := r.PostMortem
	for _, s := range r.Transcript {
		body += "\n" + s.Command
	}
	return fullyQualifiedResourceRE.MatchString(body)
}

var fullyQualifiedResourceRE = regexp.MustCompile("`?[A-Za-z][A-Za-z0-9_-]*\\.[A-Za-z_][A-Za-z0-9_-]*\\.[A-Za-z_][A-Za-z0-9_-]*`?")

func looksLikeGeneratedDocsDependency(body string) bool {
	return strings.Contains(body, ".md") || strings.Contains(body, "docs/")
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func extractJSON(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}
