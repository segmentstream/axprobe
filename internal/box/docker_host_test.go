package box

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostDockerSocket verifies the opt-in Docker-outside-of-Docker path:
// a box can talk to the host Docker daemon when HostDocker is enabled. This is
// intentionally explicit because the Docker socket is a high-privilege mount.
func TestHostDockerSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up a container)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	b := NewLocalDockerBox("docker:27-cli")
	b.HostDocker = true
	if err := b.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer b.Down()

	res, err := b.Exec(`test "$DOCKER_HOST" = "unix:///var/run/docker.sock" && docker version --format '{{.Server.Version}}'`)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("docker host access failed (exit %d): %s%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		t.Fatalf("expected docker server version, got empty stdout")
	}
}

func TestHostDockerWorkdirIsHostResolvable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up containers)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	dir, err := os.MkdirTemp(home, ".axprobe-docker-wd-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "sentinel.txt"), []byte("visible to child docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := NewLocalDockerBox("docker:27-cli")
	b.HostDocker = true
	b.Workdir = dir
	if err := b.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer b.Down()

	res, err := b.Exec(`test "$(pwd)" = "` + dir + `" && docker run --rm -v "$PWD:/probe" docker:27-cli cat /probe/sentinel.txt`)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("nested docker bind mount failed (exit %d): %s%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "visible to child docker" {
		t.Fatalf("child docker saw %q", strings.TrimSpace(res.Stdout))
	}

	res, err = b.Exec(`mkdir -p "$HOME/.tool" && printf credential > "$HOME/.tool/token" && docker run --rm -v "$HOME/.tool:/creds:ro" docker:27-cli cat /creds/token`)
	if err != nil {
		t.Fatalf("Exec HOME bind: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("nested docker HOME bind mount failed (exit %d): %s%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "credential" {
		t.Fatalf("child docker saw HOME credential %q", strings.TrimSpace(res.Stdout))
	}
}

func TestCommandBridgeRunsInBoxAndLogsTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up a container)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	b := NewLocalDockerBox("ubuntu:24.04")
	if err := b.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer b.Down()

	bridge, err := b.NewCommandBridge()
	if err != nil {
		t.Fatalf("NewCommandBridge: %v", err)
	}
	defer bridge.Cleanup()

	help, err := exec.Command(bridge.BashPath, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("bridge help: %v: %s", err, help)
	}
	if !strings.Contains(string(help), "disposable box") {
		t.Fatalf("bridge help should describe box execution, got: %s", help)
	}

	out, err := exec.Command(bridge.BashPath, "printf bridge-ok").CombinedOutput()
	if err != nil {
		t.Fatalf("bridge command: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "bridge-ok" {
		t.Fatalf("bridge output = %q", strings.TrimSpace(string(out)))
	}
	log, err := os.ReadFile(bridge.LogPath)
	if err != nil {
		t.Fatalf("read bridge log: %v", err)
	}
	if !strings.Contains(string(log), "printf bridge-ok") || !strings.Contains(string(log), "<<<AXPROBE-EXIT:0>>>") {
		t.Fatalf("bridge log missing command/exit markers:\n%s", log)
	}
}
