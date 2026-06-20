package box

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkdirMount verifies a mounted host workdir is the box's working dir and
// that files the tool generates land on the host (the live journey's value).
func TestWorkdirMount(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up a container)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	// Create the workdir under the user's home: Colima (and Docker Desktop) bind
	// the home dir but not /tmp or /var/folders (where t.TempDir lives).
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	dir, err := os.MkdirTemp(home, ".axprobe-wd-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	b := NewLocalDockerBox("ubuntu:24.04")
	b.Workdir = dir
	if err := b.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer b.Down()

	if _, err := b.Exec("echo generated > out.txt"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("file not on host: %v", err)
	}
	if strings.TrimSpace(string(data)) != "generated" {
		t.Fatalf("host file = %q, want %q", strings.TrimSpace(string(data)), "generated")
	}
}
