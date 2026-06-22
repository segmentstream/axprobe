package box

import (
	"os/exec"
	"strings"
	"testing"
)

// TestExecArgsReAddsImagePATH is a fast unit check: when the image PATH was
// captured at Up, execArgs restores it in the login shell, and is a no-op
// otherwise (backward compatible).
func TestExecArgsReAddsImagePATH(t *testing.T) {
	b := &LocalDockerBox{containerID: "x", basePath: "/usr/local/go/bin:/usr/bin"}
	cmd := lastArg(b.execArgs("go version"))
	if !strings.Contains(cmd, `export PATH="$PATH:/usr/local/go/bin:/usr/bin"`) {
		t.Fatalf("execArgs did not re-add image PATH: %q", cmd)
	}
	if !strings.HasSuffix(cmd, "go version") {
		t.Fatalf("execArgs dropped the command: %q", cmd)
	}

	b2 := &LocalDockerBox{containerID: "x"} // no basePath
	if got := lastArg(b2.execArgs("go version")); got != "go version" {
		t.Fatalf("execArgs altered command when no basePath: %q", got)
	}
}

// TestImagePATHToolFound is the integration proof of the fix: a tool installed by
// the image on its own ENV PATH (`go` in the golang image, at /usr/local/go/bin)
// is found in axprobe's login shell WITHOUT a hardcoded absolute path — i.e. the
// login shell no longer silently drops the image's PATH.
func TestImagePATHToolFound(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (brings up a container)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	b := NewLocalDockerBox("golang:1.26")
	if err := b.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	defer b.Down()

	res, err := b.Exec("go version")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("`go version` not found via login shell (exit %d): %s%s", res.ExitCode, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "go version") {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
}

func lastArg(args []string) string { return args[len(args)-1] }
