package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/segmentstream/axprobe/internal/install"
	"github.com/segmentstream/axprobe/internal/version"
)

// Options tunes a single update run.
type Options struct {
	CheckOnly bool
}

// Updater self-replaces the installed axprobe binary from GitHub Releases.
type Updater struct {
	CurrentVersion string
	GOOS           string
	GOARCH         string
	MetadataPath   string
	ReleaseClient  ReleaseClient
	Out            io.Writer
	ErrOut         io.Writer
}

// NewUpdater builds an Updater for the running build, writing to out/errOut.
func NewUpdater(info version.Info, out, errOut io.Writer) Updater {
	metadataPath, err := install.DefaultMetadataPath()
	if err != nil {
		metadataPath = ""
	}
	return Updater{
		CurrentVersion: info.Version,
		GOOS:           info.OS,
		GOARCH:         info.Arch,
		MetadataPath:   metadataPath,
		ReleaseClient:  ReleaseClient{},
		Out:            out,
		ErrOut:         errOut,
	}
}

// Run checks for a newer release and, unless CheckOnly, installs it in place.
func (updater Updater) Run(ctx context.Context, options Options) error {
	out := updater.Out
	if out == nil {
		out = io.Discard
	}

	metadataPath := updater.MetadataPath
	if metadataPath == "" {
		var err error
		metadataPath, err = install.DefaultMetadataPath()
		if err != nil {
			return err
		}
	}

	metadata, err := install.ReadMetadata(metadataPath)
	if err != nil {
		return err
	}
	if metadata.Method != install.MethodScript {
		return fmt.Errorf("install method %q cannot be self-updated; reinstall with install.sh", metadata.Method)
	}
	if metadata.InstallDir == "" {
		return errors.New("install metadata does not include install_dir; reinstall with install.sh")
	}

	repo := metadata.Repo
	if repo == "" {
		repo = install.DefaultRepo
	}

	release, err := updater.ReleaseClient.LatestRelease(ctx, repo)
	if err != nil {
		return err
	}
	latestVersion := normalizeVersion(release.TagName)
	currentVersion := metadata.Version
	if currentVersion == "" {
		currentVersion = updater.CurrentVersion
	}

	comparison, err := compareVersions(currentVersion, latestVersion)
	if err != nil {
		return fmt.Errorf("compare versions: %w", err)
	}

	fmt.Fprintf(out, "Current version: %s\n", currentVersion)
	fmt.Fprintf(out, "Latest version:  %s\n", latestVersion)

	if comparison >= 0 {
		fmt.Fprintln(out, "axprobe is already up to date.")
		return nil
	}
	if options.CheckOnly {
		fmt.Fprintln(out, "An update is available.")
		return nil
	}

	goos := updater.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := updater.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	asset := assetName(goos, goarch)
	archiveAsset, err := findAsset(release, asset)
	if err != nil {
		return err
	}
	checksumAsset, err := findAsset(release, "checksums.txt")
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Downloading %s\n", asset)
	checksums, err := updater.ReleaseClient.Download(ctx, checksumAsset.BrowserDownloadURL)
	if err != nil {
		return err
	}
	wantChecksum, err := checksumForAsset(checksums, asset)
	if err != nil {
		return err
	}
	archiveBytes, err := updater.ReleaseClient.Download(ctx, archiveAsset.BrowserDownloadURL)
	if err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp("", "axprobe-update-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	archivePath := filepath.Join(tempDir, asset)
	if err := os.WriteFile(archivePath, archiveBytes, 0o644); err != nil {
		return fmt.Errorf("write downloaded archive: %w", err)
	}

	fmt.Fprintln(out, "Verifying checksum")
	if err := verifyFileChecksum(archivePath, wantChecksum); err != nil {
		return err
	}

	extracted, err := extractBinaryFromTarGz(archivePath, tempDir)
	if err != nil {
		return err
	}

	targetPath := filepath.Join(metadata.InstallDir, install.BinaryName)
	if err := ensureReplaceAllowed(targetPath); err != nil {
		return err
	}

	fmt.Fprintln(out, "Installing update")
	if err := replaceBinary(targetPath, extracted.Path, extracted.Mode); err != nil {
		return err
	}

	metadata.Version = latestVersion
	metadata.OS = goos
	metadata.Arch = goarch
	if err := install.WriteMetadata(metadataPath, metadata); err != nil {
		return err
	}

	fmt.Fprintf(out, "Updated axprobe to %s\n", latestVersion)
	return nil
}

func ensureReplaceAllowed(targetPath string) error {
	file, err := os.OpenFile(targetPath, os.O_WRONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("cannot update %s: permission denied; rerun install.sh with --install-dir set to a user-writable directory", targetPath)
		}
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot update %s: installed binary was not found; reinstall with install.sh", targetPath)
		}
		return fmt.Errorf("cannot update %s: %w", targetPath, err)
	}
	return file.Close()
}

func replaceBinary(targetPath, sourcePath string, mode os.FileMode) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read extracted binary: %w", err)
	}
	if mode == 0 {
		mode = 0o755
	}
	mode |= 0o111

	tempFile, err := os.CreateTemp(filepath.Dir(targetPath), ".axprobe-update-*")
	if err != nil {
		return fmt.Errorf("create replacement binary: %w", err)
	}
	tempPath := tempFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return fmt.Errorf("write replacement binary: %w", err)
	}
	if err := tempFile.Chmod(mode); err != nil {
		tempFile.Close()
		return fmt.Errorf("set replacement permissions: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close replacement binary: %w", err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return fmt.Errorf("replace installed binary: %w", err)
	}
	cleanup = false
	return nil
}
