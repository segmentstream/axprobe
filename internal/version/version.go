// Package version carries the build version, stamped in at release time via
// -ldflags (see .goreleaser.yaml). A source/dev build reports "dev", which the
// updater's semver parser rejects — so a dev build can never self-update.
package version

import "runtime"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info is a snapshot of the running build, including the host platform (so the
// updater picks the right release asset).
type Info struct {
	Version string
	Commit  string
	Date    string
	OS      string
	Arch    string
}

// Current returns the running build's version info.
func Current() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}
