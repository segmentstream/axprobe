---
name: axprobe-author
description: >-
  Author and review agentic-experience (AX) fixtures with axprobe — write
  user-level intents, keep harness plumbing out of the goal, set the AX bar, and
  turn a run into actionable product feedback. Use when creating an axprobe
  scenario/fixture or reviewing an AX report/finding.
---

# Authoring & reviewing AX fixtures with axprobe

axprobe drives a real CLI with a **deliberately simple** LLM agent inside a
disposable sandbox and reports on its **agentic experience**: did the agent reach
the goal, how few human steps did it take, where did it get stuck. The agent is
dumb on purpose — a clever harness would paper over the very UX defects you want
to surface. Your job as author is to give it an honest test, not to help it.

## The one rule: the goal is USER intent, never the tool's interface

The `goal` (and the `explore` intent it is synthesized from) must read like a
user describing what they want — **not** like instructions to the tool. If you
name the tool's own commands, flags, internal response states, or transport
mechanics, you stop testing whether the tool guides the agent and start
spoon-feeding it.

- ❌ "run `warehouse configure`, then `warehouse test` until it returns `ready:true`, using `--port 8085` over the OAuth loopback"
- ✅ "Connect my BigQuery to segmentstream using my Google account, into project X, a dataset called Y in the US region. Make sure it actually works — data can be read and written — not just that it's saved."

The agent must discover *how* (which commands, what "done" looks like) from the
tool itself. That discovery **is** the AX under test. Run `axprobe lint <scenario>`
(and heed the lint `explore` prints) — it flags leaked flags, states, jargon, and
command names.

## Where each value comes from

- **User-supplied values** (project, dataset, region): **pin them in the goal.**
  They are the user's own knowledge and keep the fixture deterministic. Asking for
  them at runtime would raise HIC and make the test non-reproducible.
- **Discoverable config** (which project exists, etc.): the tool should let the
  agent discover it (a `browse`/`list` command). If the agent has to ask a human
  for something the tool could surface post-auth, that is an AX defect (HIC↑).
- **Secrets / browser logins**: declared as `credentials` — this is **harness
  plumbing**, not agent-facing. `login_command`, `callback_port`, `token_paths`
  are tool-specific there and that is fine; the agent never sees them.

## Fixture anatomy

```yaml
goal: <user-level intent, pinned values, no tool interface>
credentials: [ ... ]          # plumbing: secrets / oauth login the harness runs
reset:   { secrets: true }    # auth fixture → cold every run (box resets in-box state for free)
expect:  { goal_reached: true, max_human_interventions: 1, max_false_errors: 0 }
```

- **`reset`** returns the fixture to a clean baseline *before* the run. The
  disposable box already resets in-box state; `secrets: true` purges the cached
  token so an auth fixture is exercised cold. (Cloud/SaaS teardown belongs here too
  once a fixture creates such state.)
- **`expect`** is the AX bar — the run exits non-zero if missed, so a fixture is an
  **executable spec**: write it red, implement until green, regressions turn it red.
- **Containment**: confine created resources to a namespace you can reset (e.g. a
  dataset prefix). Mutation-safety for an unattended fixture is **pre-authorized
  containment** (namespace + reset), not per-operation approval.

## Prefer `explore` to author

Don't hand-write YAML. Give `explore` a user-level intent; it drives the tool in
the sandbox and synthesizes a correct-by-construction manifest (plumbing
separated, schema valid), then lints the goal. Review the draft before committing.

## Watching a run

Launch in the background with a JSONL event stream and tail it — reliable to parse,
no grepping human output:

```
axprobe run <scenario> --workdir . --model <id> --events /tmp/run.jsonl >/tmp/run.log 2>&1 &
tail -f /tmp/run.jsonl | jq -c .
```

Event types: `login_url` (the human must approve the browser login NOW), `gate`,
`bash`, `observe`, `finish`, `outcome` (ends the run). The full human log and the
`report.json` remain for detail. Act on `login_url` immediately; stop on `outcome`.

## Reviewing a run → product feedback

The report quantifies AX: `human_interventions` (HIC), `false_errors`, `steps`,
`commands_run`, plus qualitative `observations`, each tagged with a category:

| Category | "…" | Look for |
|---|---|---|
| `missing_guidance` | wasn't told what to do | agent had to guess / gate |
| `confusion` | got confused | false errors, misleading output |
| `extra_steps` | 3 steps for 1 | redundant commands vs the minimal path |
| `friction` | inconvenient | awkward/heavy, high HIC |
| `unclear_interface` | confusing | command names, flags, output structure |

## AX-defect checklist (what to file)

- **Trace to completion, not to the first friction.** The wall that matters is the
  one that blocks the GOAL — often a step *beyond* where the agent stopped (it
  bound the source but still couldn't write the transform). Name the deepest
  missing capability it would have needed, including ones it never reached (could
  it even *inspect* the data it must transform?). Fixing only the first friction
  leaves the goal unreachable.
- Does the tool **guide** the agent (help text, structured next-actions), or did it
  have to guess? A tool drivable from `--help` alone scores high.
- **Self-sufficiency**: can the agent *complete* the goal using only the tool, or
  does it hit a wall it must leave the tool to cross (e.g. the tool can *reference*
  a dataset but not *create* one)? A capability gap is a defect — the fix is
  explicit self-service (create behind a flag/confirmation + a clear next-action),
  not pushing the step onto a human.
- **Honest state**: never report success for a state that is not real (e.g.
  `configure` calling a non-existent dataset "valid", then failing at `test`).
- **Non-zero exit on a normal state** (a "false error")?
- **More steps than necessary** to reach the goal?
- **Mutations without `--dry-run`/confirmation** — the agent can change real state
  with no guardrail. (Example finding: segmentstream-cli #19.)
- Did the agent have to **ask a human** for something the tool could have
  discovered itself?
- Does the tool **ship/embed its own agent guidance**? (An AX criterion in itself.)

## Decide vs ask the product owner

Apply these principles and **decide**; surface the decision + a one-line reason for
a quick veto, rather than asking permission. Ask the product owner only for:
(1) product priorities / what to build next, (2) irreversible external actions on
shared systems, (3) access or credentials only they hold, (4) a genuinely novel
situation no principle covers. Mechanical AX judgments are yours to make — that is
how the human gets asked fewer questions over time.

## Finding format

A finding is a **GitHub issue in the tool's repo, in English**, framed as
*Agentic UX*:

- **Title** — `Agentic UX: <the defect in one line>`
- **Summary** — one paragraph: what is wrong.
- **Observed** — the ACTUAL transcript, verbatim from the run: commands and the
  errors / next-actions they returned, through to where the agent got stuck. This
  is auto-fillable from the run report.
- **Why it matters (Agentic Experience)** — which principle(s) it breaks
  (self-sufficiency, honest-state, discover-don't-ask, …) and the impact (HIC,
  stuck, a misleading success signal).
- **Proposed flow (ideal)** — the IDEAL transcript: what the interaction should
  look like. (Judgment.)
- **Request** — concrete, numbered changes.
- **Context** — "Found via axprobe …".

## Filing is human-gated

Drafting a finding is automatic — `axprobe review [--model <id>] <report.json>`
renders a paste-ready draft (Observed transcript verbatim from the report; with
--model an LLM reviewer guided by this skill writes the why / ideal / request).
**Filing** it is an external, public action on a shared system, so it stays in the
"ask" bucket (decide-vs-ask category 2): show the product owner the FULL rendered
draft and get approval BEFORE filing — not just the intent or a summary. The owner
approves / edits / skips with one word. The harness never files issues itself; a
human publishes it. Relax to auto-filing only low-risk findings once findings
prove high-signal.

## Never coach the agent

If you tell the agent which commands to run (in the goal, the prompt, or a custom
driver), you hide the defect you are trying to measure. Keep the agent simple and
honest; let the friction show.
