package box

import (
	"os/exec"
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
