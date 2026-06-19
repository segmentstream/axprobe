package box

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
)

// LocalDockerBox runs commands inside a throwaway container on the local Docker
// daemon. The container is started detached with a long-lived no-op command so
// the harness can `docker exec` into it repeatedly, then force-removed on Down.
type LocalDockerBox struct {
	Image       string
	Ports       []int // loopback ports to publish (host 127.0.0.1:p -> box p)
	containerID string
}

// NewLocalDockerBox returns a box backed by the given image (e.g. "ubuntu:24.04").
// Any ports are published on the host loopback so a browser on the host can reach
// a server the tool starts inside the box (oauth loopback callback).
func NewLocalDockerBox(image string, ports ...int) *LocalDockerBox {
	return &LocalDockerBox{Image: image, Ports: ports}
}

// Up starts a detached container kept alive by `sleep infinity` so we can exec
// into it. Each Up is a fresh container — that is what makes a run "from scratch".
func (b *LocalDockerBox) Up() error {
	args := []string{"run", "-d"}
	for _, p := range b.Ports {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", p, p))
	}
	args = append(args, b.Image, "sleep", "infinity")

	// Capture stdout and stderr separately: on a fresh image, `docker run -d`
	// writes pull progress to stderr and the container ID to stdout. Merging
	// them would corrupt the ID.
	stdout, stderr, err := capture("docker", args...)
	if err != nil {
		return fmt.Errorf("docker run: %w: %s", err, strings.TrimSpace(stdout+stderr))
	}
	b.containerID = strings.TrimSpace(stdout)
	if b.containerID == "" {
		return fmt.Errorf("docker run: empty container id; stderr: %s", strings.TrimSpace(stderr))
	}
	return nil
}

// Exec runs `sh -lc <cmd>` inside the container. A non-zero exit is reported in
// the ExecResult, not as an error.
func (b *LocalDockerBox) Exec(cmd string) (ExecResult, error) {
	if b.containerID == "" {
		return ExecResult{}, fmt.Errorf("box is not up")
	}

	c := exec.Command("docker", "exec", b.containerID, "sh", "-lc", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	err := c.Run()
	res := ExecResult{Cmd: cmd, Stdout: stdout.String(), Stderr: stderr.String()}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil // command ran and failed — a fact, not a harness error
		}
		return res, fmt.Errorf("docker exec: %w", err)
	}
	return res, nil
}

// ExecStream runs `sh -lc <cmd>` inside the container with output streamed live
// to out, blocking until it exits.
func (b *LocalDockerBox) ExecStream(cmd string, out io.Writer) (ExecResult, error) {
	if b.containerID == "" {
		return ExecResult{}, fmt.Errorf("box is not up")
	}
	c := exec.Command("docker", "exec", b.containerID, "sh", "-lc", cmd)
	c.Stdout = out
	c.Stderr = out

	err := c.Run()
	res := ExecResult{Cmd: cmd}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("docker exec: %w", err)
	}
	return res, nil
}

// ArchiveOut tars the given paths from the box (relative to /) and returns the
// gzipped bytes.
func (b *LocalDockerBox) ArchiveOut(paths []string) ([]byte, error) {
	if b.containerID == "" {
		return nil, fmt.Errorf("box is not up")
	}
	args := []string{"exec", b.containerID, "tar", "czf", "-", "-C", "/"}
	for _, p := range paths {
		args = append(args, strings.TrimPrefix(p, "/"))
	}
	c := exec.Command("docker", args...)
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("tar out: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// ArchiveIn extracts a gzipped tar (from ArchiveOut) back into the box at /.
func (b *LocalDockerBox) ArchiveIn(data []byte) error {
	if b.containerID == "" {
		return fmt.Errorf("box is not up")
	}
	c := exec.Command("docker", "exec", "-i", b.containerID, "tar", "xzf", "-", "-C", "/")
	c.Stdin = bytes.NewReader(data)
	var errBuf bytes.Buffer
	c.Stderr = &errBuf
	if err := c.Run(); err != nil {
		return fmt.Errorf("tar in: %w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// CopyIn writes content to destPath inside the container via `docker cp`, after
// ensuring the parent directory exists. Content is staged in a host temp file so
// it never appears on a command line.
func (b *LocalDockerBox) CopyIn(content []byte, destPath string) error {
	if b.containerID == "" {
		return fmt.Errorf("box is not up")
	}

	if _, stderr, err := capture("docker", "exec", b.containerID, "mkdir", "-p", path.Dir(destPath)); err != nil {
		return fmt.Errorf("mkdir in box: %w: %s", err, strings.TrimSpace(stderr))
	}

	tmp, err := os.CreateTemp("", "axprobe-copyin-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	if _, stderr, err := capture("docker", "cp", tmp.Name(), b.containerID+":"+destPath); err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// Down force-removes the container. Idempotent.
func (b *LocalDockerBox) Down() error {
	if b.containerID == "" {
		return nil
	}
	id := b.containerID
	b.containerID = ""
	if _, stderr, err := capture("docker", "rm", "-f", id); err != nil {
		return fmt.Errorf("docker rm: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// capture runs a command and returns its stdout and stderr separately.
func capture(name string, args ...string) (stdout, stderr string, err error) {
	c := exec.Command(name, args...)
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	err = c.Run()
	return out.String(), errBuf.String(), err
}
