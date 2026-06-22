# axprobe

A single-binary harness that drives a CLI tool inside a disposable sandbox with
an LLM and reports on its **Agentic Experience (AX)**: can an agent reach a goal,
how few human steps does it take, and where does it get stuck?

It is deliberately a *simple* agent. If a product can be driven by a minimal
harness running even a weak model, its experience is genuinely good — a clever
harness would paper over the very defects you want to surface.

## How it works

```
intent → drive a real CLI in a clean Docker box → AX report (outcome, friction, HIC, cost)
```

- A **scenario** (YAML) describes what to install and the goal — in plain language.
- The **driver** (an LLM via [OpenRouter](https://openrouter.ai)) plays an ordinary
  user: it runs commands, records friction it hits, and stops at genuine human
  gates (secrets, browser logins) instead of working around bad UX.
- You get a structured **AX report**: did it reach the goal, how many human
  interventions, what false errors / confusing moments, tokens and cost.

## Install

```sh
go install github.com/segmentstream/axprobe@latest   # needs Go 1.22+
```

Requires a running **Docker** daemon (Docker Desktop or Colima). Set an
[OpenRouter key](https://openrouter.ai/keys) via `OPENROUTER_API_KEY` (or a
gitignored `.env` in the working directory).

Optional driver/review defaults live outside scenarios so the same scenario can
run across a model matrix:

```yaml
# ~/.axprobe/config.yaml
driver_model: moonshotai/kimi-k2.6
review_model: anthropic/claude-opus-4.8
```

CI should usually set `AXPROBE_DRIVER_MODEL` or pass `--driver-model` explicitly.

## Quickstart

```sh
# scripted smoke test (no key needed) — proves the box plumbing
axprobe run testdata/smoke.yaml

# drive a real tool with an agent and get an AX report
axprobe run --driver-model moonshotai/kimi-k2.6 testdata/gh-device/.axprobe/gh-auth.yaml
```

## Scenarios: the `.axprobe/` convention

A repo under test keeps its scenarios in `.axprobe/`, split into two levels so the
install isn't repeated per test:

```
.axprobe/
  config.yaml        # WORKSPACE: how to install the tool (once)
  onboarding.yaml    # SCENARIO: the intent + the AX bar (expect)
```

```yaml
# .axprobe/onboarding.yaml
schema_version: "1"
name: onboarding
goal: Install the tool and complete first-run setup.
expect:                 # the AX bar — the run fails (non-zero exit) if missed
  goal_reached: true
  max_human_interventions: 1
```

- `axprobe run` (no arg) discovers and runs every `.axprobe/*.yaml`.
- `axprobe run <name>` runs `.axprobe/<name>.yaml`.

Because `expect` makes a run pass/fail, a scenario is an **executable spec**: write
it before the feature (red), implement until green, and a regression turns it red.

JSON Schemas: [`schema/config.schema.json`](schema/config.schema.json),
[`schema/manifest.schema.json`](schema/manifest.schema.json),
[`schema/report.schema.json`](schema/report.schema.json).

## Credentials & logins

When the agent hits a gate, a small broker provides the credential — the secret
never enters the model's context. Kinds: `file`, `value`, and `oauth` (browser
login; device-code or loopback). A successful oauth login is cached (OS Keychain)
so repeat runs need no browser. `--unattended` mode satisfies gates from a
cached/provisioned credential or ends `stopped_at_gate` — for CI.

See [`CLAUDE.md`](CLAUDE.md) for the architecture and design notes.

## License

[MIT](LICENSE).
