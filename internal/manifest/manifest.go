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

	// Probes are scripted commands run when no --model is given (Layer 0).
	Probes []string `yaml:"probes"`

	// Credentials the secret broker may provide when the driver hits a gate.
	Credentials []Credential `yaml:"credentials"`

	// Expect is the AX bar (the PM-owned "definition of done"). When set, the run
	// passes only if the result meets it; otherwise axprobe exits non-zero (CI gate).
	Expect *Expect `yaml:"expect,omitempty"`

	// Reset returns the fixture to a clean baseline BEFORE the run, so a re-run
	// starts from scratch. The box is disposable (in-box files reset for free);
	// reset covers cross-run state.
	Reset *Reset `yaml:"reset,omitempty"`
}

// Reset declares how to clear a fixture's persistent state before a run. Secrets
// purges this scenario's cached credentials so an auth fixture runs cold. Paths
// are workdir-relative outputs the scenario itself generates (e.g. sources/vercel)
// — removed before the run so it authors from scratch. The whole workdir is never
// wiped (it is the user's repo); only declared, in-workdir paths are cleared.
type Reset struct {
	Secrets bool     `yaml:"secrets,omitempty"`
	Paths   []string `yaml:"paths,omitempty"`
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
	Credentials   []Credential `yaml:"credentials"`
}

// BoxSpec declares the environment and how to get the tool under test into it.
type BoxSpec struct {
	Image string   `yaml:"image"`
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
