package box

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyFileInPreservesMode verifies box.copy's mechanic: a prebuilt host
// binary copied into the box stays executable and is runnable by name (on PATH),
// without mounting the project — the fix for the "no way to inject a binary" gap.
func TestCopyFileInPreservesMode(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up a container)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	// docker cp (unlike a bind mount) works from anywhere, so t.TempDir is fine.
	binPath := filepath.Join(t.TempDir(), "mytool")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho copied-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := NewLocalDockerBox("ubuntu:24.04")
	if err := b.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer b.Down()

	if err := b.CopyFileIn(binPath, "/usr/local/bin/mytool"); err != nil {
		t.Fatalf("CopyFileIn: %v", err)
	}
	res, err := b.Exec("mytool") // by name → proves it is on PATH and executable
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d: %s%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "copied-ok") {
		t.Fatalf("stdout = %q, want copied-ok", res.Stdout)
	}
}

// A missing host file is reported clearly, not as an opaque docker error.
func TestCopyFileInMissingHostFile(t *testing.T) {
	b := NewLocalDockerBox("ubuntu:24.04")
	b.containerID = "fake" // skip Up(); the host-file check runs first
	err := b.CopyFileIn("/no/such/file", "/usr/local/bin/x")
	if err == nil || !strings.Contains(err.Error(), "host file") {
		t.Fatalf("want a clear host-file error, got %v", err)
	}
}
