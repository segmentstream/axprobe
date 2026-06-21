// Package config loads optional operator-level defaults from
// ~/.axprobe/config.yaml. These are conveniences only: every value is
// overridable by an env var and then by a command-line flag (flag > env >
// config). The driver model (who is measured) and the review model (who judges)
// are resolved separately and on purpose — so the deliberately weak driver model
// is never silently reused as the judge.
//
// Model belongs here (an operator/runtime choice), NOT in a scenario manifest: a
// scenario is a spec that should run unchanged across many models.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the user-level defaults file (~/.axprobe/config.yaml).
type Config struct {
	Model       string `yaml:"model"`        // default driver model (e.g. moonshotai/kimi-k2.6)
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

// ResolveModel resolves the driver model: flag > AXPROBE_MODEL > config.model.
func ResolveModel(flag string) string {
	return firstNonEmpty(flag, os.Getenv("AXPROBE_MODEL"), Load().Model)
}

// ResolveReviewModel resolves the judge model: flag > AXPROBE_REVIEW_MODEL >
// config.review_model. It deliberately does NOT fall back to the driver model —
// an unset judge means "no LLM review" (mechanical scaffold), not "reuse the
// weak driver as judge".
func ResolveReviewModel(flag string) string {
	return firstNonEmpty(flag, os.Getenv("AXPROBE_REVIEW_MODEL"), Load().ReviewModel)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
