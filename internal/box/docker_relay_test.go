package box

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestLoopbackRelay verifies startLoopbackRelays (run from Up): a published port
// must end up served by a relay bound to the container IP, so docker's eth0
// forward reaches a server that binds the container loopback. Needs Docker.
func TestLoopbackRelay(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up a container)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	const port = 8137
	b := NewLocalDockerBox("ubuntu:24.04", port)
	if err := b.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer b.Down()

	res, err := b.Exec(fmt.Sprintf(
		"apt-get install -y -qq iproute2 >/dev/null 2>&1; ss -ltn 2>/dev/null | grep ':%d' || true", port))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	// Some line must listen on :port at an address that is NOT loopback — that is
	// the relay on the container IP (a 127.0.0.1 bind would be the app, unreachable
	// from the host forward).
	relayUp := false
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.Contains(line, fmt.Sprintf(":%d", port)) && !strings.Contains(line, "127.0.0.1:") {
			relayUp = true
		}
	}
	if !relayUp {
		t.Fatalf("no relay listening on container IP:%d\nss output:\n%s", port, res.Stdout)
	}
}
