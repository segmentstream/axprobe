// Package driver is the Layer 1 LLM driver: it pursues a manifest's goal inside
// a box using a small tool set, and records friction in plain language. It is
// deliberately a *simple* agent — its honesty about getting stuck is the
// measurement, so it is told not to work around bad UX heroically.
package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/events"
	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/manifest"
)

// Gatekeeper resolves a human gate: it provides whatever the driver is blocked on
// and returns a resume message, or ("", false) if the run must stop. Implemented
// by the secret broker (run) and the discovery broker (explore).
type Gatekeeper interface {
	Resolve(needs string) (resume string, ok bool)
}

// bareLoginInterceptor is an optional Gatekeeper capability. When it returns
// true, the driver routes an apparent browser-login command the agent runs
// directly (not via gate) through the gatekeeper, instead of executing it and
// blocking forever. The discovery broker (explore) opts in because, unlike run,
// it has no credential declared in advance to match against.
type bareLoginInterceptor interface {
	InterceptsBareLogins() bool
}

const (
	maxSteps         = 30
	maxToolOutput    = 4000 // chars of stdout/stderr fed back to the model
	onelinePreviewer = 200
)

// Observation is one qualitative AX finding, tagged with a product-owner-defined
// category so a run becomes actionable app-improvement feedback.
type Observation struct {
	Category   string `json:"category"`
	Note       string `json:"note"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ObservationCategories is the AX feedback taxonomy (defined with the product
// owner). Order is the order shown in the report tally.
var ObservationCategories = []string{
	"missing_guidance",  // the tool didn't say what to do; the agent had to guess
	"confusion",         // confusing or false error, misleading output
	"extra_steps",       // it took more steps than it should
	"friction",          // it worked but was inconvenient/awkward
	"unclear_interface", // confusing command names, flags, or output structure
}

// normalizeCategory maps a model-supplied category onto the taxonomy, defaulting
// to "friction" (the generic "this was worse than it should be") when unknown.
func normalizeCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	for _, k := range ObservationCategories {
		if c == k {
			return c
		}
	}
	return "friction"
}

// FalseError is a non-zero exit that does not look like a real failure — the tool
// reported normal state / a next action, e.g. init exiting 13 on a not-ready state.
type FalseError struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Reason   string `json:"reason"`
}

// Step is one command in the run transcript — the ACTUAL evidence a finding is
// built from (command → result), recorded verbatim.
type Step struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Result   string `json:"result"` // one-line summary of the command's output
}

// Result holds the approved v0 telemetry for a driven run.
type Result struct {
	DriverModel   string
	Outcome       string // goal_reached | stopped_at_gate | stuck | error
	GoalReached   bool
	Reached       bool // raw flag from finish(); GoalReached is the reconciled value
	StoppedAtGate bool // true only if the run ENDED at an unresolved gate
	Summary       string
	Gates         []string // gate_details; human_interventions (HIC) = len(Gates)
	Steps         int
	CommandsRun   int
	Commands      []string // commands the agent ran (tool-vocabulary source for the goal lint)
	Transcript    []Step   // command → result, the ACTUAL evidence for findings
	DurationSec   float64
	Observations  []Observation
	FalseErrors   []FalseError
	PostMortem    string // driver's end-of-run reflection (grounded in the real interface it just exercised)
	Tokens        llm.Usage
}

// finalize derives the headline outcome once the run ends. A gate that the
// broker resolved (and the run continued past) does not count as stopped.
func (r *Result) finalize() {
	switch {
	case r.StoppedAtGate:
		r.Outcome = "stopped_at_gate"
		r.GoalReached = false
	case r.Reached:
		r.Outcome = "goal_reached"
		r.GoalReached = true
	default:
		r.Outcome = "stuck"
		r.GoalReached = false
	}
	events.Emit("outcome", "outcome", r.Outcome, "goal_reached", r.GoalReached)
}

// Driver couples a box, a manifest goal, a driver model and a gatekeeper.
type Driver struct {
	box  box.Box
	m    *manifest.Manifest
	llm  *llm.Client
	gate Gatekeeper
}

// New builds a driver. gate may be nil (gates then always stop the run).
func New(b box.Box, m *manifest.Manifest, client *llm.Client, gk Gatekeeper) *Driver {
	return &Driver{box: b, m: m, llm: client, gate: gk}
}

// Run drives the box toward the goal until the model calls finish/gate or the
// step budget runs out.
func (d *Driver) Run(ctx context.Context) (*Result, error) {
	res := &Result{DriverModel: d.llm.Model}
	start := time.Now()
	defer func() { res.DurationSec = time.Since(start).Seconds() }()

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: d.goalPrompt()},
	}

	finished := false
	for step := 1; step <= maxSteps && !finished; step++ {
		res.Steps = step
		// Wander nudge: two-thirds through the budget with no observation, gate, or
		// finish, the agent is likely flailing (poking around without progress).
		// Prompt it to reflect and stop instead of burning the rest of the budget.
		if step == maxSteps*2/3 && len(res.Observations) == 0 && len(res.Gates) == 0 {
			messages = append(messages, llm.Message{
				Role: "user",
				Content: "You have taken many steps without recording a finding, gating, or finishing. " +
					"If you are stuck or unsure how to proceed, call observe() to state exactly what is " +
					"missing or unclear about the tool, then finish(reached=false). Do not keep exploring.",
			})
		}
		msg, usage, err := d.llm.Chat(ctx, messages, toolDefs())
		if err != nil {
			res.Outcome = "error"
			return res, err
		}
		res.Tokens.PromptTokens += usage.PromptTokens
		res.Tokens.CompletionTokens += usage.CompletionTokens
		res.Tokens.TotalTokens += usage.TotalTokens
		res.Tokens.Cost += usage.Cost
		messages = append(messages, msg)

		if len(msg.ToolCalls) == 0 {
			if c := strings.TrimSpace(msg.Content); c != "" {
				fmt.Printf("  · %s\n", oneline(c))
			}
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "Use one of the tools (bash / observe / gate / finish) to proceed.",
			})
			continue
		}

		for _, tc := range msg.ToolCalls {
			out, done := d.dispatch(tc, res)
			messages = append(messages, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    out,
			})
			if done {
				finished = true
				break
			}
		}
	}

	if !finished {
		if res.Summary == "" {
			res.Summary = fmt.Sprintf("Stopped after %d steps without finishing.", maxSteps)
		}
		// A run that exhausts the whole budget without recording anything is itself a
		// finding — make sure the report carries a signal instead of zero observations.
		if len(res.Observations) == 0 {
			res.Observations = append(res.Observations, Observation{
				Category: "missing_guidance",
				Note: fmt.Sprintf("Ran the full %d-step budget without reaching the goal, gating, or recording a finding — "+
					"the tool gave no clear path forward.", maxSteps),
			})
		}
	}

	// Post-mortem: with the tool still fresh in context, have the driver reflect on
	// its own experience — grounded in the real interface it just exercised. This
	// lived testimony (not a cold reader's guess) is what the review agent builds
	// the finding's ideal flow from.
	res.PostMortem = d.postMortem(ctx, messages, res)

	res.finalize()
	return res, nil
}

// postMortem asks the driver, with its full run history still in context, to
// reflect honestly: what it tried, what was unclear, and the ideal command
// sequence grounded in the tool's REAL interface (marking anything the tool does
// not yet offer as PROPOSED). The history already holds the real commands and
// outputs, so the reflection is grounded by construction.
func (d *Driver) postMortem(ctx context.Context, history []llm.Message, res *Result) string {
	prompt := "The run is over. Write a short post-mortem of YOUR experience driving this tool — not a list of commands, but what it was like:\n" +
		"- what you were trying to do and what you actually did;\n" +
		"- what was confusing, missing, or forced you to guess;\n" +
		"- where (if anywhere) you needed a human;\n" +
		"- the IDEAL sequence of commands that should have reached the goal, grounded in the tool's REAL interface as you observed it — use the actual command and flag names you saw, each as `# why` then `$ command` then `→ result`. Mark any step the tool does NOT yet offer with `# PROPOSED`.\n" +
		"Be concrete and honest. If the tool worked well, say so briefly."
	msgs := append(history, llm.Message{Role: "user", Content: prompt})
	msg, usage, err := d.llm.Chat(ctx, msgs, nil)
	res.Tokens.PromptTokens += usage.PromptTokens
	res.Tokens.CompletionTokens += usage.CompletionTokens
	res.Tokens.TotalTokens += usage.TotalTokens
	res.Tokens.Cost += usage.Cost
	if err != nil {
		return ""
	}
	return strings.TrimSpace(msg.Content)
}

func (d *Driver) goalPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", d.m.Goal)
	if d.m.StopWhen != "" {
		fmt.Fprintf(&b, "Stop condition: %s\n", d.m.StopWhen)
	}
	b.WriteString("The tool under test is already installed in the sandbox. Begin.")
	return b.String()
}

// dispatch executes one tool call and reports whether the run should end.
func (d *Driver) dispatch(tc llm.ToolCall, res *Result) (output string, done bool) {
	var args map[string]any
	_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
	str := func(k string) string { s, _ := args[k].(string); return s }

	switch tc.Function.Name {
	case "bash":
		cmd := str("command")

		// Deterministic safeguard: if the agent runs a declared oauth login
		// command directly (instead of gating), route it through the gatekeeper's
		// oauth resolver rather than letting the blocking command hang the run.
		if login, isLogin := declaredLogin(d.m, cmd); isLogin && !isHelpInvocation(cmd) && d.gate != nil {
			fmt.Printf("▸ bash:    %s\n", cmd)
			fmt.Println("   ↪ interactive login — handled by the harness")
			events.Emit("login_intercept", "credential", login.Name)
			res.Gates = append(res.Gates, "browser login: "+login.Name)
			if msg, ok := d.gate.Resolve("a browser login is required for " + login.Name); ok {
				fmt.Printf("✓ resumed: %s\n", oneline(msg))
				return msg, false
			}
			res.StoppedAtGate = true
			return "stopping: the interactive login could not be completed", true
		}

		// Explore has no credential declared in advance, so a login the agent runs
		// directly (e.g. `… auth login`) would block forever. If the gatekeeper
		// opts into intercepting bare logins, route it through the oauth resolver.
		if bi, ok := d.gate.(bareLoginInterceptor); ok && bi.InterceptsBareLogins() &&
			!isHelpInvocation(cmd) && looksLikeBrowserLogin(cmd) {
			fmt.Printf("▸ bash:    %s\n", cmd)
			fmt.Println("   ↪ interactive login — handled by the harness")
			res.Gates = append(res.Gates, "browser login (discovered)")
			if msg, ok := d.gate.Resolve("The agent attempted a browser login by running: " + cmd); ok {
				fmt.Printf("✓ resumed: %s\n", oneline(msg))
				return msg, false
			}
			res.StoppedAtGate = true
			return "stopping: the interactive login could not be completed", true
		}

		fmt.Printf("▸ bash:    %s\n", cmd)
		r, err := d.box.Exec(cmd)
		if err != nil {
			return fmt.Sprintf("box error: %v", err), false
		}
		res.CommandsRun++
		res.Commands = append(res.Commands, cmd)
		line := resultLine(r)
		res.Transcript = append(res.Transcript, Step{Command: cmd, ExitCode: r.ExitCode, Result: line})
		events.Emit("bash", "cmd", cmd, "exit", r.ExitCode, "result", line)
		printResult(r)
		if looksLikeFalseError(r) {
			res.FalseErrors = append(res.FalseErrors, FalseError{
				Command:  cmd,
				ExitCode: r.ExitCode,
				Reason:   "non-zero exit but output describes normal state/next action",
			})
		}

		// Stuck guard: the SAME command returning the SAME result three times means
		// the agent is looping with no path forward (re-running a verify command
		// whose result changes is fine — that is progress). Stop and record a
		// missing-guidance finding instead of burning the step budget.
		if repeatedNoProgress(res.Transcript, cmd, line) >= 3 {
			res.Observations = append(res.Observations, Observation{
				Category: "missing_guidance",
				Note:     fmt.Sprintf("Ran %q repeatedly with identical output and no way forward — the tool gave no next-action to get unstuck.", oneline(cmd)),
			})
			res.Summary = "Stuck: repeated a command with identical output and no path forward."
			fmt.Println("⨯ stuck:   command repeated with no progress — stopping")
			return "Stopping: repeating this command yields the same result and makes no progress.", true
		}
		return fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s",
			r.ExitCode, truncate(r.Stdout), truncate(r.Stderr)), false

	case "observe":
		o := Observation{Category: normalizeCategory(str("category")), Note: str("note"), Suggestion: str("suggestion")}
		res.Observations = append(res.Observations, o)
		fmt.Printf("⚑ observe [%s]: %s\n", o.Category, oneline(o.Note))
		events.Emit("observe", "category", o.Category, "note", oneline(o.Note))
		return "recorded", false

	case "gate":
		needs := str("needs")
		res.Gates = append(res.Gates, needs)
		fmt.Printf("⏸ gate:    needs %s\n", oneline(needs))
		events.Emit("gate", "needs", oneline(needs))
		if d.gate != nil {
			if msg, ok := d.gate.Resolve(needs); ok {
				fmt.Printf("✓ resumed: %s\n", oneline(msg))
				return msg, false // gate satisfied — the run continues
			}
		}
		res.StoppedAtGate = true
		return "stopping at human gate (no credential available)", true

	case "finish":
		reached, _ := args["reached"].(bool)
		res.Reached = reached
		res.Summary = str("summary")
		fmt.Printf("■ finish:  reached=%v\n", reached)
		events.Emit("finish", "reached", reached)
		return "done", true

	default:
		return fmt.Sprintf("unknown tool %q", tc.Function.Name), false
	}
}

// resultLine summarizes a command's output as its first non-empty line, for the
// transcript and the stuck-guard's progress comparison.
func resultLine(r box.ExecResult) string {
	for _, stream := range []string{r.Stdout, r.Stderr} {
		for _, ln := range strings.Split(stream, "\n") {
			if t := strings.TrimSpace(ln); t != "" {
				return oneline(t)
			}
		}
	}
	return ""
}

// repeatedNoProgress counts transcript steps with the same (normalized) command
// AND the same result — identical re-runs that made no progress.
func repeatedNoProgress(ts []Step, cmd, result string) int {
	want := normCommand(cmd)
	n := 0
	for _, s := range ts {
		if normCommand(s.Command) == want && s.Result == result {
			n++
		}
	}
	return n
}

func normCommand(s string) string { return strings.Join(strings.Fields(s), " ") }

func printResult(r box.ExecResult) {
	for _, stream := range []string{r.Stdout, r.Stderr} {
		s := strings.TrimRight(stream, "\n")
		if s == "" {
			continue
		}
		for _, line := range strings.Split(s, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}
	fmt.Printf("  └─ exit %d\n", r.ExitCode)
}

// declaredLogin reports whether cmd invokes a declared oauth credential's
// login_command (matched on the first few tokens, so flags don't matter).
func declaredLogin(m *manifest.Manifest, cmd string) (manifest.Credential, bool) {
	for _, c := range m.Credentials {
		if c.Kind == "oauth" && c.LoginCommand != "" && sameBaseCommand(cmd, c.LoginCommand) {
			return c, true
		}
	}
	return manifest.Credential{}, false
}

// looksLikeBrowserLogin reports whether cmd appears to start an interactive
// browser/oauth login — e.g. "gh auth login", "gcloud auth login", "segmentstream
// warehouse auth login". Used only by the explore interception path, where no
// credential is declared in advance to match against.
func looksLikeBrowserLogin(cmd string) bool {
	for _, f := range strings.Fields(cmd) {
		if f == "login" {
			return true
		}
	}
	return false
}

// isHelpInvocation reports whether cmd is a help/version invocation, which must
// not trigger the login interception — it should print help, not authenticate.
func isHelpInvocation(cmd string) bool {
	for _, f := range strings.Fields(cmd) {
		switch f {
		case "--help", "-h", "--version":
			return true
		}
	}
	return false
}

// sameBaseCommand compares the first up-to-3 whitespace tokens of two commands
// (e.g. "gh auth login" matches "gh auth login --hostname … --web"). The first
// token — the binary — is compared by basename, so an agent invoking the tool by
// absolute path (/root/.../bin/segmentstream …) still matches a declared
// "segmentstream …" login command.
func sameBaseCommand(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	if len(fa) > 0 {
		fa[0] = path.Base(fa[0])
	}
	if len(fb) > 0 {
		fb[0] = path.Base(fb[0])
	}
	n := 3
	if len(fa) < n {
		n = len(fa)
	}
	if len(fb) < n {
		n = len(fb)
	}
	if n == 0 {
		return false
	}
	for i := 0; i < n; i++ {
		if fa[i] != fb[i] {
			return false
		}
	}
	return true
}

// looksLikeFalseError flags a non-zero exit that does not look like a real
// failure: the command still printed normal-looking state/output and none of the
// usual failure markers. This is the heuristic half of false_errors; the driver
// may also flag confusing exits via observe().
func looksLikeFalseError(r box.ExecResult) bool {
	if r.ExitCode == 0 {
		return false
	}
	out := strings.ToLower(r.Stdout + "\n" + r.Stderr)
	for _, marker := range []string{
		"error", "failed", "fatal", "panic", "exception",
		"not found", "no such file", "permission denied",
		"command not found", "cannot ", "unable to", "usage:",
	} {
		if strings.Contains(out, marker) {
			return false
		}
	}
	// Non-zero exit but stdout carries normal content → a state signal, not a failure.
	return strings.TrimSpace(r.Stdout) != ""
}

func truncate(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + "\n…(truncated)"
}

func oneline(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > onelinePreviewer {
		return s[:onelinePreviewer] + "…"
	}
	return s
}
