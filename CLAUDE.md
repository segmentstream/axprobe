# axprobe — repo guide

Single-binary Go harness that drives a CLI tool inside a disposable box and
reports on its **Agentic Experience (AX)**: how few human steps it takes an agent
to reach a goal, and where it gets stuck.

Design tenet: the harness is **deliberately simple**. If a product can be driven
by a minimal harness running even a weak model, its experience is genuinely good.
A clever harness would paper over the defects we want to surface.

## Layout

| Path | Role |
|------|------|
| `main.go` | CLI entry + run orchestration (scripted vs LLM driver) + AX report printing |
| `internal/manifest` | Parses `test.yaml`. All product-specifics live here; the harness knows nothing about any specific tool. |
| `internal/box` | `Box` interface + `LocalDockerBox`. Disposable, from-scratch environment. |
| `internal/driver` | LLM driver: agent loop, tool definitions, AX-rubric system prompt. |
| `internal/llm` | Minimal OpenRouter client (OpenAI-compatible chat + tool calling). |
| `internal/dotenv` | Loads a gitignored `.env` (e.g. `OPENROUTER_API_KEY`) at startup. |
| `testdata/` | per-tool scenario fixtures (e.g. `gh-device/`, `segmentstream/`). |

## CLI

```
axprobe run <manifest.yaml>                          # drive the LLM agent at the goal
axprobe run --driver-model <openrouter-id> <m.yaml>  # pick the driver model explicitly
axprobe probe "<cmd>" ["<cmd>"...]                   # run command(s) in a clean box, no LLM
```

- `run` always drives an LLM agent: it needs a driver model (`--driver-model`,
  `AXPROBE_DRIVER_MODEL`, or a configured default) and `OPENROUTER_API_KEY`
  (from `.env`, env, or the Keychain via `axprobe key set`).
- The key is never passed on the command line or written into a manifest.

## The `.axprobe/` convention (two levels)

Test definitions are a **public, versioned interface**, authored in target repos.
They split into two levels so that *how to install the product* (a property of the
product) is not repeated in every test:

```
.axprobe/
  config.yaml             # WORKSPACE: how to install/run the product (once)
  <scenario>.yaml         # SCENARIO: just the test (intent/goal, checks, creds)
  <scenario2>.yaml        # …more scenarios, same install inherited
```

- **Workspace** [`config.yaml`](schema/config.schema.json): `box` (image + install),
  plus any credentials shared across scenarios. Authored once per target.
- **Scenario** [`<name>.yaml`](schema/manifest.schema.json): the test. Inherits
  `box` from `config.yaml` (no `box` of its own) and may add/override credentials.
  A scenario may still define its own `box` to be fully self-contained.

Both schemas are strict (`additionalProperties: false`) and require `schema_version`.

**Discovery / run:**
- `axprobe run` (no arg) → runs every `.axprobe/*.yaml` scenario (skips `config.yaml`).
- `axprobe run <name>` → runs `.axprobe/<name>.yaml`.
- `axprobe run path/to.yaml` → an explicit path (separate manifest repo / self-test).

```yaml
# .axprobe/config.yaml (workspace)
schema_version: "1"
# Optional repo-level model defaults; flags/env override them.
defaults:
  driver_model: moonshotai/kimi-k2.6
  review_model: anthropic/claude-opus-4.8
box:
  image: ubuntu:24.04
  copy:  [<host>:<box>, ...]  # inject host files before setup (mode preserved); ship a prebuilt binary without --workdir
  setup: [<string>, ...]   # install the tool under test; a failing step aborts

# .axprobe/<scenario>.yaml (scenario — box inherited from config.yaml)
schema_version: "1"
name: <string>
intent: <string>           # plain-language source; `explore` derives the rest
goal: <string>             # what the driver pursues (often derived from intent)
stop_when: <string>
success_check: <string>
credentials:               # Layer 3 secret broker (scenario-specific)
  - name: <string>
    kind: file | value
    prompt: <string>
    inject: { box_path: <string> }   # or { env: <string> }
```

New tool to test (CLI today, MCP later) = a new `.axprobe/` workspace, not new
harness code.

## Box contract (`internal/box`)

`Box` is the swappable environment interface; everything above it is box-agnostic.

```go
type Box interface {
    Up() error                       // provision a fresh, clean environment
    Exec(cmd string) (ExecResult, error) // run `sh -lc <cmd>` inside; non-zero exit is data, not error
    CopyIn(content []byte, dest string) error // write a file inside the box (used by the broker)
    Down() error                     // tear down; idempotent
}

type ExecResult struct { Cmd string; ExitCode int; Stdout, Stderr string }
```

- Today: `LocalDockerBox` (`docker run -d <image> sleep infinity`, `docker exec`, `docker rm -f`).
- Planned: `GithubRunnerBox` (the CI runner is itself the box), `VMBox` (native Docker
  Compose, no nesting) — for the full run-to-report scenario.
- Commands run in a **login shell** (`sh -lc`), so `/etc/profile.d/*.sh` is sourced
  (that is how the tool under test gets onto `PATH`).

## Harness tools (the driver's tool set) — `internal/driver`

> **Status: v0.**
> Tool names and interfaces are a deliberate design choice.
> Revisions in this round: `run` → `bash` (match the common Anthropic/Claude Code
> convention rather than reinvent a base tool); `gate` collapsed to a single
> `needs` param.

The LLM driver is given exactly four tools. It is prompted to behave as a simple,
non-clever user: record friction instead of working around bad UX, and never
manufacture secrets.

### `bash`
Run a shell command inside the sandbox and return its stdout, stderr and exit code.
Named `bash` to match the established convention (Anthropic's `bash` tool, Claude
Code's `Bash`), so models use their existing priors.
| Param | Type | Req | Notes |
|-------|------|-----|-------|
| `command` | string | ✅ | The shell command to run. |

Returns to the model: `exit=<n>` plus stdout/stderr (each truncated to 4000 chars).

### `observe`
Record an AX finding in plain language: anything that made progress harder, was
ambiguous, produced a confusing/false error, or required a human. No scores, no
invented metrics.
| Param | Type | Req | Notes |
|-------|------|-----|-------|
| `note` | string | ✅ | What happened and why it is a finding. |
| `suggestion` | string | ✖ | Optional: how the tool could make this simpler. |

_Kept deliberately prompt-light for v0. A future skill will teach how to structure
observations; until then we avoid over-specifying in the prompt._

### `gate`
Stop the run because a human must provide a secret/credential or make a real
decision before progress can continue.
| Param | Type | Req | Notes |
|-------|------|-----|-------|
| `needs` | string | ✅ | What the human must provide or decide, and why — one description. |

_Scope of this tool is only to **signal** a human is needed and stop. It does NOT
collect the secret. Secure entry is a separate Layer 3 "secret broker": on a gate
it prompts the human outside the model's context, stores the value in the OS
keychain, injects it into the box at the process level, then resumes — the model
never sees the secret. Not built yet; today gate signals + stops._

### `finish`
End the run.
| Param | Type | Req | Notes |
|-------|------|-----|-------|
| `reached` | bool | ✅ | True only if the goal was actually accomplished. Feeds the `goal_reached` metric. |
| `summary` | string | ✅ | The model's free-text recap of what happened. |

_Provisional: the meaning of `summary` and possibly replacing `reached` with an
`outcome` enum will be refined later via tool description / a skill._

### Driver limits
- `maxSteps = 30` — hard cap on tool-call rounds per run.
- `maxToolOutput = 4000` — chars of stdout/stderr fed back to the model per command.

## Telemetry metrics

> **Status: v0 — not yet implemented.**
> Metrics are a deliberate, human-curated set — the harness does not invent them.

Per-run metrics (the report’s schema and the app’s core UX):

| Metric | Type | Definition | Why it matters |
|--------|------|-----------|----------------|
| `outcome` | enum | `goal_reached` / `stopped_at_gate` / `stuck` / `error` | The single headline result. |
| `goal_reached` | bool | Goal actually accomplished (distinct from hitting `stop_when`). | Stops "reached the gate" being misread as success. |
| `human_interventions` (HIC) | int | Count of `gate()` calls — times a human was required. | North-star: minimise human steps. |
| `gate_details` | list | For each gate: the `needs` description. | Shows *where* and *why* a human was needed. |
| `steps` | int | Driver tool-call rounds to terminal. | Effort/efficiency. |
| `commands_run` | int | Count of `run()` calls. | How much poking it took. |
| `duration_seconds` | number | Wall-clock time of the run. | Speed of onboarding. |
| `observations` | int + list | Recorded friction findings (note + optional suggestion). | The qualitative AX findings. |
| `false_errors` | int | Non-zero exits that were not real failures (e.g. exit 13/10 on a not-ready state). | Catches the exit-13/10 class of defect automatically. |
| `driver_model` | string | OpenRouter model id that drove the run. | Cross-model comparison. |
| `tokens` / `cost` | int / number | Usage reported by OpenRouter. | Run cost; optional. |

Implementation notes:
- All eleven rows above are approved for v0.
- `false_errors`: detect via a heuristic (a non-zero exit whose output still
  describes normal next actions / state — e.g. init's exit 13/10) AND let the
  driver flag them via `observe`; reconcile the two. Exact heuristic TBD at build time.
- `tokens` / `cost`: read from the OpenRouter response `usage` field.
- `duration_seconds`: wall-clock around the driver loop.

## Secret broker (Layer 3) — `internal/broker`, `internal/secrets`

When the driver calls `gate()`, the broker provides the next pending declared
credential and lets the run continue:

```
gate fires → broker has a matching manifest credential?
   ├─ in store  → inject into box → return "available at <path>" → driver continues
   ├─ not stored → prompt the user once (stdin) → store → inject → continue
   └─ none pending → run stops (Result.StoppedAtGate = true → outcome stopped_at_gate)
```

- **Storage** (`internal/secrets`): macOS Keychain when available, else a 0600
  file store under `~/.axprobe/secrets/<scenario>/<name>`. First run prompts;
  later runs auto-inject.
- **Injection**: `kind:file` → `Box.CopyIn` to `inject.box_path`; `kind:value` →
  an `export` line in `/etc/profile.d` for `inject.env`.
- **Secrecy**: the value never appears in any model API request — the driver only
  receives "credential <name> is available at <path>". Caveat: the box runs as
  root, so an agent that chooses to `cat` the injected file could read it; the
  guarantee is about what *we* send to the model, not what a root agent can do
  inside its own sandbox. The box is ephemeral and destroyed after the run.
- **Metrics**: every `gate()` counts toward `human_interventions` (HIC), resolved
  or not; only an *unresolved* gate sets `stopped_at_gate`.

## Human-interaction contract: credential kinds & run modes

> **Status: v0.**
> How axprobe interacts with a human is a public contract. Two-level dispatch:
> the **agent** decides *whether* a human is needed (`gate()`); the **broker**
> decides *how*, by switching on the credential's `kind`. The agent never sees
> `kind`. New kinds add a broker resolver, not an agent tool.

**`kind` is a property of a declared `credentials[]` entry.** Runtime: agent
gates → broker takes the next pending credential → `switch(kind)` → resolver runs.

| `kind` | declares | how the human interacts | unattended (CI) | status |
|--------|----------|-------------------------|-----------------|--------|
| `file` | `inject.box_path` | terminal prompt for a file path | from store or fail | ✅ built |
| `value` | `inject.env` / `box_path` | terminal prompt for a value | from store or fail | ✅ built |
| `oauth` (device) | `login_command`, `mode: device` | broker runs login_command, streams the URL+code to the host, waits — no callback, no port forwarding | only if a refresh token is already cached | ✅ device built |
| `oauth` (loopback) | `login_command`, `callback_port` | broker runs login_command, forwards the callback port, waits | as above | ⏳ approved, resolver pending |

**oauth mechanism (approved):** the broker — not the agent — runs `login_command`
inside the box and waits; the LLM never improvises auth handling.
- **device** (default, RFC 8628 — e.g. `gh auth login`): the command prints a URL
  and a code and polls. The broker streams that output live to the host terminal;
  the user opens the URL and enters the code; the command completes. No callback
  server, no port forwarding.
- **loopback** (e.g. `gcloud auth login`): the command listens on `callback_port`;
  the broker forwards it host↔box so the browser redirect reaches the box. (Not
  built yet. A product's bundled client should prefer device flow.)

Testable today against real tools (`gh` = device, `gcloud` = loopback), so the
resolvers are verified without waiting for any specific product.

**Run modes (approved):**
- `interactive` — a human is present and answers gates (typing, or a browser).
- `unattended` — gates must be satisfiable from the store (cached credential /
  refresh token), otherwise the run fails. This is the CI/regression mode.

## Token cache & unattended/CI oauth

> **Status: v0. Not yet built.**
> How a successful oauth login is reused (so repeat/CI runs need no browser).
> Approved: extract→store→inject (cache-first); cached tokens in **Keychain only**
> (no file fallback); unattended with no token → **stopped_at_gate** (never blocks).

**Problem.** An oauth token is written by the *tool* inside the box (gh →
`~/.config/gh/hosts.yml`, gcloud → `~/.config/gcloud/`) and the box is disposable,
so the token dies with it — every run would need a browser. Fix: cache it.

**Lifecycle — extract → store → inject (cache-first):**
```
resolveOAuth(cred):
  if a cached token for cred exists  → CopyIn the token files → tool is
                                        authenticated → resume (NO browser)
  else                               → run login_command (browser) → on success,
                                        CopyOut cred.token_paths → store → resume
```

**New pieces:**
- Manifest oauth credential gains **`token_paths`** — the file(s)/dir(s) the tool
  stores its credential in after login.
- Box gains **`CopyOut`** (tar a path out of the box; `CopyIn` already exists).
- Broker `resolveOAuth` becomes **cache-first** (above).
- A **run mode**: `interactive` (may do the browser flow) vs `unattended` (must
  satisfy the gate from a cached/provisioned token, else the run ends
  `stopped_at_gate` — never blocks waiting for a human).

**Storage (approved).** Cached oauth tokens are **live secrets** (incl. refresh
tokens) → stored in the OS Keychain **only**, no plaintext-file fallback. On a
non-macOS host the cache is simply unavailable (interactive login each time). A
stale cache falls back to a fresh interactive login.

**GitHub Actions / CI.** No human, no browser → interactive oauth is impossible.
CI relies entirely on a **pre-provisioned token**:
```
GitHub Actions secret → env/file in the runner → axprobe injects token files
                        into the box (the SAME CopyIn as token-cache) → authenticated
```
So token-cache (local) and CI-secret-injection are **one mechanism** (`CopyIn` the
token), differing only in the token's source (a prior login vs a GitHub secret).
In CI a gate may also be satisfied by a different `kind` (a PAT / service-account
key from a secret via the value/file resolvers) instead of an oauth token.

## Report JSON schema (public regression interface)

> **Status: v0.**
> The per-run JSON report is a stable public contract consumed by regression
> tooling. Approved strict (`additionalProperties: false`) with a `schema_version`
> field. Full spec: [`schema/report.schema.json`](schema/report.schema.json)
> (JSON Schema draft 2020-12).

Top-level fields (see the schema file for full descriptions and nested shapes):

| Field | Type | Notes |
|-------|------|-------|
| `schema_version` | string | Version of this contract (emitted as `"2"`). |
| `scenario` | string | Manifest name. |
| `driver_model` | string | OpenRouter model id (empty for scripted runs). |
| `outcome` | enum | `goal_reached` / `stopped_at_gate` / `stuck` / `error`. |
| `goal_reached` | bool | Goal actually accomplished. |
| `human_interventions` | int | Count of `gate()` calls (HIC). |
| `gate_details` | string[] | Per-gate `needs` description. |
| `steps` | int | Driver tool-call rounds. |
| `commands_run` | int | `bash()` calls. |
| `duration_seconds` | number | Wall-clock of the driver loop. |
| `observations` | object[] | `{ note, suggestion? }`. |
| `false_errors` | object[] | `{ command, exit_code, reason }`. |
| `tokens` | object | `{ prompt, completion, total, cost_usd }`. |
| `summary` | string | `finish()` recap; may be empty when gated. |

Approved v0: field set, names, types and descriptions are locked; the contract is
strict (`additionalProperties: false`); `schema_version` is emitted (`"2"`). Any
change is a deliberate, versioned bump of `schema_version`.

## Conventions

- Go 1.22+, single dependency (`gopkg.in/yaml.v3`); keep it a clean single binary.
- A non-zero exit from the tool under test is **data**, never a harness error.
- Secrets via `.env` only (gitignored); never logged, never in manifests/CLI args.
- Build/verify: `go build -o axprobe . && go vet ./...`; run `testdata/smoke.yaml`.
- Requires a running Docker daemon (Colima locally).
