package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/segmentstream/axprobe/internal/manifest"
)

func TestClearWorkspacePaths(t *testing.T) {
	wd := t.TempDir()
	mustWrite(t, filepath.Join(wd, "sources/vercel/events.sql"), "x") // scenario output → clear
	mustWrite(t, filepath.Join(wd, "keep.txt"), "keep")               // sibling → keep

	outside := filepath.Join(filepath.Dir(wd), "axprobe-outside.txt")
	mustWrite(t, outside, "safe")
	defer os.Remove(outside)

	// declared output, an escape attempt, and the workspace itself
	clearWorkspacePaths(wd, []string{"sources/vercel", "../" + filepath.Base(outside), "."})

	if _, err := os.Stat(filepath.Join(wd, "sources/vercel")); !os.IsNotExist(err) {
		t.Error("declared in-workspace path should be removed")
	}
	if _, err := os.Stat(filepath.Join(wd, "keep.txt")); err != nil {
		t.Error("sibling file must remain")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("escaping path must NOT be removed")
	}
}

func TestCheckWorkspaceSecretsIgnoresTemplates(t *testing.T) {
	wd := t.TempDir()
	mustWrite(t, filepath.Join(wd, ".env.example"), "OPENROUTER_API_KEY=example\n")
	if err := checkWorkspaceSecrets(wd); err != nil {
		t.Fatalf(".env.example should not block workdir mount: %v", err)
	}

	mustWrite(t, filepath.Join(wd, ".env"), "OPENROUTER_API_KEY=real\n")
	if err := checkWorkspaceSecrets(wd); err == nil {
		t.Fatal(".env should block workdir mount")
	}
}

// A run with no driver model resolvable (no flag, env, or config default) must
// fail fast with a clear message — before any box startup — naming the ways to
// set one. Isolate HOME and env so no machine-level default leaks in.
func TestCmdRunRequiresDriverModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AXPROBE_DRIVER_MODEL", "")

	wd := t.TempDir()
	manifest := filepath.Join(wd, "scenario.yaml")
	if err := os.WriteFile(manifest, []byte(`schema_version: "1"
name: no-model
goal: Do a thing.
box:
  image: ubuntu:24.04
`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := cmdRun(manifest, "", "", false, "", false, false)
	if err == nil {
		t.Fatal("expected a run with no driver model to fail before box startup")
	}
	if got := err.Error(); !strings.Contains(got, "no driver model") {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestDefaultReportPathUsesRunStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := defaultReportPath("my scenario")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(home, ".axprobe", "runs") + string(os.PathSeparator)
	if !strings.HasPrefix(path, wantPrefix) {
		t.Fatalf("report path %q should be under %q", path, wantPrefix)
	}
	if filepath.Base(path) != "report.json" {
		t.Fatalf("report filename = %q, want report.json", filepath.Base(path))
	}
	runDir := filepath.Base(filepath.Dir(path))
	if !strings.Contains(runDir, "my-scenario") {
		t.Fatalf("run dir %q should include scenario slug", runDir)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("run dir was not created: %v", err)
	}
}

func TestPrepareWorkspaceCopiesScenarioTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := t.TempDir()
	manifestDir := filepath.Join(root, ".axprobe")
	templateDir := filepath.Join(manifestDir, "fixtures", "empty-project")
	mustWrite(t, filepath.Join(templateDir, "segmentstream.yml"), "version: 1\n")
	mustWrite(t, filepath.Join(templateDir, "nested", "model.sql"), "select 1\n")
	manifestPath := filepath.Join(manifestDir, "source.yaml")

	m := &manifest.Manifest{
		Name:      "source",
		Workspace: &manifest.Workspace{Template: "fixtures/empty-project"},
	}
	workdir, cleanup, err := prepareWorkspace(manifestPath, m, "", false)
	if err != nil {
		t.Fatal(err)
	}

	if workdir == "" || workdir == templateDir {
		t.Fatalf("expected copied temp workspace, got %q", workdir)
	}
	wantPrefix := filepath.Join(home, ".axprobe", "tmp") + string(os.PathSeparator)
	if !strings.HasPrefix(workdir, wantPrefix) {
		t.Fatalf("fixture workspace %q should be under %q", workdir, wantPrefix)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "segmentstream.yml")); err != nil || string(got) != "version: 1\n" {
		t.Fatalf("copied segmentstream.yml = %q, %v", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "nested", "model.sql")); err != nil || string(got) != "select 1\n" {
		t.Fatalf("copied nested file = %q, %v", string(got), err)
	}

	cleanup()
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("fixture workspace should be removed by cleanup, stat err = %v", err)
	}
}

func TestPrepareWorkspaceLiveWorkdirOverridesTemplate(t *testing.T) {
	live := t.TempDir()
	m := &manifest.Manifest{
		Name:      "source",
		Workspace: &manifest.Workspace{Template: "fixtures/empty-project"},
	}
	workdir, cleanup, err := prepareWorkspace(filepath.Join(t.TempDir(), ".axprobe", "source.yaml"), m, live, false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if workdir != live {
		t.Fatalf("workdir = %q, want live workdir %q", workdir, live)
	}
}

func TestPrepareWorkspaceRejectsTemplateEscape(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, ".axprobe")
	outsideTemplate := filepath.Join(root, "fixtures", "outside")
	mustWrite(t, filepath.Join(outsideTemplate, "file.txt"), "x")

	m := &manifest.Manifest{
		Name:      "source",
		Workspace: &manifest.Workspace{Template: "../fixtures/outside"},
	}
	workdir, cleanup, err := prepareWorkspace(filepath.Join(manifestDir, "source.yaml"), m, "", false)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatalf("expected template escape to fail, got workdir %q", workdir)
	}
	if !strings.Contains(err.Error(), "must stay inside") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
