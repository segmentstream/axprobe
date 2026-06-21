package driver

import (
	"testing"

	"github.com/segmentstream/axprobe/internal/manifest"
)

func TestDeclaredLoginMatchesFullPath(t *testing.T) {
	m := &manifest.Manifest{Credentials: []manifest.Credential{{
		Name: "bq", Kind: "oauth",
		LoginCommand: "segmentstream warehouse auth login --port 8085",
	}}}
	cases := map[string]bool{
		"segmentstream warehouse auth login --json":                   true,
		"/root/.segmentstream/bin/segmentstream warehouse auth login": true, // full path
		"segmentstream warehouse browse --json":                       false,
		"segmentstream warehouse auth login --help":                   true, // matched here; help guard is separate
	}
	for cmd, want := range cases {
		if _, got := declaredLogin(m, cmd); got != want {
			t.Errorf("declaredLogin(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestRepeatedNoProgress(t *testing.T) {
	ts := []Step{
		{Command: "tool init --json", Result: "ready: false"},
		{Command: "tool configure", Result: "valid"},
		{Command: "tool init --json", Result: "ready: false"}, // same cmd+result as #1
	}
	// init --json with the SAME result has occurred twice; a third makes 3 → stuck.
	if n := repeatedNoProgress(ts, "tool   init   --json", "ready: false"); n != 2 {
		t.Errorf("repeatedNoProgress = %d, want 2 (normalized match, same result)", n)
	}
	// A verify command whose result CHANGED is progress, not a loop.
	if n := repeatedNoProgress(ts, "tool init --json", "ready: true"); n != 0 {
		t.Errorf("changed result should not count as repeat; got %d", n)
	}
}
