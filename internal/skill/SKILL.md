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

AXprobe is manifest-agnostic: it tests whichever tool the manifest installs and
exposes. Do not assume product, vendor, backend, or domain behavior that is not
present in the manifest, transcript, or run report.

The `goal` (and the `explore` intent it is synthesized from) must read like a
user describing what they want — **not** like instructions to the tool. If you
name the tool's own commands, flags, internal response states, or transport
mechanics, you stop testing whether the tool guides the agent and start
spoon-feeding it.

- ❌ "run `tool configure`, then `tool test` until it returns `ready:true`, using `--port 8085` over the OAuth loopback"
- ✅ "Connect the tool to my account, use the project and storage location I provided, and make sure the connection actually works — data can be read and written — not just that settings were saved."

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

## Credentials: authorize once, reuse warm

An auth fixture declares `credentials` (with a `login_command` and `token_paths`).
Don't picture re-authorizing every run — and don't write the fixture as if you
must:

- The **first** run performs the real browser login. That is a genuine human gate
  (a human intervention — HIC counts it). axprobe then **caches** the resulting
  token files in the macOS Keychain, keyed by the login command's base, so related
  fixtures share the one authorization.
- **Subsequent** runs **restore** those cached tokens — warm, no login, HIC drops
  to 0. One manual authorization is reused across every later run.
- `reset: {secrets: true}` (or `--reset`) **purges** the cached token to force a
  **cold** run — re-doing the login — e.g. to exercise the from-scratch auth path
  on purpose.
- The agent never sees the token; this is harness plumbing.

So: authorize once, then runs are warm; reset only when you want to test cold.

## Fixture anatomy

```yaml
goal: <user-level intent, pinned values, no tool interface>
credentials: [ ... ]          # plumbing: secrets / oauth login the harness runs
reset:   { secrets: true }    # BEFORE the run: auth fixture → cold every run (box resets in-box state for free)
teardown: { run: [ "mytool resource destroy probe --yes" ] }   # AFTER the run: dispose external resources it created
expect:  { goal_reached: true, max_human_interventions: 1, max_false_errors: 0 }
```

- **`reset`** returns the fixture to a clean baseline *before* the run. The
  disposable box already resets in-box state; `secrets: true` purges the cached
  token so an auth fixture is exercised cold. It clears **axprobe's own footprint**
  (its secret cache, its declared output paths) — host-side, typed, never arbitrary
  shell.
- **`teardown`** disposes the **tool's footprint in the real world** *after* the run
  — cloud resources that outlive the disposable box (a Turso DB, a GitHub repo, a
  deploy). Its `run` commands execute IN-BOX (same warm creds as the run), in the
  box's defer path, so they fire on success, failure, and crash alike — no orphans.
  Declare ONE symmetric tool command that cascades (`mytool agent destroy probe`),
  not a pile of raw provider calls. This is the opposite phase/domain from `reset`:
  reset = before / axprobe's state; teardown = after / the tool's external state.
- **`expect`** is the AX bar — the run exits non-zero if missed, so a fixture is an
  **executable spec**: write it red, implement until green, regressions turn it red.
- **Containment**: a fixture that creates real resources must declare how to undo
  them. Confine them to a namespace (e.g. a `probe-` prefix) AND declare `teardown`
  to destroy them. Mutation-safety for an unattended fixture is **pre-authorized
  containment** (namespace + teardown), not per-operation approval.

## Prefer `explore` to author

Don't hand-write YAML. Give `explore` a user-level intent; it drives the tool in
the sandbox and synthesizes a correct-by-construction manifest (plumbing
separated, schema valid), then lints the goal. Review the draft before committing.

## Development loop

A fixture is an executable spec, so it belongs in your edit loop. After you change
the tool under test (a command, a flag, help text, output, an error), close the
loop on AX before you move on:

1. **Regression** — `axprobe run <scenario>` for the scenarios that exercise the
   surface you touched. They re-drive the agent against the known goal and turn red
   if the change made the tool harder to operate. This catches *regressions on paths
   you already chose to measure*.
2. **Discovery** — if you changed the **interface** (renamed a command, reshaped a
   flag/output, altered the guidance the agent reads), `axprobe explore "<fresh user
   intent>"` drives a brand-new plain-language intent through the box and synthesizes
   a scenario. This surfaces *new* friction your existing fixtures can't see, because
   they were written against the old interface and ask only their old questions.
3. **Red → fix** — a red run or fresh friction is a finding: file it (human-gated) or
   fix the tool, then re-run until green.

**run vs explore** — `run` is regression on a *known* scenario (did AX hold?);
`explore` is discovery of *new* friction after an interface change (what does the
new surface trip on?). Run alone tells you the old goals still pass; it cannot tell
you the rename you just shipped confuses a first-time agent. After an interface
change, do both.

## Watching a run

Launch in the background with a JSONL event stream and tail it — reliable to parse,
no grepping human output:

```
axprobe run <scenario> --workdir . --driver-model <id> --events /tmp/run.jsonl >/tmp/run.log 2>&1 &
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
  with no guardrail.
- Did the agent have to **ask a human** for something the tool could have
  discovered itself?
- Does the tool **ship/embed its own agent guidance**? (An AX criterion in itself.)
- **Preserve upstream errors, add context.** Do not ask tools to translate every
  provider error; prefer raw/provider error plus known state and recovery
  affordances when the tool has enough deterministic context. If an upstream
  error appears in the failure, the desired output should preserve it rather than
  hide it behind a custom message.
- **Scaffold returns machine-actionable state.** A scaffold/generate/create-package
  response should report created files, unresolved implementation surfaces,
  verification command, and contract summary when applicable. Avoid tutorial-style
  `next_steps` and generated docs/markdown as the agent interface; do not request
  keeping generated docs "in addition to" structured output unless documentation
  itself was the user's goal.
- **Do not invent unproven fixes.** Ask only for missing capabilities proven by
  the transcript. Do not ask for project/account/workspace override flags when the
  failing command already identifies the external resource fully; ask for the
  setting that actually blocked the operation.
- Does the tool expose structured state and then fail to use it downstream? If a
  command discovers a resource location, ID, auth state, or next action, later
  commands should consume it or return diagnostics that connect the dots.
- Do config names match the domain model when ambiguity is proven? Ask for more
  specific names only when the transcript shows one generic term being used for
  different scopes and that ambiguity contributes to the failure.

## Decide vs ask the product owner

Apply these principles and **decide**; surface the decision + a one-line reason for
a quick veto, rather than asking permission. Ask the product owner only for:
(1) product priorities / what to build next, (2) irreversible external actions on
shared systems, (3) access or credentials only they hold, (4) a genuinely novel
situation no principle covers. Mechanical AX judgments are yours to make — that is
how the human gets asked fewer questions over time.

## Finding format

A finding is a **public-safe GitHub issue draft in the tool's repo, in English**,
framed with an AXprobe issue-list prefix:

- **Title** — `[AXprobe] <the defect in one line>`
- **Summary** — one paragraph: what is wrong.
- **What Happened** — the narrative of the run before requests:
  - **Goal** — what the agent was trying to accomplish.
  - **Agent path** — the chronological path it took, including useful discoveries,
    detours, and the major tool surfaces it used. This is not a feature-request
    list.
  - **What worked** — useful affordances the run proved: structured JSON,
    state-machine next actions, discovery/browse/query commands, clear created-file
    output, good diagnostics, etc.
  - **Where it stopped** — the final wall that left the goal incomplete, not just
    the first friction.
  - **What would have helped** — the missing command, diagnostic, structured state,
    or preflight that would have let the agent continue.
  Keep the whole section public-safe: use placeholders for private identifiers
  (`<source-name>`, `<external-resource>`, `<local-path>`, `<credential-file>`,
  etc.). Do **not** include manifest paths, local paths, report paths, project IDs,
  dataset/table names, credential paths, raw payload values, tokens, or secrets.
- **Attempt Transcript** — a chronological public-safe transcript of the path the
  agent took. This is not a tiny failure excerpt: include the important
  command/result steps from discovery through the final wall, including useful
  successful steps, detours, diagnostics, scaffold/generate actions, and the point
  where progress stopped. Prefer 8-15 concise command/result pairs; summarize long
  JSON to the fields that mattered.
- **Failed Transcript** — a short public-safe transcript excerpt proving the wall:
  the command/output that failed, plus the immediately preceding command if it
  revealed state the failing command should have used.
- **Why it matters (Agentic Experience)** — which principle(s) it breaks
  (self-sufficiency, honest-state, discover-don't-ask, …) and the impact (HIC,
  stuck, a misleading success signal).
- **Desired Transcript** — a CONCRETE tool-call transcript, not prose: each step
  is a `# why` comment, the `$ command`, and the `→ result` it would return (show
  real command/flag names; if a step edits a file, show the key lines). The reader
  must see exactly what is called and why. **Ground it in reality:** build from the
  run's `post_mortem` (the driver saw the real interface) or commands the transcript
  actually shows; mark any not-yet-existing capability `# PROPOSED`; never fabricate
  a flag or output. Keep this public-safe too: placeholders instead of private
  identifiers and no raw data. (Judgment.)
- **Request** — concrete, numbered changes. Each item should include the change
  and a short rationale explaining how it improves the desired transcript or
  removes the observed AX failure. Do not just restate the change.
- **Attribution footer** — generated by the renderer:
  `Reported from an AXprobe agentic experience review.`

Keep private diagnostics separate from the public issue. Internal run provenance
(manifest path, report path, exact transcript, local usernames, specific project
identifiers) can help the operator debug, but should not be copied into issues
filed in third-party or open-source repositories.

## Filing is human-gated

Drafting a finding is automatic — `axprobe review [--review-model <id>] <report.json>`
renders a paste-ready public issue draft (with --review-model an LLM reviewer
guided by this skill writes the sanitized observed section, failed transcript,
why, desired transcript, and request rationale).
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
