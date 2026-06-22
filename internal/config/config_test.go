package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	// An empty HOME means no config file, so resolution reduces to flag > env.
	t.Setenv("HOME", t.TempDir())

	t.Setenv("AXPROBE_DRIVER_MODEL", "env-driver")
	t.Setenv("AXPROBE_REVIEW_MODEL", "env-judge")

	if got := ResolveDriverModel("flag-driver", "workspace-driver"); got != "flag-driver" {
		t.Fatalf("flag must win: got %q", got)
	}
	if got := ResolveDriverModel("", "workspace-driver"); got != "env-driver" {
		t.Fatalf("env must win when no flag: got %q", got)
	}
	if got := ResolveReviewModel("flag-judge", "workspace-judge"); got != "flag-judge" {
		t.Fatalf("review flag must win: got %q", got)
	}
	if got := ResolveReviewModel("", "workspace-judge"); got != "env-judge" {
		t.Fatalf("review env must win when no flag: got %q", got)
	}
}

func TestResolveFromConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AXPROBE_DRIVER_MODEL", "")
	t.Setenv("AXPROBE_REVIEW_MODEL", "")
	if err := writeConfig(home, "driver_model: cfg-driver\nreview_model: cfg-judge\n"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveDriverModel("", ""); got != "cfg-driver" {
		t.Fatalf("config driver_model: got %q", got)
	}
	if got := ResolveReviewModel("", ""); got != "cfg-judge" {
		t.Fatalf("config review_model: got %q", got)
	}
	if got := ResolveDriverModel("", "workspace-driver"); got != "workspace-driver" {
		t.Fatalf("workspace driver_model must beat user config: got %q", got)
	}
	if got := ResolveReviewModel("", "workspace-judge"); got != "workspace-judge" {
		t.Fatalf("workspace review_model must beat user config: got %q", got)
	}
	// Review must NOT fall back to the driver model when review_model is unset.
	if err := writeConfig(home, "driver_model: cfg-driver\n"); err != nil {
		t.Fatal(err)
	}
	if got := ResolveReviewModel("", ""); got != "" {
		t.Fatalf("review must not inherit driver model: got %q", got)
	}
}

func writeConfig(home, body string) error {
	dir := filepath.Join(home, ".axprobe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o644)
}
