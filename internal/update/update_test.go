package update

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/segmentstream/axprobe/internal/install"
	"github.com/segmentstream/axprobe/internal/version"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"0.1.0", "0.1.1", -1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "1.0.0", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
	}
	for _, c := range cases {
		got, err := compareVersions(c.left, c.right)
		if err != nil {
			t.Fatalf("compareVersions(%q,%q) error: %v", c.left, c.right, err)
		}
		if got != c.want {
			t.Fatalf("compareVersions(%q,%q) = %d, want %d", c.left, c.right, got, c.want)
		}
	}
}

func TestCompareVersionsRejectsDev(t *testing.T) {
	if _, err := compareVersions("dev", "0.1.0"); err == nil {
		t.Fatal("expected a dev build to be unparseable (barred from self-update)")
	}
}

func TestChecksumForAsset(t *testing.T) {
	checksums := []byte("abc123  axprobe_darwin_arm64.tar.gz\ndef456  axprobe_linux_amd64.tar.gz\n")
	got, err := checksumForAsset(checksums, "axprobe_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("checksumForAsset error: %v", err)
	}
	if got != "def456" {
		t.Fatalf("checksumForAsset = %q, want def456", got)
	}
	if _, err := checksumForAsset(checksums, "missing.tar.gz"); err == nil {
		t.Fatal("expected error for a missing asset")
	}
}

func TestAssetName(t *testing.T) {
	if got := assetName("darwin", "arm64"); got != "axprobe_darwin_arm64.tar.gz" {
		t.Fatalf("assetName = %q", got)
	}
}

func TestNotifyIfAvailablePrintsAndThrottles(t *testing.T) {
	dir := t.TempDir()
	metadataPath := filepath.Join(dir, "install.json")
	statePath := filepath.Join(dir, "update-check.json")
	if err := install.WriteMetadata(metadataPath, install.Metadata{
		Method:     install.MethodScript,
		InstallDir: dir,
		Repo:       "owner/repo",
		Version:    "0.1.0",
		OS:         "darwin",
		Arch:       "arm64",
	}); err != nil {
		t.Fatal(err)
	}

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			return nil, fmt.Errorf("unexpected path: %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v0.2.0","assets":[]}`)),
			Header:     make(http.Header),
		}, nil
	})}

	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	err := NotifyIfAvailable(context.Background(), version.Info{Version: "0.1.0"}, &out, NoticeOptions{
		MetadataPath:  metadataPath,
		StatePath:     statePath,
		ReleaseClient: ReleaseClient{BaseURL: "https://api.test", HTTPClient: client},
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("expected one release request, got %d", requests)
	}
	got := out.String()
	if !strings.Contains(got, "axprobe 0.2.0 is available") || !strings.Contains(got, "axprobe update") {
		t.Fatalf("unexpected notice: %q", got)
	}

	out.Reset()
	err = NotifyIfAvailable(context.Background(), version.Info{Version: "0.1.0"}, &out, NoticeOptions{
		MetadataPath:  metadataPath,
		StatePath:     statePath,
		ReleaseClient: ReleaseClient{BaseURL: "https://api.test", HTTPClient: client},
		Now:           func() time.Time { return now.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("throttled check should not call GitHub again, got %d requests", requests)
	}
	if out.Len() != 0 {
		t.Fatalf("throttled check should be silent, got %q", out.String())
	}
}

func TestNotifyIfAvailableSkipsDevAndMissingMetadata(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-check.json")
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("release API should not be called")
	})}

	var out bytes.Buffer
	if err := NotifyIfAvailable(context.Background(), version.Info{Version: "dev"}, &out, NoticeOptions{
		MetadataPath:  filepath.Join(dir, "missing-install.json"),
		StatePath:     statePath,
		ReleaseClient: ReleaseClient{BaseURL: "https://api.test", HTTPClient: client},
	}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("missing metadata should be silent, got %q", out.String())
	}

	metadataPath := filepath.Join(dir, "install.json")
	if err := install.WriteMetadata(metadataPath, install.Metadata{
		Method:  install.MethodScript,
		Repo:    "owner/repo",
		Version: "dev",
	}); err != nil {
		t.Fatal(err)
	}
	if err := NotifyIfAvailable(context.Background(), version.Info{Version: "dev"}, &out, NoticeOptions{
		MetadataPath:  metadataPath,
		StatePath:     statePath,
		ReleaseClient: ReleaseClient{BaseURL: "https://api.test", HTTPClient: client},
	}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("dev build should be silent, got %q", out.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}
