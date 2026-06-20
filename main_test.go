package main

import (
	"os"
	"path/filepath"
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

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
