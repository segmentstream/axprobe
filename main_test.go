package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClearWorkdirPaths(t *testing.T) {
	wd := t.TempDir()
	mustWrite(t, filepath.Join(wd, "sources/vercel/events.sql"), "x") // scenario output → clear
	mustWrite(t, filepath.Join(wd, "keep.txt"), "keep")               // sibling → keep

	outside := filepath.Join(filepath.Dir(wd), "axprobe-outside.txt")
	mustWrite(t, outside, "safe")
	defer os.Remove(outside)

	// declared output, an escape attempt, and the workdir itself
	clearWorkdirPaths(wd, []string{"sources/vercel", "../" + filepath.Base(outside), "."})

	if _, err := os.Stat(filepath.Join(wd, "sources/vercel")); !os.IsNotExist(err) {
		t.Error("declared in-workdir path should be removed")
	}
	if _, err := os.Stat(filepath.Join(wd, "keep.txt")); err != nil {
		t.Error("sibling file must remain")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("escaping path must NOT be removed")
	}
}

func TestCheckWorkdirSecretsIgnoresTemplates(t *testing.T) {
	wd := t.TempDir()
	mustWrite(t, filepath.Join(wd, ".env.example"), "OPENROUTER_API_KEY=example\n")
	if err := checkWorkdirSecrets(wd); err != nil {
		t.Fatalf(".env.example should not block workdir mount: %v", err)
	}

	mustWrite(t, filepath.Join(wd, ".env"), "OPENROUTER_API_KEY=real\n")
	if err := checkWorkdirSecrets(wd); err == nil {
		t.Fatal(".env should block workdir mount")
	}
}

func TestCmdRunRejectsSetupOnlyScenario(t *testing.T) {
	wd := t.TempDir()
	manifest := filepath.Join(wd, "setup-only.yaml")
	if err := os.WriteFile(manifest, []byte(`schema_version: "1"
name: setup-only
goal: This would only run setup.
box:
  image: ubuntu:24.04
`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := cmdRun(manifest, "", "", false, "", false)
	if err == nil {
		t.Fatal("expected setup-only scenario to fail before box startup")
	}
	if got := err.Error(); !strings.Contains(got, "this would only run setup") {
		t.Fatalf("unexpected error: %s", got)
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
