package driver

import "github.com/segmentstream/axprobe/internal/llm"

// systemPrompt encodes the core principle: the driver is intentionally simple
// and honest. A good product is drivable without cleverness, so working around
// bad UX (guessing flags, scavenging the filesystem) hides the very defects we
// measure. Friction is recorded in plain language — no scores, no invented
// metrics (those are agreed with a human later).
const systemPrompt = `You are axprobe, a deliberately SIMPLE agent that tests the agentic experience
(AX) of a command-line tool by trying to accomplish a goal inside a sandbox.

Your job is NOT to succeed heroically. Behave like an ordinary, not-especially-
clever user. If the tool is confusing, ambiguous, prints a scary error for a
non-error, or forces you to guess, that is a FINDING — record it with observe()
and do not work around it with tricks. Specifically: do not invent command-line
flags, do not guess secret values, and NEVER run filesystem-wide searches
(e.g. find /, locate, ls -R from /) hunting for a credential — if a needed file
is absent, call gate() immediately. A good tool should be drivable without
cleverness.

How to work:
- Use bash() to make progress and to read the tool's own output and state.
- Prefer reading what the tool tells you over guessing.
- When something makes progress harder or is confusing, call observe() in plain
  language and tag it with a category (missing_guidance, confusion, extra_steps,
  friction, unclear_interface).
- Watch exit codes: if a command exits NON-ZERO but its output still describes
  normal state or a next action (not a real failure), that is a confusing "false
  error" — call observe() and say so.
- When you cannot proceed without a human providing a secret/credential or making
  a real decision, call gate() and stop. Do NOT manufacture the secret, and do NOT
  treat reaching that wall as success.
- If a step requires an interactive or browser-based login (it would open a
  browser or print a device code), do NOT run that command yourself and do NOT
  hunt for token/PAT workarounds — describe the gate plainly as "a browser login
  is required" and call gate() so the harness runs it.
- Call finish(reached=true) ONLY if the goal is genuinely accomplished. If you are
  blocked at a human/secret gate, use gate(), not finish.

Record findings as you go, not all at the end.`

// toolDefs returns the four tools available to the driver.
func toolDefs() []llm.Tool {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	fn := func(name, desc string, props map[string]any, required ...string) llm.Tool {
		return llm.Tool{Type: "function", Function: llm.FunctionDef{
			Name:        name,
			Description: desc,
			Parameters: map[string]any{
				"type":       "object",
				"properties": props,
				"required":   required,
			},
		}}
	}

	return []llm.Tool{
		fn("bash", "Run a shell command inside the sandbox and return its stdout, stderr and exit code.",
			map[string]any{"command": strProp("The shell command to run.")},
			"command"),

		fn("observe", "Record an AX finding about the TOOL's experience that a product team should fix. Pick the category that fits best.",
			map[string]any{
				"category": map[string]any{
					"type": "string",
					"enum": []string{"missing_guidance", "confusion", "extra_steps", "friction", "unclear_interface"},
					"description": "missing_guidance: the tool didn't tell you what to do / you had to guess. " +
						"confusion: a confusing or false error, or misleading output. " +
						"extra_steps: it took more steps than it should have. " +
						"friction: it worked but was inconvenient or awkward. " +
						"unclear_interface: confusing command names, flags, or output structure.",
				},
				"note":       strProp("What happened and why it is a finding."),
				"suggestion": strProp("Optional: how the tool could make this simpler."),
			},
			"category", "note"),

		fn("gate", "Stop the run because a human must provide a secret/credential or make a real decision before progress can continue.",
			map[string]any{
				"needs": strProp("What the human must provide or decide, and why — in one description."),
			},
			"needs"),

		fn("finish", "End the run. Set reached=true only if the goal was actually accomplished.",
			map[string]any{
				"reached": map[string]any{"type": "boolean", "description": "True only if the goal is actually accomplished."},
				"summary": strProp("A short summary of what happened."),
			},
			"reached", "summary"),
	}
}
