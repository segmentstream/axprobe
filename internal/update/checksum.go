package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checksumForAsset finds the sha256 hex for assetName in a checksums.txt body.
func checksumForAsset(checksums []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if filepath.Base(fields[len(fields)-1]) == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", assetName)
}

// verifyFileChecksum fails if the file at path does not hash to want.
func verifyFileChecksum(path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read downloaded archive: %w", err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", filepath.Base(path), want, got)
	}
	return nil
}
