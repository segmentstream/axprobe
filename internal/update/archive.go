package update

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/segmentstream/axprobe/internal/install"
)

type extractedBinary struct {
	Path string
	Mode os.FileMode
}

// extractBinaryFromTarGz pulls the axprobe executable out of a release tarball
// into destinationDir, preserving its mode.
func extractBinaryFromTarGz(archivePath, destinationDir string) (extractedBinary, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return extractedBinary{}, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return extractedBinary{}, fmt.Errorf("read gzip archive: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return extractedBinary{}, fmt.Errorf("read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(header.Name) != install.BinaryName {
			continue
		}

		outputPath := filepath.Join(destinationDir, install.BinaryName)
		output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
		if err != nil {
			return extractedBinary{}, fmt.Errorf("create extracted binary: %w", err)
		}
		if _, err := io.Copy(output, tarReader); err != nil {
			output.Close()
			return extractedBinary{}, fmt.Errorf("write extracted binary: %w", err)
		}
		if err := output.Close(); err != nil {
			return extractedBinary{}, fmt.Errorf("close extracted binary: %w", err)
		}

		mode := os.FileMode(header.Mode) & 0o777
		if mode == 0 {
			mode = 0o755
		}
		return extractedBinary{Path: outputPath, Mode: mode}, nil
	}

	return extractedBinary{}, fmt.Errorf("%s binary not found in archive", install.BinaryName)
}
