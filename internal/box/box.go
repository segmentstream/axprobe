package box

import "io"

// ExecResult captures the outcome of one command run inside a box.
//
// A non-zero ExitCode is a fact about the command, not an error of the harness:
// Exec returns (result, nil) even when the command exits non-zero. Exec only
// returns a non-nil error when the box itself could not run the command.
type ExecResult struct {
	Cmd      string
	ExitCode int
	Stdout   string
	Stderr   string
}

// Box is an isolated, disposable environment the harness drives a CLI inside.
//
// Layer 0 ships only LocalDockerBox. GithubRunnerBox (the CI runner is itself
// the box) and VMBox (native Docker Compose, no nesting) come later. Everything
// above this interface — manifest, driver, reporter — is box-agnostic, so the
// same scenario runs unchanged whichever box backs it.
type Box interface {
	// Up provisions a fresh, clean environment.
	Up() error

	// Exec runs a shell command inside the box and captures its result.
	Exec(cmd string) (ExecResult, error)

	// CopyIn writes content to destPath inside the box, creating parent dirs.
	// Used by the secret broker to inject a credential at the process level so
	// it never passes through the model's context.
	CopyIn(content []byte, destPath string) error

	// ExecStream runs a command inside the box with its output streamed live to
	// out, blocking until it exits. Used for interactive flows (oauth device
	// login) where the user must see a URL + code while the command keeps running.
	ExecStream(cmd string, out io.Writer) (ExecResult, error)

	// ArchiveOut tars the given (absolute) box paths and returns gzipped bytes.
	// Used to cache a tool's credential files after an oauth login.
	ArchiveOut(paths []string) ([]byte, error)

	// ArchiveIn extracts a gzipped tar produced by ArchiveOut back into the box.
	ArchiveIn(data []byte) error

	// Down tears the environment down. Safe to call more than once.
	Down() error
}
