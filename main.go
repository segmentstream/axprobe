// Command axprobe drives a CLI tool inside a disposable box and reports on the
// experience.
//
//	axprobe run <manifest.yaml>                 # Layer 0: scripted probes
//	axprobe run --model <id> <manifest.yaml>    # Layer 1: LLM driver via OpenRouter
//
// Both share the same box and manifest; only the "driver" differs. The LLM
// driver needs OPENROUTER_API_KEY in the environment.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/broker"
	"github.com/segmentstream/axprobe/internal/config"
	"github.com/segmentstream/axprobe/internal/dotenv"
	"github.com/segmentstream/axprobe/internal/driver"
	"github.com/segmentstream/axprobe/internal/events"
	"github.com/segmentstream/axprobe/internal/explore"
	"github.com/segmentstream/axprobe/internal/lint"
	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/manifest"
	"github.com/segmentstream/axprobe/internal/report"
	"github.com/segmentstream/axprobe/internal/review"
	"github.com/segmentstream/axprobe/internal/secrets"
	"github.com/segmentstream/axprobe/internal/skill"
	"github.com/segmentstream/axprobe/internal/update"
	"github.com/segmentstream/axprobe/internal/version"
)

// printUsage writes the help text to w. usage() uses it for the error path
// (stderr, exit 2); the explicit `help`/`--help` path uses it for success.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "axprobe — drive a CLI in a disposable box and report its Agentic Experience.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "First time? Run `axprobe init` to scaffold a workspace, then edit it and `axprobe run`.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  axprobe init")
	fmt.Fprintln(w, "      scaffold .axprobe/config.yaml (the workspace install) + an example scenario")
	fmt.Fprintln(w, "  axprobe run [--model <id>] [--report <path>] [--workdir <dir>] [--reset] [<manifest.yaml> | <scenario-name>]")
	fmt.Fprintln(w, "      with no argument, runs every .axprobe/*.yaml in the current directory")
	fmt.Fprintln(w, "      --workdir mounts a persistent project (live journey); --reset starts cold")
	fmt.Fprintln(w, "  axprobe explore --model <id> [--name <name>] [--workdir <dir>] \"<intent>\"")
	fmt.Fprintln(w, "      drive a plain-language intent once and synthesize .axprobe/<name>.yaml")
	fmt.Fprintln(w, "  axprobe probe [--image <img>] <command> [<command>...]")
	fmt.Fprintln(w, "      run command(s) in a clean box (install from .axprobe/config.yaml); no LLM")
	fmt.Fprintln(w, "  axprobe lint [--strict] [<scenario-name>]")
	fmt.Fprintln(w, "      warn if a scenario goal leaks tool-interface detail (prefer user intent)")
	fmt.Fprintln(w, "  axprobe skill [--install]")
	fmt.Fprintln(w, "      print the axprobe-author skill (rubric), or install it under .claude/skills/")
	fmt.Fprintln(w, "  axprobe review [--model <id>] <report.json>")
	fmt.Fprintln(w, "      AX-review a run report into a paste-ready finding draft (does not file)")
	fmt.Fprintln(w, "  axprobe key set")
	fmt.Fprintln(w, "      store your OpenRouter API key in the Keychain (read from stdin)")
	fmt.Fprintln(w, "  axprobe update [--check]")
	fmt.Fprintln(w, "      update an install.sh-installed binary to the latest GitHub release")
	fmt.Fprintln(w, "  axprobe version")
	fmt.Fprintln(w, "      print the build version")
}

// usage is the error path: a misuse prints help to stderr and exits non-zero.
func usage() {
	printUsage(os.Stderr)
	os.Exit(2)
}

func main() {
	// Load the OpenRouter key etc. Real environment variables win. A global
	// ~/.axprobe/.env is preferred and loaded first, so a secret never has to live
	// in a project .env that a --workdir bind-mount would expose to the sandbox.
	if home, err := os.UserHomeDir(); err == nil {
		dotenv.Load(filepath.Join(home, ".axprobe", ".env"))
	}
	dotenv.Load(".env")
	// Fall back to the Keychain (axprobe/app/openrouter) when no env/.env key is
	// set — the preferred, plaintext-free home for the key.
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		if v, ok := secrets.GetApp("openrouter"); ok {
			os.Setenv("OPENROUTER_API_KEY", string(v))
		}
	}

	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "help", "--help", "-h":
		// Explicit help is success, not a usage error: print to stdout, exit 0.
		printUsage(os.Stdout)
		os.Exit(0)
	case "init":
		initMain()
	case "run":
		runMain()
	case "explore":
		exploreMain()
	case "probe":
		probeMain()
	case "lint":
		lintMain()
	case "skill":
		skillMain()
	case "review":
		reviewMain()
	case "key":
		keyMain()
	case "update":
		updateMain()
	case "version":
		versionMain()
	default:
		usage()
	}
}

// versionMain prints the running build's version (stamped at release time).
func versionMain() {
	fmt.Printf("axprobe %s\n", version.Current().Version)
}

// updateMain self-updates a binary installed via install.sh: fetch the latest
// GitHub release, verify its checksum, and atomically replace the running
// binary. A dev/source build (version "dev") cannot self-update (semver rejects
// it), which is correct — the dev wrapper rebuilds from source instead.
func updateMain() {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "check for an update without installing it")
	_ = parsePositionals(fs, os.Args[2:])
	updater := update.NewUpdater(version.Current(), os.Stdout, os.Stderr)
	if err := updater.Run(context.Background(), update.Options{CheckOnly: *check}); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
}

// initMain scaffolds a workspace so a new user never has to reverse-engineer the
// schema (the axprobe-self run showed an agent binary-grepping for it): it writes
// .axprobe/config.yaml (the install recipe) and an example scenario, refusing to
// clobber anything that already exists.
func initMain() {
	dir := ".axprobe"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	wrote := false
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("• %s already exists, left as-is\n", p)
			return
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✓ wrote %s\n", p)
		wrote = true
	}
	write(manifest.ConfigFile, configScaffold)
	write("example.yaml", exampleScaffold)
	if wrote {
		fmt.Println("\nNext:")
		fmt.Println("  1. edit .axprobe/config.yaml — set box.image and the setup commands that install your tool")
		fmt.Println("  2. write your goal in .axprobe/example.yaml (or: axprobe explore --model <id> \"<intent>\")")
		fmt.Println("  3. axprobe lint example          # check the goal reads as user intent")
		fmt.Println("  4. axprobe run example --model <id>")
	}
}

const configScaffold = `schema_version: "1"
# The workspace: how to install the tool under test in a fresh, disposable box.
box:
  image: ubuntu:24.04
  setup:
    # Commands that install your CLI in the box. For example:
    # - apt-get update -qq && apt-get install -y -qq curl ca-certificates
    # - curl -fsSL https://example.com/install.sh | sh
`

const exampleScaffold = `schema_version: "1"
name: example
# The goal is the USER's intent in plain language — never the tool's own commands
# or flags. The agent must discover HOW from the tool itself; that discovery is
# the agentic experience under test. Run ` + "`axprobe lint example`" + ` to check it.
goal: <what does the user want to accomplish? e.g. "connect my data warehouse and confirm it actually works">
expect:
  goal_reached: true
  max_human_interventions: 1
  max_false_errors: 0
`

// keyMain stores a named app key in the Keychain (axprobe/app/<name>), read from
// stdin so it never lands in argv or shell history. The name defaults to
// "openrouter" (the LLM key) but is parameterized for future keys axprobe needs.
func keyMain() {
	if len(os.Args) < 3 || os.Args[2] != "set" {
		fmt.Fprintln(os.Stderr, "usage: axprobe key set [name]   # name defaults to openrouter; then paste the key (or pipe)")
		os.Exit(2)
	}
	name := "openrouter"
	if len(os.Args) >= 4 {
		name = os.Args[3]
	}
	fmt.Fprintf(os.Stderr, "Paste the %q key and press Enter (input is not hidden):\n", name)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		fmt.Fprintln(os.Stderr, "axprobe: no key provided")
		os.Exit(1)
	}
	// Light shape check for known key types so a wrong paste fails loudly here, not
	// at the first API call.
	if name == "openrouter" && !strings.HasPrefix(key, "sk-or-") {
		fmt.Fprintln(os.Stderr, "axprobe: that does not look like an OpenRouter key (expected sk-or-…); not stored")
		os.Exit(1)
	}
	if err := secrets.SetApp(name, []byte(key)); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ stored %q key in the Keychain (axprobe/app/%s)\n", name, name)
}

// reviewMain is the AX review agent: from a run report it drafts a paste-ready
// finding. With --model an LLM reviewer (guided by the skill) writes the judgment
// parts; without it, a mechanical scaffold. The Observed transcript is always
// verbatim from the report. It prints the draft — it never files (human-gated).
func reviewMain() {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	model := fs.String("model", "", "Judge model id (else AXPROBE_REVIEW_MODEL, then review_model in ~/.axprobe/config.yaml). Prefer a stronger model than the driver. Unset → mechanical scaffold.")
	pos := parsePositionals(fs, os.Args[2:])
	if len(pos) < 1 {
		usage()
	}
	data, err := os.ReadFile(pos[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: parse report %s: %v\n", pos[0], err)
		os.Exit(1)
	}

	// Resolve the judge model: --model > AXPROBE_REVIEW_MODEL > config.review_model.
	// It does NOT fall back to the driver model: an unset judge means the
	// mechanical scaffold, never "reuse the weak driver as judge".
	reviewModel := config.ResolveReviewModel(*model)
	if reviewModel == "" {
		fmt.Print(report.Draft(rep))
		return
	}
	// The judge should be stronger than — and distinct from — the model under
	// measurement. If they coincide, the instrument is grading itself; warn.
	if rep.Model != "" && reviewModel == rep.Model {
		fmt.Fprintf(os.Stderr, "axprobe: warning: review model %q is the same as the driver model that produced this report — the judge is the instrument. Prefer a stronger review model (--model, AXPROBE_REVIEW_MODEL, or review_model in ~/.axprobe/config.yaml).\n", reviewModel)
	}
	client, err := llm.New(reviewModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	draft, err := review.WithModel(context.Background(), client, rep)
	if err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(draft)
}

// skillMain prints the bundled axprobe-author skill (the authoring/review rubric),
// or installs it as a Claude Code skill with --install.
func skillMain() {
	fs := flag.NewFlagSet("skill", flag.ExitOnError)
	install := fs.Bool("install", false, "Write the skill to .claude/skills/<name>/SKILL.md instead of printing it.")
	_ = fs.Parse(os.Args[2:])
	if !*install {
		fmt.Print(skill.Body)
		return
	}
	dir := filepath.Join(".claude", "skills", skill.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(skill.Body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("installed skill → %s\n", path)
}

// lintMain warns when a scenario goal leaks tool-interface detail (command names,
// flags, internal states, transport jargon) instead of reading as user intent.
// Standalone lint is generic (no run); explore lints with the run's vocabulary.
func lintMain() {
	fs := flag.NewFlagSet("lint", flag.ExitOnError)
	strict := fs.Bool("strict", false, "Exit non-zero if any goal has leakage warnings.")
	pos := parsePositionals(fs, os.Args[2:])
	arg := ""
	if len(pos) >= 1 {
		arg = pos[0]
	}
	manifests, err := resolveManifests(arg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
	leaked := false
	for _, mp := range manifests {
		m, err := manifest.Load(mp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("▸ lint:     %s\n", m.Name)
		warns := lint.Goal(m.Goal, nil)
		if len(warns) == 0 {
			fmt.Println("    ✓ goal reads as user-level intent")
			continue
		}
		leaked = true
		for _, w := range warns {
			fmt.Printf("    ⚠ %s\n", w)
		}
	}
	if *strict && leaked {
		os.Exit(1)
	}
}

// probeMain runs one or more commands in a clean box — no LLM, no scenario, no
// report. The cheap "I know the command, just run it in the sandbox" mode.
func probeMain() {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	image := fs.String("image", "", "Box image to use when there is no .axprobe/config.yaml (runs with no setup).")
	pos := parsePositionals(fs, os.Args[2:])
	if len(pos) < 1 {
		usage()
	}
	if err := probeCmd(pos, *image); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
}

func probeCmd(commands []string, image string) error {
	var m *manifest.Manifest
	if image != "" {
		// Bare box, no setup.
		m = &manifest.Manifest{SchemaVersion: manifest.SupportedSchemaVersion, Name: "probe", Box: manifest.BoxSpec{Image: image}}
	} else {
		cfg, err := manifest.LoadConfig(filepath.Join(".axprobe", manifest.ConfigFile))
		if err != nil {
			return err
		}
		if cfg == nil {
			return fmt.Errorf("no .axprobe/%s and no --image — nothing to base the box on", manifest.ConfigFile)
		}
		m = &manifest.Manifest{SchemaVersion: manifest.SupportedSchemaVersion, Name: "probe", Box: cfg.Box}
	}

	b, teardown, err := bringUp(m, "")
	if err != nil {
		return err
	}
	defer teardown()

	for _, cmd := range commands {
		fmt.Printf("▸ probe:    %s\n", cmd)
		res, err := b.Exec(cmd)
		if err != nil {
			return err
		}
		printResult(res)
	}
	return nil
}

func runMain() {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	model := fs.String("model", "", "OpenRouter model id (e.g. moonshotai/kimi-k2.6). If set, use the LLM driver instead of scripted probes.")
	reportPath := fs.String("report", "", "Path to write the JSON AX report (default <scenario>.report.json for LLM runs).")
	unattended := fs.Bool("unattended", false, "No interactive gates: satisfy oauth from a cached/provisioned token or end stopped_at_gate (for CI).")
	workdir := fs.String("workdir", "", "Mount this host dir as the persistent project workspace (the live journey). Never wiped. Empty = disposable.")
	reset := fs.Bool("reset", false, "Start cold: purge this scenario's cached credentials before the run (does not touch --workdir).")
	eventsPath := fs.String("events", "", "Write a JSONL event stream here, watchable via `tail -f | jq` (login_url, bash, gate, observe, outcome…).")
	pos := parsePositionals(fs, os.Args[2:])
	openEvents(*eventsPath)
	// Resolve the driver model: --model > AXPROBE_MODEL > ~/.axprobe/config.yaml.
	*model = config.ResolveModel(*model)

	arg := ""
	if len(pos) >= 1 {
		arg = pos[0]
	}

	manifests, err := resolveManifests(arg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}

	failed := false
	for _, mp := range manifests {
		// With multiple manifests, --report can't name them all; use per-scenario defaults.
		rp := *reportPath
		if len(manifests) > 1 {
			rp = ""
		}
		if err := cmdRun(mp, *model, rp, *unattended, *workdir, *reset); err != nil {
			fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func exploreMain() {
	fs := flag.NewFlagSet("explore", flag.ExitOnError)
	model := fs.String("model", "", "OpenRouter model id (required).")
	name := fs.String("name", "", "Scenario name → .axprobe/<name>.yaml (default derived from intent).")
	workdir := fs.String("workdir", "", "Mount this host dir as the persistent project workspace (the live journey). Never wiped.")
	eventsPath := fs.String("events", "", "Write a JSONL event stream here, watchable via `tail -f | jq`.")
	pos := parsePositionals(fs, os.Args[2:])
	openEvents(*eventsPath)
	if len(pos) < 1 {
		usage()
	}
	intent := strings.Join(pos, " ")
	// Resolve the driver model: --model > AXPROBE_MODEL > ~/.axprobe/config.yaml.
	*model = config.ResolveModel(*model)
	if err := exploreCmd(intent, *model, *name, *workdir); err != nil {
		fmt.Fprintf(os.Stderr, "axprobe: %v\n", err)
		os.Exit(1)
	}
}

// resolveManifests implements the .axprobe/ convention:
//   - no arg          → every .axprobe/*.yaml in the current directory
//   - an existing file → that file (e.g. a separate manifest repo)
//   - a bare name      → .axprobe/<name>.yaml
func resolveManifests(arg string) ([]string, error) {
	if arg == "" {
		var found []string
		for _, pat := range []string{"*.yaml", "*.yml"} {
			m, _ := filepath.Glob(filepath.Join(".axprobe", pat))
			found = append(found, m...)
		}
		// config.yaml is the workspace file, not a scenario.
		found = filterOut(found, filepath.Join(".axprobe", manifest.ConfigFile))
		sort.Strings(found)
		if len(found) == 0 {
			return nil, fmt.Errorf("no manifest given and no .axprobe/*.yaml scenarios found in the current directory")
		}
		return found, nil
	}
	if fileExists(arg) {
		return []string{arg}, nil
	}
	if cand := filepath.Join(".axprobe", arg+".yaml"); fileExists(cand) {
		return []string{cand}, nil
	}
	return nil, fmt.Errorf("manifest not found: %q (looked for the file and .axprobe/%s.yaml)", arg, arg)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// parsePositionals parses flags that may appear before OR after positional
// arguments. Go's flag package stops at the first positional, which silently
// dropped flags like `axprobe run <name> --model X`; this re-parses around them.
func parsePositionals(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		_ = fs.Parse(args)
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func filterOut(items []string, drop string) []string {
	var out []string
	for _, it := range items {
		if it != drop {
			out = append(out, it)
		}
	}
	return out
}

func cmdRun(manifestPath, model, reportPath string, unattended bool, workdir string, reset bool) error {
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return err
	}

	// Build the LLM client up front so a missing key/model fails before we spin
	// up a box and run setup.
	var client *llm.Client
	if model != "" {
		client, err = llm.New(model)
		if err != nil {
			return err
		}
	}

	fmt.Printf("▸ scenario: %s\n", m.Name)
	if m.Goal != "" {
		fmt.Printf("▸ goal:     %s\n", m.Goal)
	}
	if model != "" {
		fmt.Printf("▸ driver:   llm (%s)\n", model)
	} else {
		fmt.Printf("▸ driver:   scripted probes\n")
	}

	// Clear the scenario's own declared outputs in the workdir before the run, so
	// it starts from scratch (a from-scratch authoring fixture). Host-side, guarded.
	if m.Reset != nil && len(m.Reset.Paths) > 0 && workdir != "" {
		clearWorkdirPaths(workdir, m.Reset.Paths)
	}

	b, teardown, err := bringUp(m, workdir)
	if err != nil {
		return err
	}
	defer teardown()

	if client == nil {
		return runProbes(b, m) // Layer 0
	}
	return runDriver(b, m, client, reportPath, unattended, reset) // Layer 1
}

// clearWorkdirPaths removes the scenario's declared outputs, each resolved
// relative to the workdir and refused if it would escape it — so reset clears
// only what the scenario generates, never the user's broader repo.
func clearWorkdirPaths(workdir string, paths []string) {
	wabs, err := filepath.Abs(workdir)
	if err != nil {
		return
	}
	for _, p := range paths {
		target := filepath.Clean(filepath.Join(wabs, p))
		if target == wabs || !strings.HasPrefix(target, wabs+string(os.PathSeparator)) {
			fmt.Printf("⚠ reset:    skipping %q (escapes --workdir)\n", p)
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			fmt.Printf("  warning: reset path %q: %v\n", p, err)
			continue
		}
		fmt.Printf("▸ reset:    cleared %s\n", p)
	}
}

// bringUp creates a fresh box, starts it, and runs the manifest's setup. The
// returned teardown must be deferred by the caller. extraPorts are published in
// addition to those declared by loopback oauth credentials — explore uses this
// to reserve a callback port for an oauth login it may only discover mid-run.
func bringUp(m *manifest.Manifest, workdir string, extraPorts ...int) (box.Box, func(), error) {
	// Publish callback ports declared by loopback oauth credentials so the
	// browser redirect on the host reaches the login server inside the box.
	var ports []int
	for _, c := range m.Credentials {
		if c.Kind == "oauth" && c.Mode == "loopback" && c.CallbackPort > 0 {
			ports = append(ports, c.CallbackPort)
		}
	}
	ports = append(ports, extraPorts...)
	if workdir != "" {
		if err := checkWorkdirSecrets(workdir); err != nil {
			return nil, nil, err
		}
	}
	b := box.NewLocalDockerBox(m.Box.Image, ports...)
	b.Workdir = workdir // live journey: mount the real project; "" = disposable
	fmt.Printf("▸ box up:   %s\n", m.Box.Image)
	if err := b.Up(); err != nil {
		return nil, nil, err
	}
	teardown := func() {
		fmt.Println("▸ box down")
		if err := b.Down(); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: teardown failed: %v\n", err)
		}
	}
	// Setup: how the tool under test gets into the box. A failed step aborts.
	for _, step := range m.Box.Setup {
		fmt.Printf("▸ setup:    %s\n", step)
		res, err := b.Exec(step)
		if err != nil {
			teardown()
			return nil, nil, err
		}
		printResult(res)
		if res.ExitCode != 0 {
			teardown()
			return nil, nil, fmt.Errorf("setup step failed (exit %d): %s", res.ExitCode, step)
		}
	}
	return b, teardown, nil
}

// exploreCmd drives a plain-language intent once, discovering credentials
// interactively, and synthesizes a scenario manifest under .axprobe/.
func exploreCmd(intent, model, name, workdir string) error {
	if model == "" {
		return fmt.Errorf("explore requires --model")
	}
	cfg, err := manifest.LoadConfig(filepath.Join(".axprobe", manifest.ConfigFile))
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("no workspace found: .axprobe/%s is missing (it defines how to install the tool under test).\n"+
			"Run `axprobe init` to scaffold one, then set box.image and the setup commands.", manifest.ConfigFile)
	}
	if name == "" {
		name = slug(intent)
	}
	client, err := llm.New(model)
	if err != nil {
		return err
	}

	m := &manifest.Manifest{
		SchemaVersion: manifest.SupportedSchemaVersion,
		Name:          name,
		Box:           cfg.Box,
		Intent:        intent,
		Goal:          intent,
		Credentials:   cfg.Credentials,
	}

	fmt.Printf("▸ explore:  %s\n", name)
	fmt.Printf("▸ intent:   %s\n", intent)

	// Reserve the oauth callback port up front: explore may discover a loopback
	// login mid-run, and the port must already be published to forward the redirect.
	b, teardown, err := bringUp(m, workdir, explore.DefaultOAuthPort)
	if err != nil {
		return err
	}
	defer teardown()

	disc := explore.NewDiscoveryBroker(b, secrets.New(name), client, os.Stdin, os.Stdout)
	res, err := driver.New(b, m, client, disc).Run(context.Background())
	if err != nil {
		return err
	}

	path, err := explore.Synthesize(name, intent, disc.Discovered)
	if err != nil {
		return err
	}

	// A drive produces an AX report whether it is authoring (explore) or measuring
	// (run) — the first-contact friction is the same signal.
	emitReport(name, res, "")

	fmt.Printf("\n▸ manifest: %s  — review & commit (%d creds discovered)\n", path, len(disc.Discovered))

	// Lint the goal against the tool vocabulary the run actually used: if the
	// intent named the tool's own commands/flags/states, it spoon-feeds the agent.
	if warns := lint.Goal(m.Goal, res.Commands); len(warns) > 0 {
		fmt.Println("⚠ goal lint — the intent leaks tool-interface detail; prefer user-level intent:")
		for _, w := range warns {
			fmt.Printf("    - %s\n", w)
		}
	}
	return nil
}

// slug builds a filesystem-safe scenario name from the first words of intent.
func slug(s string) string {
	words := strings.Fields(strings.ToLower(s))
	if len(words) > 6 {
		words = words[:6]
	}
	var parts []string
	for _, w := range words {
		var clean []rune
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				clean = append(clean, r)
			}
		}
		if len(clean) > 0 {
			parts = append(parts, string(clean))
		}
	}
	out := strings.Join(parts, "-")
	if out == "" {
		return "scenario"
	}
	return out
}

// runProbes is the Layer 0 stand-in driver: a fixed list of commands.
func runProbes(b box.Box, m *manifest.Manifest) error {
	for _, p := range m.Probes {
		fmt.Printf("▸ probe:    %s\n", p)
		res, err := b.Exec(p)
		if err != nil {
			return err
		}
		printResult(res)
	}
	return nil
}

// emitReport builds the AX report from a drive, prints the human summary, and
// writes the JSON artifact. The report is a property of any drive — run uses it
// for the expect gate, explore for first-contact findings.
func emitReport(name string, res *driver.Result, reportPath string) report.Report {
	rep := report.From(name, res)
	rep.PrintHuman(os.Stdout)
	if reportPath == "" {
		reportPath = name + ".report.json"
	}
	if err := rep.WriteJSON(reportPath); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write report: %v\n", err)
	} else {
		fmt.Printf("\n▸ report:   %s\n", reportPath)
	}
	return rep
}

// runDriver is the Layer 1 LLM driver. It collects the approved metrics and
// emits both a human summary and a JSON report artifact.
func runDriver(b box.Box, m *manifest.Manifest, client *llm.Client, reportPath string, unattended, reset bool) error {
	store := secrets.New(m.Name)
	cold := reset || (m.Reset != nil && m.Reset.Secrets)
	br := broker.New(m, b, store, unattended, os.Stdin, os.Stdout)
	if cold {
		fmt.Println("▸ cold:     skipping cached-login restore (auth runs fresh)")
	} else {
		br.Prime() // restore the shared warehouse token (warm) before driving
	}

	res, err := driver.New(b, m, client, br).Run(context.Background())
	if err != nil {
		return err
	}

	rep := emitReport(m.Name, res, reportPath)

	// AX bar: if the scenario declares `expect`, fail the run (non-zero exit) when
	// the result misses it — this is the CI gate / TDD red-green signal.
	if fails := report.Evaluate(rep, m.Expect); len(fails) > 0 {
		fmt.Println("\n✗ AX expectations FAILED:")
		for _, f := range fails {
			fmt.Printf("    - %s\n", f)
		}
		return fmt.Errorf("AX expectations not met (%d)", len(fails))
	}
	if m.Expect != nil {
		fmt.Println("\n✓ AX expectations met")
	}
	return nil
}

// openEvents directs the JSONL event stream to a file (for `tail -f | jq`
// watching). The file stays open for the process lifetime; a run is one process.
func openEvents(path string) {
	if path == "" {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warning: --events %q: %v\n", path, err)
		return
	}
	events.SetOutput(f)
}

// checkWorkdirSecrets REFUSES to mount a workdir holding secret-looking files: a
// --workdir bind-mount makes everything in it readable by the sandboxed agent,
// and a deliberately-dumb agent does read them (a prompt prohibition is not
// reliable — observed live). Warning was not enough; we abort. Override with
// AXPROBE_ALLOW_WORKDIR_SECRETS=1 if you accept the exposure.
func checkWorkdirSecrets(workdir string) error {
	var hits []string
	for _, pat := range []string{".env", ".env.*", "*.pem", "*credential*.json", "*key*.json", "*secret*"} {
		m, _ := filepath.Glob(filepath.Join(workdir, pat))
		for _, h := range m {
			hits = append(hits, filepath.Base(h))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	if os.Getenv("AXPROBE_ALLOW_WORKDIR_SECRETS") != "" {
		fmt.Printf("⚠ --workdir has secret-looking files (%s); proceeding (AXPROBE_ALLOW_WORKDIR_SECRETS set)\n", strings.Join(hits, ", "))
		return nil
	}
	return fmt.Errorf("refusing to mount --workdir — it holds secret-looking files the sandboxed agent could read: %s\n"+
		"  move them out (axprobe's key belongs in the Keychain via `axprobe key set`), or set AXPROBE_ALLOW_WORKDIR_SECRETS=1 to override",
		strings.Join(hits, ", "))
}

// printResult renders one command's output, indented, with its exit code.
func printResult(res box.ExecResult) {
	for _, stream := range []string{res.Stdout, res.Stderr} {
		s := strings.TrimRight(stream, "\n")
		if s == "" {
			continue
		}
		for _, line := range strings.Split(s, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}
	fmt.Printf("  └─ exit %d\n", res.ExitCode)
}
