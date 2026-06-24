package manifest

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SupportedSchemaVersion is the contract version this binary understands. Both
// the workspace config and scenario manifests are public interfaces, so they are
// versioned like the report schema.
const SupportedSchemaVersion = "1"

// ConfigFile is the workspace-level file that defines HOW to install/run the
// product under test, shared by every scenario in the same .axprobe/ directory.
const ConfigFile = "config.yaml"

// Manifest is a resolved test scenario: the scenario file merged with its
// workspace config. The harness knows nothing product-specific — install, goal,
// and checks all come from here.
type Manifest struct {
	SchemaVersion string  `yaml:"schema_version"`
	Name          string  `yaml:"name"`
	Box           BoxSpec `yaml:"box"`

	// Intent is the plain-language description of the scenario (used by `explore`
	// to author the rest). Goal/StopWhen/SuccessCheck are the machine-derived form.
	Intent       string `yaml:"intent"`
	Goal         string `yaml:"goal"`
	StopWhen     string `yaml:"stop_when"`
	SuccessCheck string `yaml:"success_check"`

	// Defaults are inherited from .axprobe/config.yaml. They are not valid in
	// scenario YAML; scenarios stay model-agnostic so the same goal can run across
	// a model matrix.
	Defaults Defaults `yaml:"-"`

	// Credentials the secret broker may provide when the driver hits a gate.
	Credentials []Credential `yaml:"credentials"`

	// Workspace describes a reproducible project workspace for the scenario. The
	// template lives beside the manifest (usually under .axprobe/fixtures) and is
	// copied into a temp dir when --workdir is not supplied.
	Workspace *Workspace `yaml:"workspace,omitempty"`

	// Expect is the AX bar (the PM-owned "definition of done"). When set, the run
	// passes only if the result meets it; otherwise axprobe exits non-zero (CI gate).
	Expect *Expect `yaml:"expect,omitempty"`

	// Reset returns the fixture to a clean baseline BEFORE the run, so a re-run
	// starts from scratch. The box is disposable (in-box files reset for free);
	// reset covers cross-run state.
	Reset *Reset `yaml:"reset,omitempty"`

	// Teardown disposes the EXTERNAL side-effects the tool created during the run
	// — cloud resources that outlive the disposable box (a Turso DB, a GitHub repo,
	// a deploy). It is the AFTER-phase counterpart to reset's BEFORE-phase: its
	// commands run IN-BOX (same warm creds/env as the run), in the box's defer
	// path, so they fire on success, failure, and crash alike — no orphans. Each
	// fixture that creates real resources declares here how to clean itself up.
	Teardown *Teardown `yaml:"teardown,omitempty"`
}

// Reset declares how to clear a fixture's persistent state before a run. Secrets
// purges this scenario's cached credentials so an auth fixture runs cold. Paths
// are workspace-relative outputs the scenario itself generates (e.g. sources/vercel)
// — removed before the run so it authors from scratch. The whole workspace is
// never wiped; only declared paths inside it are cleared.
type Reset struct {
	Secrets bool     `yaml:"secrets,omitempty"`
	Paths   []string `yaml:"paths,omitempty"`
}

// Workspace declares a fixture-backed project workspace for deterministic runs.
// Template is a directory path relative to the scenario manifest directory.
type Workspace struct {
	Template string `yaml:"template,omitempty"`
}

// Teardown declares how a fixture cleans up the external resources it created.
// Run is a list of in-box commands executed after the run (in the box's defer
// path, before the box is brought down) — typically ONE symmetric tool command
// (e.g. `edenyx agent destroy probe --yes`) that cascades to the underlying
// infra, NOT a pile of raw provider calls. Commands run unconditionally so a
// failed run still cleans up; a non-zero teardown command is reported, not fatal.
type Teardown struct {
	Run []string `yaml:"run"`
}

// Expect is a scenario's AX assertions. Pointers/zero distinguish "not asserted".
// Counts are upper bounds (<=); goal_reached/outcome are exact.
type Expect struct {
	GoalReached           *bool  `yaml:"goal_reached,omitempty"`
	Outcome               string `yaml:"outcome,omitempty"`
	MaxHumanInterventions *int   `yaml:"max_human_interventions,omitempty"`
	MaxFalseErrors        *int   `yaml:"max_false_errors,omitempty"`
}

// Config is the workspace file (.axprobe/config.yaml): how to install/run the
// product under test, plus any credentials shared across scenarios.
type Config struct {
	SchemaVersion string       `yaml:"schema_version"`
	Box           BoxSpec      `yaml:"box"`
	Defaults      Defaults     `yaml:"defaults,omitempty"`
	Credentials   []Credential `yaml:"credentials"`
}

// Defaults are repo/workspace-level operator defaults. They are lower precedence
// than flags/env and higher precedence than ~/.axprobe/config.yaml.
type Defaults struct {
	DriverModel string `yaml:"driver_model,omitempty"`
	ReviewModel string `yaml:"review_model,omitempty"`
}

// BoxSpec declares the environment and how to get the tool under test into it.
type BoxSpec struct {
	Image string `yaml:"image"`
	// Docker exposes the host Docker daemon inside the box via /var/run/docker.sock.
	// This is high privilege, so scenarios opt in explicitly when the tool under
	// test must run Docker (for example, a CLI that verifies generated projects in
	// containers). It provides the socket; setup is still responsible for a docker
	// client binary if the image does not include one.
	Docker bool `yaml:"docker,omitempty"`
	// Copy injects host files into the box (before setup runs): each entry is
	// "<host-path>:<box-path>". File mode is preserved, so a compiled binary stays
	// executable — the blessed way to test a prebuilt binary without mounting the
	// whole project with --workdir.
	Copy  []string `yaml:"copy"`
	Setup []string `yaml:"setup"`
}

// Credential declares a secret the broker can collect from the user (once),
// store for reuse, and inject into the box. The secret never reaches the model.
type Credential struct {
	Name   string     `yaml:"name"`
	Kind   string     `yaml:"kind"` // "file" | "value" | "oauth"
	Prompt string     `yaml:"prompt,omitempty"`
	Inject InjectSpec `yaml:"inject,omitempty"`

	// oauth only.
	Mode         string   `yaml:"mode,omitempty"` // "device" (default) | "loopback"
	LoginCommand string   `yaml:"login_command,omitempty"`
	CallbackPort int      `yaml:"callback_port,omitempty"` // loopback only
	TokenPaths   []string `yaml:"token_paths,omitempty"`   // box paths cached after login
}

// InjectSpec says where a credential lands inside the box. Exactly one of the
// fields is used: BoxPath writes a file there; Env exports it for login shells.
type InjectSpec struct {
	BoxPath string `yaml:"box_path,omitempty"`
	Env     string `yaml:"env,omitempty"`
}

// Load reads and validates a scenario, inheriting the box and shared credentials
// from a sibling config.yaml when the scenario does not define its own box.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}

	// `probes:` (scripted Layer-0 commands) was removed — axprobe manifests are
	// LLM-driven (a `goal:` a driver pursues). Reject the key explicitly so an old
	// fixture, or an agent that writes `probes:`, gets a clear migration error
	// instead of a silently ignored field.
	var legacy struct {
		Probes []string `yaml:"probes"`
	}
	if yaml.Unmarshal(data, &legacy) == nil && len(legacy.Probes) > 0 {
		return nil, fmt.Errorf("manifest %s: `probes:` is no longer supported — axprobe drives an LLM against a `goal:`; move a deterministic check to a unit test, or run commands ad hoc with `axprobe probe`", path)
	}

	// Validate the workspace config FIRST — it is the foundation, so a broken
	// config.yaml should surface as the root cause before scenario-level nitpicks
	// (the axprobe-self run showed an agent fixing scenario fields one by one only
	// to hit the real config error last). A self-contained scenario (its own box)
	// does not need it; an absent config is fine (LoadConfig returns nil, nil).
	cfg, err := LoadConfig(filepath.Join(filepath.Dir(path), ConfigFile))
	if err != nil {
		return nil, err
	}

	if err := checkVersion(path, m.SchemaVersion); err != nil {
		return nil, err
	}
	if m.Name == "" {
		return nil, fmt.Errorf("manifest %s: name is required", path)
	}

	// Inherit environment + shared credentials from the workspace config unless
	// the scenario is self-contained (defines its own box).
	if m.Box.Image == "" && cfg != nil {
		m.Box = cfg.Box
		m.Defaults = cfg.Defaults
		m.Credentials = mergeCredentials(cfg.Credentials, m.Credentials)
	}

	if m.Box.Image == "" {
		return nil, fmt.Errorf("manifest %s: no box.image (define it in the scenario or in a sibling %s)", path, ConfigFile)
	}
	return &m, nil
}

// LoadConfig reads an optional workspace config. Returns (nil, nil) if absent.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := checkVersion(path, c.SchemaVersion); err != nil {
		return nil, err
	}
	if c.Box.Image == "" {
		return nil, fmt.Errorf("%s: box.image is required", path)
	}
	return &c, nil
}

func checkVersion(path, v string) error {
	if v == "" {
		return fmt.Errorf("%s: schema_version is required (add `schema_version: \"%s\"`)", path, SupportedSchemaVersion)
	}
	if v != SupportedSchemaVersion {
		return fmt.Errorf("%s: unsupported schema_version %q (this axprobe supports %q)", path, v, SupportedSchemaVersion)
	}
	return nil
}

// mergeCredentials returns base credentials overlaid with scenario ones; a
// scenario credential with the same name overrides the shared one.
func mergeCredentials(base, override []Credential) []Credential {
	out := append([]Credential{}, base...)
	for _, o := range override {
		replaced := false
		for i := range out {
			if out[i].Name == o.Name {
				out[i] = o
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, o)
		}
	}
	return out
}
