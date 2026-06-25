// Package config loads optional operator-level defaults from
// ~/.axprobe/config.yaml. These are conveniences only: every value is
// overridable by workspace defaults, an env var, and then by a command-line flag
// (flag > env > workspace defaults > user config). The driver model (who is
// measured) and the review model (who judges) are resolved separately and on
// purpose, so the deliberately weak driver model is never silently reused as the
// judge.
//
// DriverModel belongs here (an operator/runtime choice), NOT in a scenario
// manifest: a scenario is a spec that should run unchanged across many models.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the user-level defaults file (~/.axprobe/config.yaml).
type Config struct {
	Driver      string `yaml:"driver"`       // driver runtime: axprobe (default), codex, claude
	DriverModel string `yaml:"driver_model"` // default driver model (e.g. moonshotai/kimi-k2.6)
	ReviewModel string `yaml:"review_model"` // default review/judge model (prefer a stronger one)
}

func path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".axprobe", "config.yaml")
}

// Load reads ~/.axprobe/config.yaml. A missing or unparseable file yields a zero
// Config — the file is purely optional.
func Load() Config {
	var c Config
	p := path()
	if p == "" {
		return c
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	_ = yaml.Unmarshal(data, &c)
	return c
}

// ResolveDriver resolves the driver runtime: flag > AXPROBE_DRIVER >
// workspaceDefault > config.driver > axprobe.
func ResolveDriver(flag, workspaceDefault string) string {
	return firstNonEmpty(flag, os.Getenv("AXPROBE_DRIVER"), workspaceDefault, Load().Driver, "axprobe")
}

// ResolveDriverModel resolves the driver model: flag > AXPROBE_DRIVER_MODEL >
// workspaceDefault > config.driver_model.
func ResolveDriverModel(flag, workspaceDefault string) string {
	return firstNonEmpty(flag, os.Getenv("AXPROBE_DRIVER_MODEL"), workspaceDefault, Load().DriverModel)
}

// ResolveReviewModel resolves the judge model: flag > AXPROBE_REVIEW_MODEL >
// workspaceDefault > config.review_model. It deliberately does NOT fall back to
// the driver model — an unset judge means "no LLM review" (mechanical scaffold),
// not "reuse the weak driver as judge".
func ResolveReviewModel(flag, workspaceDefault string) string {
	return firstNonEmpty(flag, os.Getenv("AXPROBE_REVIEW_MODEL"), workspaceDefault, Load().ReviewModel)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
