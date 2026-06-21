// Package install records how axprobe was installed, so `axprobe update` knows
// where the binary lives and whether it may replace it. install.sh writes this
// file; the updater reads it and writes the new version back after updating.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// MethodScript marks a binary installed by install.sh — the only method the
	// updater may self-replace (a brew/go-install binary is owned by its manager).
	MethodScript = "script"
	// DefaultRepo is the GitHub repo releases are fetched from.
	DefaultRepo = "segmentstream/axprobe"
	// BinaryName is the installed executable's basename.
	BinaryName = "axprobe"
)

// Metadata is the install record persisted at ~/.axprobe/install.json.
type Metadata struct {
	Method     string `json:"method"`
	InstallDir string `json:"install_dir"`
	Repo       string `json:"repo"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

// DefaultMetadataPath is ~/.axprobe/install.json.
func DefaultMetadataPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".axprobe", "install.json"), nil
}

// ReadMetadata loads the install record, with a friendly hint if it is absent.
func ReadMetadata(path string) (Metadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Metadata{}, fmt.Errorf("install metadata was not found at %s; reinstall with install.sh before using axprobe update", path)
		}
		return Metadata{}, fmt.Errorf("read install metadata: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("parse install metadata: %w", err)
	}
	return metadata, nil
}

// WriteMetadata persists the install record (creating ~/.axprobe if needed).
func WriteMetadata(path string, metadata Metadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode install metadata: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create metadata directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write install metadata: %w", err)
	}
	return nil
}
