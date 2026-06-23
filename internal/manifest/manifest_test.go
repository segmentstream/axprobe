package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// `probes:` was removed (axprobe manifests are LLM-driven). Load must reject a
// manifest that still declares them with a clear migration error, so an old
// fixture or an agent writing `probes:` is told to use a goal, not silently
// ignored.
func TestLoadRejectsProbes(t *testing.T) {
	p := writeManifest(t, `schema_version: "1"
name: legacy
box:
  image: ubuntu:24.04
probes:
  - echo hi
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected Load to reject a manifest with probes:, got nil error")
	}
	if !strings.Contains(err.Error(), "probes") {
		t.Fatalf("error should name probes: %v", err)
	}
}

// A normal LLM-driven manifest (a goal, no probes) still loads cleanly — guards
// against the rejection being too broad.
func TestLoadAcceptsGoalManifest(t *testing.T) {
	p := writeManifest(t, `schema_version: "1"
name: ok
box:
  image: ubuntu:24.04
goal: do a thing
`)
	m, err := Load(p)
	if err != nil {
		t.Fatalf("goal manifest should load: %v", err)
	}
	if m.Goal != "do a thing" {
		t.Fatalf("goal not parsed: %q", m.Goal)
	}
}
