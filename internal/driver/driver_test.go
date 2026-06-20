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
		"segmentstream warehouse auth login --json":                      true,
		"/root/.segmentstream/bin/segmentstream warehouse auth login":    true, // full path
		"segmentstream warehouse browse --json":                          false,
		"segmentstream warehouse auth login --help":                      true, // matched here; help guard is separate
	}
	for cmd, want := range cases {
		if _, got := declaredLogin(m, cmd); got != want {
			t.Errorf("declaredLogin(%q) = %v, want %v", cmd, got, want)
		}
	}
}
