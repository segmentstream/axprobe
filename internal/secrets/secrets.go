// Package secrets stores credentials for reuse across runs. It prefers the macOS
// Keychain and falls back to a 0600 file store under ~/.axprobe/secrets. Values
// are namespaced by scenario so different tests don't collide.
package secrets

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const keychainService = "axprobe"

// Store reads and writes credentials for one scenario.
type Store struct {
	scenario string
}

// New returns a store namespaced to a scenario.
func New(scenario string) *Store { return &Store{scenario: scenario} }

func (s *Store) account(name string) string { return s.scenario + "/" + name }

// Get returns a stored credential and whether it was found.
func (s *Store) Get(name string) ([]byte, bool) {
	if v, ok := keychainGet(s.account(name)); ok {
		return v, true
	}
	return fileGet(s.account(name))
}

// Set stores a credential, preferring the keychain.
func (s *Store) Set(name string, value []byte) error {
	if keychainAvailable() {
		if err := keychainSet(s.account(name), value); err == nil {
			return nil
		}
	}
	return fileSet(s.account(name), value)
}

// SetKeychainOnly stores a value in the Keychain only — no plaintext-file
// fallback. Used for live secrets like oauth tokens. Errors if the Keychain is
// unavailable (then there is simply no cache on this host).
func (s *Store) SetKeychainOnly(name string, value []byte) error {
	if !keychainAvailable() {
		return fmt.Errorf("keychain unavailable on this host")
	}
	return keychainSet(s.account(name), value)
}

// Backend reports which store satisfied the last lookup, for display.
func Backend() string {
	if keychainAvailable() {
		return "macOS Keychain"
	}
	return "file store (~/.axprobe/secrets)"
}

func keychainAvailable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("security")
	return err == nil
}

func keychainSet(account string, value []byte) error {
	enc := base64.StdEncoding.EncodeToString(value)
	// -U updates if the item already exists.
	cmd := exec.Command("security", "add-generic-password",
		"-U", "-s", keychainService, "-a", account, "-w", enc)
	return cmd.Run()
}

func keychainGet(account string) ([]byte, bool) {
	if !keychainAvailable() {
		return nil, false
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-a", account, "-w").Output()
	if err != nil {
		return nil, false
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, false
	}
	return dec, true
}

func fileDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".axprobe", "secrets")
}

func fileGet(account string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join(fileDir(), account))
	if err != nil {
		return nil, false
	}
	return b, true
}

func fileSet(account string, value []byte) error {
	p := filepath.Join(fileDir(), account)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, value, 0o600)
}
