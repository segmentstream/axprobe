package box

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// containerWorkdir is where a mounted host workdir lands inside the box, and the
// working directory commands run from when one is mounted.
const containerWorkdir = "/workspace"

// LocalDockerBox runs commands inside a throwaway container on the local Docker
// daemon. The container is started detached with a long-lived no-op command so
// the harness can `docker exec` into it repeatedly, then force-removed on Down.
type LocalDockerBox struct {
	Image string
	Ports []int // loopback ports to publish (host 127.0.0.1:p -> box p)
	// HostDocker exposes the host Docker daemon inside the box. This is a powerful
	// opt-in escape hatch for tools whose normal runtime uses Docker.
	HostDocker bool
	// Workdir, if set, is a host directory bind-mounted into the box and used as
	// the working directory — the live journey's persistent, inspectable project.
	// It is never wiped by the harness; it is the user's real repo.
	Workdir string
	// containerWorkdir is where Workdir is mounted inside the box. It is normally
	// /workspace. When HostDocker is enabled, it becomes the host absolute path so
	// Docker commands run by the tool under test pass bind-mount paths the host
	// daemon can actually resolve.
	containerWorkdir string
	// hostDockerHome is a host-resolvable HOME mounted into the box when HostDocker
	// is enabled. Tools that start child Docker containers often bind-mount files
	// from HOME; those paths must exist from the host daemon's point of view.
	hostDockerHome string
	containerID    string
	// basePath is the image's own $PATH, captured from a non-login shell at Up.
	// Commands run in a login shell (so profile.d-installed tools are found), which
	// rebuilds PATH from /etc/profile and drops the image's ENV PATH; we re-add
	// basePath so image-installed tools (e.g. `go` in the golang image) are found
	// without fixtures hardcoding absolute paths.
	basePath string
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
	// Preflight: a clean, actionable error beats the raw exec failure an agent
	// finds cryptic. Check both that the binary exists AND that the daemon is
	// reachable — a present `docker` whose daemon is down otherwise fails deep in
	// `docker run` with a low-level message.
	if b.HostDocker {
		home, err := prepareHostDockerHome()
		if err != nil {
			return err
		}
		b.hostDockerHome = home
		defer func() {
			if b.containerID == "" && b.hostDockerHome != "" {
				_ = os.RemoveAll(b.hostDockerHome)
				b.hostDockerHome = ""
			}
		}()
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("Docker is required to run commands in a disposable box, but `docker` was not found on PATH — install Docker (https://docs.docker.com/get-docker/) and make sure the daemon is running")
	}
	if _, stderr, err := capture("docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("Docker is installed but its daemon is not reachable — start Docker (Docker Desktop / colima / dockerd) and try again (%s)", msg)
	}
	args := []string{"run", "-d"}
	for _, p := range b.Ports {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", p, p))
	}
	if b.Workdir != "" {
		abs, err := filepath.Abs(b.Workdir)
		if err != nil {
			return fmt.Errorf("workdir: %w", err)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return fmt.Errorf("workdir: %w", err)
		}
		b.containerWorkdir = b.mountTarget(abs)
		args = append(args, "-v", abs+":"+b.containerWorkdir, "-w", b.containerWorkdir)
	}
	if b.HostDocker {
		args = append(args,
			"-v", "/var/run/docker.sock:/var/run/docker.sock",
			"-e", "DOCKER_HOST=unix:///var/run/docker.sock",
		)
		if b.hostDockerHome != "" {
			args = append(args,
				"-v", b.hostDockerHome+":"+b.hostDockerHome,
				"-e", "HOME="+b.hostDockerHome,
				"-e", "AXPROBE_HOME="+b.hostDockerHome,
			)
		}
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
	// Capture the image's own PATH from a NON-login shell (which honors the image's
	// ENV PATH). execArgs re-adds it to the login shell so image-installed tools
	// are found. Best-effort: on failure we fall back to the plain login PATH.
	if out, _, err := capture("docker", "exec", b.containerID, "sh", "-c", `printf %s "$PATH"`); err == nil {
		b.basePath = strings.TrimSpace(out)
	}
	if err := b.startLoopbackRelays(); err != nil {
		return err
	}
	return nil
}

func (b *LocalDockerBox) mountTarget(absWorkdir string) string {
	if b.HostDocker {
		return absWorkdir
	}
	return containerWorkdir
}

func prepareHostDockerHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("host docker home: find user home: %w", err)
	}
	parent := filepath.Join(home, ".axprobe", "tmp")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("host docker home: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "home-")
	if err != nil {
		return "", fmt.Errorf("host docker home: %w", err)
	}
	return dir, nil
}

// startLoopbackRelays makes a published port reach a server that binds the
// container's loopback. Docker forwards host:PORT to the container's eth0, but an
// oauth loopback server binds 127.0.0.1:PORT inside the box — traffic arriving on
// eth0 can't reach it, so the browser callback fails ("this page isn't working").
// A socat relay bound to the container's own IP bridges eth0:PORT -> 127.0.0.1:PORT.
// It binds the container IP (not 0.0.0.0), so it never conflicts with the app's
// own 127.0.0.1 bind. No-op when no ports are published.
func (b *LocalDockerBox) startLoopbackRelays() error {
	if len(b.Ports) == 0 {
		return nil
	}
	if _, stderr, err := capture("docker", "exec", b.containerID, "sh", "-c",
		"command -v socat >/dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq socat >/dev/null 2>&1)"); err != nil {
		return fmt.Errorf("install socat for loopback relay: %w: %s", err, strings.TrimSpace(stderr))
	}
	ip, _, err := capture("docker", "exec", b.containerID, "sh", "-c", "hostname -I | awk '{print $1}'")
	if err != nil {
		return fmt.Errorf("resolve container ip for relay: %w", err)
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return fmt.Errorf("loopback relay: empty container ip")
	}
	for _, p := range b.Ports {
		cmd := fmt.Sprintf("socat TCP-LISTEN:%d,fork,reuseaddr,bind=%s TCP:127.0.0.1:%d", p, ip, p)
		if err := exec.Command("docker", "exec", "-d", b.containerID, "sh", "-c", cmd).Run(); err != nil {
			return fmt.Errorf("start loopback relay on port %d: %w", p, err)
		}
	}
	return nil
}

// execArgs builds the `docker exec` argv, running from the mounted workdir when
// one is set so the tool generates into the host project.
func (b *LocalDockerBox) execArgs(cmd string) []string {
	a := []string{"exec"}
	if b.Workdir != "" {
		a = append(a, "-w", b.containerWorkdir)
	}
	// Login shell (-l) so profile.d-installed tools are on PATH; but the login
	// shell rebuilds PATH from /etc/profile and drops the image's ENV PATH, so we
	// re-append the captured image PATH (union of both) — no fixture PATH hacks.
	if b.basePath != "" {
		cmd = `export PATH="$PATH:` + b.basePath + `"; ` + cmd
	}
	if b.hostDockerHome != "" {
		cmd = `export HOME=` + shellQuote(b.hostDockerHome) + ` AXPROBE_HOME=` + shellQuote(b.hostDockerHome) + `; ` + cmd
	}
	return append(a, b.containerID, "sh", "-lc", cmd)
}

// Exec runs `sh -lc <cmd>` inside the container. A non-zero exit is reported in
// the ExecResult, not as an error.
func (b *LocalDockerBox) Exec(cmd string) (ExecResult, error) {
	if b.containerID == "" {
		return ExecResult{}, fmt.Errorf("box is not up")
	}

	c := exec.Command("docker", b.execArgs(cmd)...)
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
	c := exec.Command("docker", b.execArgs(cmd)...)
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

// NewCommandBridge creates a host-side axprobe-bash shim for external drivers.
// The shim executes commands inside this box and logs command/result blocks for
// report reconstruction.
func (b *LocalDockerBox) NewCommandBridge() (*CommandBridge, error) {
	if b.containerID == "" {
		return nil, fmt.Errorf("box is not up")
	}
	dir, err := os.MkdirTemp("", "axprobe-bridge-")
	if err != nil {
		return nil, err
	}
	bridge := &CommandBridge{
		Dir:      dir,
		BashPath: filepath.Join(dir, "axprobe-bash"),
		LogPath:  filepath.Join(dir, "transcript.log"),
	}
	bridge.Cleanup = func() { _ = os.RemoveAll(dir) }
	dockerPrefix := "docker exec"
	if b.Workdir != "" {
		dockerPrefix += " -w " + shellQuote(b.containerWorkdir)
	}
	dockerPrefix += " " + shellQuote(b.containerID) + " sh -lc"
	cmdPrefix := ""
	if b.basePath != "" {
		cmdPrefix += `export PATH="$PATH:` + b.basePath + `"; `
	}
	if b.hostDockerHome != "" {
		cmdPrefix += `export HOME=` + shellQuote(b.hostDockerHome) + ` AXPROBE_HOME=` + shellQuote(b.hostDockerHome) + `; `
	}
	script := fmt.Sprintf(`#!/bin/sh
set +e
if [ "$#" -eq 0 ]; then
  echo "usage: axprobe-bash '<command>'" >&2
  exit 2
fi
if [ "$1" = "--help" ] || [ "$1" = "-h" ]; then
  echo "usage: axprobe-bash '<command>'"
  echo "Runs the command inside the AXprobe disposable box and records it in the run transcript."
  exit 0
fi
cmd="$*"
tmp="$(mktemp)"
log=%s
prefix=%s
printf '\n<<<AXPROBE-CMD>>>\n%%s\n' "$cmd" >> "$log"
%s "${prefix}${cmd}" > "$tmp" 2>&1
code=$?
cat "$tmp"
printf '<<<AXPROBE-EXIT:%%s>>>\n' "$code" >> "$log"
cat "$tmp" >> "$log"
printf '\n<<<AXPROBE-END>>>\n' >> "$log"
rm -f "$tmp"
exit "$code"
`, shellQuote(bridge.LogPath), shellQuote(cmdPrefix), dockerPrefix)
	if err := os.WriteFile(bridge.BashPath, []byte(script), 0o755); err != nil {
		bridge.Cleanup()
		return nil, err
	}
	return bridge, nil
}

// ArchiveOut tars the given paths from the box (relative to /) and returns the
// gzipped bytes.
func (b *LocalDockerBox) ArchiveOut(paths []string) ([]byte, error) {
	if b.containerID == "" {
		return nil, fmt.Errorf("box is not up")
	}
	if b.hostDockerHome != "" {
		if err := b.syncHomeForArchiveOut(); err != nil {
			return nil, err
		}
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
	if b.hostDockerHome != "" {
		if err := b.syncHomeAfterArchiveIn(); err != nil {
			return err
		}
	}
	return nil
}

func (b *LocalDockerBox) syncHomeAfterArchiveIn() error {
	cmd := `mkdir -p "$HOME"; if [ -d /root ]; then for p in /root/.[!.]* /root/..?* /root/*; do [ -e "$p" ] || continue; cp -a "$p" "$HOME"/; done; fi`
	if _, stderr, err := capture("docker", "exec", b.containerID, "sh", "-c", cmd); err != nil {
		return fmt.Errorf("sync restored credentials into host docker home: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

func (b *LocalDockerBox) syncHomeForArchiveOut() error {
	cmd := `mkdir -p /root; if [ -d "$HOME" ]; then for p in "$HOME"/.[!.]* "$HOME"/..?* "$HOME"/*; do [ -e "$p" ] || continue; cp -a "$p" /root/; done; fi`
	if _, stderr, err := capture("docker", "exec", b.containerID, "sh", "-c", cmd); err != nil {
		return fmt.Errorf("sync host docker home before credential capture: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// CopyIn writes content to destPath inside the container via `docker cp`, after
// ensuring the parent directory exists. Content is staged in a host temp file so
// it never appears on a command line.
// CopyFileIn copies a host file into the box at destPath, preserving its mode
// (so a compiled binary stays executable). This is box.copy — getting a prebuilt
// tool into the box without mounting the whole project.
func (b *LocalDockerBox) CopyFileIn(hostPath, destPath string) error {
	if b.containerID == "" {
		return fmt.Errorf("box is not up")
	}
	if _, err := os.Stat(hostPath); err != nil {
		return fmt.Errorf("host file %q: %w", hostPath, err)
	}
	if _, stderr, err := capture("docker", "exec", b.containerID, "mkdir", "-p", path.Dir(destPath)); err != nil {
		return fmt.Errorf("mkdir in box: %w: %s", err, strings.TrimSpace(stderr))
	}
	// docker cp preserves the source file's mode, including the executable bit.
	if _, stderr, err := capture("docker", "cp", hostPath, b.containerID+":"+destPath); err != nil {
		return fmt.Errorf("docker cp: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

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
	home := b.hostDockerHome
	b.containerID = ""
	b.hostDockerHome = ""
	if _, stderr, err := capture("docker", "rm", "-f", id); err != nil {
		return fmt.Errorf("docker rm: %w: %s", err, strings.TrimSpace(stderr))
	}
	if home != "" {
		if err := os.RemoveAll(home); err != nil {
			return fmt.Errorf("remove host docker home: %w", err)
		}
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
