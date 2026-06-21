package broker

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/manifest"
	"github.com/segmentstream/axprobe/internal/secrets"
)

// fakeBox stands in for a real box: ExecStream mimics a device-flow login that
// prints a URL + code and then exits 0, so we can verify the resolver mechanics
// without Docker, an LLM, or a real account.
type fakeBox struct {
	streamedCmd string
	exitCode    int
}

func (f *fakeBox) Up() error                           { return nil }
func (f *fakeBox) Exec(string) (box.ExecResult, error) { return box.ExecResult{}, nil }
func (f *fakeBox) CopyIn([]byte, string) error         { return nil }
func (f *fakeBox) ArchiveOut([]string) ([]byte, error) { return nil, nil }
func (f *fakeBox) ArchiveIn([]byte) error              { return nil }
func (f *fakeBox) Down() error                         { return nil }
func (f *fakeBox) ExecStream(cmd string, out io.Writer) (box.ExecResult, error) {
	f.streamedCmd = cmd
	fmt.Fprintln(out, "! First copy your one-time code: WDJB-MJHT")
	fmt.Fprintln(out, "Open https://github.com/login/device and paste the code")
	fmt.Fprintln(out, "Authentication complete.")
	return box.ExecResult{ExitCode: f.exitCode}, nil
}

func TestResolveOAuthDevice(t *testing.T) {
	m := &manifest.Manifest{Credentials: []manifest.Credential{{
		Name:         "gh",
		Kind:         "oauth",
		LoginCommand: "gh auth login --web",
	}}}
	fb := &fakeBox{}
	var out bytes.Buffer
	br := New(m, fb, secrets.New("oauth-test"), false, strings.NewReader(""), &out)

	resume, ok := br.Resolve("need to authenticate with GitHub")
	if !ok {
		t.Fatalf("expected resolve to succeed; output:\n%s", out.String())
	}
	if fb.streamedCmd != "gh auth login --web" {
		t.Fatalf("login_command not run via ExecStream, got %q", fb.streamedCmd)
	}
	if !strings.Contains(out.String(), "WDJB-MJHT") || !strings.Contains(out.String(), "github.com/login/device") {
		t.Fatalf("URL/code not surfaced to host terminal:\n%s", out.String())
	}
	if !strings.Contains(resume, "completed") {
		t.Fatalf("unexpected resume message: %q", resume)
	}
}

func TestResolveOAuthDeviceFailsOnNonZeroExit(t *testing.T) {
	m := &manifest.Manifest{Credentials: []manifest.Credential{{
		Name: "gh", Kind: "oauth", LoginCommand: "gh auth login",
	}}}
	fb := &fakeBox{exitCode: 1}
	var out bytes.Buffer
	br := New(m, fb, secrets.New("oauth-test"), false, strings.NewReader(""), &out)

	if _, ok := br.Resolve("login"); ok {
		t.Fatal("expected resolve to fail when login command exits non-zero")
	}
}
