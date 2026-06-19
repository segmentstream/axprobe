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
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/broker"
	"github.com/segmentstream/axprobe/internal/dotenv"
	"github.com/segmentstream/axprobe/internal/driver"
	"github.com/segmentstream/axprobe/internal/explore"
	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/manifest"
	"github.com/segmentstream/axprobe/internal/report"
	"github.com/segmentstream/axprobe/internal/secrets"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  axprobe run [--model <id>] [--report <path>] [<manifest.yaml> | <scenario-name>]")
	fmt.Fprintln(os.Stderr, "      with no argument, runs every .axprobe/*.yaml in the current directory")
	fmt.Fprintln(os.Stderr, "  axprobe explore --model <id> [--name <name>] \"<intent>\"")
	fmt.Fprintln(os.Stderr, "      drive a plain-language intent once and synthesize .axprobe/<name>.yaml")
	fmt.Fprintln(os.Stderr, "  axprobe probe [--image <img>] <command> [<command>...]")
	fmt.Fprintln(os.Stderr, "      run command(s) in a clean box (install from .axprobe/config.yaml); no LLM")
	os.Exit(2)
}

func main() {
	// Load a folder-local .env (e.g. OPENROUTER_API_KEY) before reading any key.
	// Real environment variables still win.
	dotenv.Load(".env")

	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		runMain()
	case "explore":
		exploreMain()
	case "probe":
		probeMain()
	default:
		usage()
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

	b, teardown, err := bringUp(m)
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

	failed := false
	for _, mp := range manifests {
		// With multiple manifests, --report can't name them all; use per-scenario defaults.
		rp := *reportPath
		if len(manifests) > 1 {
			rp = ""
		}
		if err := cmdRun(mp, *model, rp, *unattended); err != nil {
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
	pos := parsePositionals(fs, os.Args[2:])
	if len(pos) < 1 {
		usage()
	}
	intent := strings.Join(pos, " ")
	if err := exploreCmd(intent, *model, *name); err != nil {
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

func cmdRun(manifestPath, model, reportPath string, unattended bool) error {
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

	b, teardown, err := bringUp(m)
	if err != nil {
		return err
	}
	defer teardown()

	if client == nil {
		return runProbes(b, m) // Layer 0
	}
	return runDriver(b, m, client, reportPath, unattended) // Layer 1
}

// bringUp creates a fresh box, starts it, and runs the manifest's setup. The
// returned teardown must be deferred by the caller. extraPorts are published in
// addition to those declared by loopback oauth credentials — explore uses this
// to reserve a callback port for an oauth login it may only discover mid-run.
func bringUp(m *manifest.Manifest, extraPorts ...int) (box.Box, func(), error) {
	// Publish callback ports declared by loopback oauth credentials so the
	// browser redirect on the host reaches the login server inside the box.
	var ports []int
	for _, c := range m.Credentials {
		if c.Kind == "oauth" && c.Mode == "loopback" && c.CallbackPort > 0 {
			ports = append(ports, c.CallbackPort)
		}
	}
	ports = append(ports, extraPorts...)
	b := box.NewLocalDockerBox(m.Box.Image, ports...)
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
func exploreCmd(intent, model, name string) error {
	if model == "" {
		return fmt.Errorf("explore requires --model")
	}
	cfg, err := manifest.LoadConfig(filepath.Join(".axprobe", manifest.ConfigFile))
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("explore needs .axprobe/%s (the workspace install)", manifest.ConfigFile)
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
	b, teardown, err := bringUp(m, explore.DefaultOAuthPort)
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
	fmt.Printf("\n▸ outcome:  %s (goal_reached=%v, gates=%d, discovered=%d creds)\n",
		res.Outcome, res.GoalReached, len(res.Gates), len(disc.Discovered))
	fmt.Printf("▸ manifest: %s  — review & commit\n", path)
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

// runDriver is the Layer 1 LLM driver. It collects the approved metrics and
// emits both a human summary and a JSON report artifact.
func runDriver(b box.Box, m *manifest.Manifest, client *llm.Client, reportPath string, unattended bool) error {
	store := secrets.New(m.Name)
	br := broker.New(m, b, store, unattended, os.Stdin, os.Stdout)
	br.Prime() // restore any cached oauth tokens before driving

	res, err := driver.New(b, m, client, br).Run(context.Background())
	if err != nil {
		return err
	}

	rep := report.From(m.Name, res)
	rep.PrintHuman(os.Stdout)

	if reportPath == "" {
		reportPath = m.Name + ".report.json"
	}
	if err := rep.WriteJSON(reportPath); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write report: %v\n", err)
	} else {
		fmt.Printf("\n▸ report:   %s\n", reportPath)
	}

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
