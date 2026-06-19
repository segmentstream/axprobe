// Package broker is the Layer 3 secret broker. When the driver hits a gate, the
// broker provides the next declared credential — from the store if cached, else
// by prompting the user once — injects it into the box, and lets the run resume.
// The secret never enters the model's context: the driver only learns that a
// named credential is now available at a path/env inside the box.
package broker

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/manifest"
	"github.com/segmentstream/axprobe/internal/secrets"
)

// Broker collects, stores and injects credentials for one run.
type Broker struct {
	creds      []manifest.Credential
	provided   map[string]bool
	box        box.Box
	store      *secrets.Store
	unattended bool
	in         *bufio.Reader
	out        io.Writer
}

// New builds a broker. in/out are the user-facing prompt streams (stdin/stdout).
// In unattended mode a gate that cannot be satisfied from a cached/provisioned
// credential stops the run instead of prompting (for CI).
func New(m *manifest.Manifest, b box.Box, store *secrets.Store, unattended bool, in io.Reader, out io.Writer) *Broker {
	return &Broker{
		creds:      m.Credentials,
		provided:   map[string]bool{},
		box:        b,
		store:      store,
		unattended: unattended,
		in:         bufio.NewReader(in),
		out:        out,
	}
}

// Prime restores any cached oauth tokens into the box before the run starts, so
// the tool is already authenticated and the agent never needs to log in.
func (br *Broker) Prime() {
	for _, c := range br.creds {
		if c.Kind == "oauth" && br.restoreToken(c) {
			br.provided[c.Name] = true
		}
	}
}

// Resolve tries to satisfy a gate by providing the next pending declared
// credential. It returns a resume message (for the model) and whether the run
// can continue. The message never contains the secret value.
func (br *Broker) Resolve(needs string) (string, bool) {
	cred, ok := br.nextPending()
	if !ok {
		return "", false
	}

	if cred.Kind == "oauth" {
		return br.resolveOAuth(cred)
	}

	value, err := br.obtain(cred)
	if err != nil {
		fmt.Fprintf(br.out, "  credential error: %v\n", err)
		return "", false
	}
	if err := br.inject(cred, value); err != nil {
		fmt.Fprintf(br.out, "  inject error: %v\n", err)
		return "", false
	}
	br.provided[cred.Name] = true
	return br.resumeMsg(cred), true
}

// resolveOAuth handles kind:oauth. Device mode (default): run login_command in
// the box with its output streamed live to the host so the user sees the URL +
// code, and block until it completes. No callback server, no port forwarding.
// (loopback mode is declared in the contract but not yet implemented.)
func (br *Broker) resolveOAuth(c manifest.Credential) (string, bool) {
	mode := c.Mode
	if mode == "" {
		mode = "device"
	}
	if mode != "device" && mode != "loopback" {
		fmt.Fprintf(br.out, "  oauth mode %q not supported (device|loopback) — stopping.\n", mode)
		return "", false
	}

	// Cache-first: a stored token skips the browser entirely.
	if br.restoreToken(c) {
		br.provided[c.Name] = true
		return fmt.Sprintf("Cached login for %q restored; the tool is authenticated. Continue toward the goal.", c.Name), true
	}
	if br.unattended {
		fmt.Fprintf(br.out, "  unattended: no cached token for %q and no browser — stopping.\n", c.Name)
		return "", false
	}
	if c.LoginCommand == "" {
		fmt.Fprintf(br.out, "  oauth credential %q has no login_command — stopping.\n", c.Name)
		return "", false
	}

	// For loopback, the callback port was published when the box started, so the
	// browser redirect to 127.0.0.1:<port> on the host reaches the box. Either
	// way the mechanic is the same: run the login, stream it, wait.
	fmt.Fprintf(br.out, "\n🔓 browser login required for %q (%s) — complete it when prompted below:\n", c.Name, mode)
	fmt.Fprintf(br.out, "   $ %s\n", c.LoginCommand)
	fmt.Fprintln(br.out, "   ──────── login output ────────")
	res, err := br.box.ExecStream(c.LoginCommand, br.out)
	fmt.Fprintln(br.out, "   ──────────────────────────────")
	if err != nil {
		fmt.Fprintf(br.out, "  oauth error: %v — stopping.\n", err)
		return "", false
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(br.out, "  login command exited %d — stopping.\n", res.ExitCode)
		return "", false
	}

	br.cacheToken(c) // extract + store the token so the next run needs no browser
	br.provided[c.Name] = true
	return fmt.Sprintf("Browser login %q completed in the sandbox; the tool is now authenticated. Continue toward the goal.", c.Name), true
}

// restoreToken injects a cached token for c into the box. Returns true on success.
func (br *Broker) restoreToken(c manifest.Credential) bool {
	if len(c.TokenPaths) == 0 {
		return false
	}
	data, ok := br.store.Get(tokenKey(c.Name))
	if !ok {
		return false
	}
	if err := br.box.ArchiveIn(data); err != nil {
		fmt.Fprintf(br.out, "  cached token restore failed: %v\n", err)
		return false
	}
	fmt.Fprintf(br.out, "  ↻ reused cached login for %q (no browser)\n", c.Name)
	return true
}

// cacheToken extracts c's token files from the box and stores them (Keychain only).
func (br *Broker) cacheToken(c manifest.Credential) {
	if len(c.TokenPaths) == 0 {
		return
	}
	data, err := br.box.ArchiveOut(c.TokenPaths)
	if err != nil {
		fmt.Fprintf(br.out, "  warning: could not capture token for %q: %v\n", c.Name, err)
		return
	}
	if err := br.store.SetKeychainOnly(tokenKey(c.Name), data); err != nil {
		fmt.Fprintf(br.out, "  warning: could not cache token for %q: %v\n", c.Name, err)
		return
	}
	fmt.Fprintf(br.out, "  ✓ cached login for %q (next run needs no browser)\n", c.Name)
}

func tokenKey(name string) string { return name + ".token" }

func (br *Broker) nextPending() (manifest.Credential, bool) {
	for _, c := range br.creds {
		if !br.provided[c.Name] {
			return c, true
		}
	}
	return manifest.Credential{}, false
}

// obtain returns the credential value from the store, or prompts the user once
// and stores it. The value is never echoed by us.
func (br *Broker) obtain(c manifest.Credential) ([]byte, error) {
	if v, ok := br.store.Get(c.Name); ok {
		fmt.Fprintf(br.out, "  using stored credential %q (%s)\n", c.Name, secrets.Backend())
		return v, nil
	}

	prompt := c.Prompt
	if prompt == "" {
		prompt = "Provide credential " + c.Name
	}

	switch c.Kind {
	case "file":
		fmt.Fprintf(br.out, "  🔐 %s\n     path> ", prompt)
		path := expandHome(strings.TrimSpace(br.readLine()))
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read file %q: %w", path, err)
		}
		if err := br.store.Set(c.Name, content); err != nil {
			fmt.Fprintf(br.out, "  warning: could not store credential: %v\n", err)
		}
		return content, nil

	case "value", "":
		fmt.Fprintf(br.out, "  🔐 %s\n     value> ", prompt)
		v := []byte(strings.TrimRight(br.readLine(), "\r\n"))
		if err := br.store.Set(c.Name, v); err != nil {
			fmt.Fprintf(br.out, "  warning: could not store credential: %v\n", err)
		}
		return v, nil

	default:
		return nil, fmt.Errorf("unknown credential kind %q", c.Kind)
	}
}

func (br *Broker) inject(c manifest.Credential, value []byte) error {
	switch {
	case c.Inject.BoxPath != "":
		return br.box.CopyIn(value, c.Inject.BoxPath)
	case c.Inject.Env != "":
		line := fmt.Sprintf("export %s=%s\n", c.Inject.Env, shellQuote(string(value)))
		return br.box.CopyIn([]byte(line), "/etc/profile.d/axprobe-"+c.Name+".sh")
	default:
		return fmt.Errorf("credential %q has no inject target", c.Name)
	}
}

func (br *Broker) resumeMsg(c manifest.Credential) string {
	switch {
	case c.Inject.BoxPath != "":
		return fmt.Sprintf("Credential %q is now available in the sandbox at %s. Continue toward the goal.", c.Name, c.Inject.BoxPath)
	case c.Inject.Env != "":
		return fmt.Sprintf("Credential %q is now available in the sandbox as env %s. Continue toward the goal.", c.Name, c.Inject.Env)
	}
	return "Credential provided. Continue toward the goal."
}

func (br *Broker) readLine() string {
	line, _ := br.in.ReadString('\n')
	return line
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
