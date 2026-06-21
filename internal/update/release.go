// Package update implements `axprobe update`: fetch the latest GitHub release,
// compare it to the running build, and atomically replace the installed binary
// after verifying its checksum. Ported from the segmentstream-cli updater.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultGitHubAPIBaseURL = "https://api.github.com"

// ReleaseClient talks to the GitHub Releases API. A zero value is usable.
type ReleaseClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// GitHubRelease is the subset of the release payload the updater needs.
type GitHubRelease struct {
	TagName    string        `json:"tag_name"`
	Prerelease bool          `json:"prerelease"`
	Assets     []GitHubAsset `json:"assets"`
}

// GitHubAsset is one downloadable file attached to a release.
type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// LatestRelease returns the newest published release for repo (owner/name).
func (client ReleaseClient) LatestRelease(ctx context.Context, repo string) (GitHubRelease, error) {
	baseURL := strings.TrimRight(client.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultGitHubAPIBaseURL
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/latest", baseURL, repo), nil)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("create release request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "axprobe")

	response, err := httpClient(client.HTTPClient).Do(request)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return GitHubRelease{}, fmt.Errorf("fetch latest release: GitHub returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var release GitHubRelease
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return GitHubRelease{}, fmt.Errorf("decode latest release: %w", err)
	}
	return release, nil
}

// Download fetches the bytes at downloadURL.
func (client ReleaseClient) Download(ctx context.Context, downloadURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	request.Header.Set("User-Agent", "axprobe")

	response, err := httpClient(client.HTTPClient).Do(request)
	if err != nil {
		return nil, fmt.Errorf("download asset: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download asset: server returned %s", response.Status)
	}

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read downloaded asset: %w", err)
	}
	return data, nil
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return http.DefaultClient
}

func findAsset(release GitHubRelease, name string) (GitHubAsset, error) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, nil
		}
	}
	return GitHubAsset{}, fmt.Errorf("release %s does not contain asset %s", release.TagName, name)
}

// assetName mirrors the goreleaser archive name_template.
func assetName(goos, goarch string) string {
	return fmt.Sprintf("axprobe_%s_%s.tar.gz", goos, goarch)
}
