package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/segmentstream/axprobe/internal/install"
	"github.com/segmentstream/axprobe/internal/version"
)

const defaultNoticeInterval = 24 * time.Hour

// NoticeOptions tunes the passive update notice shown on ordinary commands.
// A zero value checks at most once per day for install.sh-managed release builds.
type NoticeOptions struct {
	CheckInterval time.Duration
	MetadataPath  string
	StatePath     string
	ReleaseClient ReleaseClient
	Now           func() time.Time
}

type noticeState struct {
	LastCheckedAt time.Time `json:"last_checked_at"`
}

// NotifyIfAvailable prints a short, non-blocking update notice to errOut when a
// newer released axprobe exists. It is intentionally best-effort: callers should
// ignore errors so update checks never break the command the user actually ran.
func NotifyIfAvailable(ctx context.Context, info version.Info, errOut io.Writer, options NoticeOptions) error {
	if errOut == nil {
		errOut = io.Discard
	}
	if options.CheckInterval == 0 {
		options.CheckInterval = defaultNoticeInterval
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}

	metadataPath := options.MetadataPath
	if metadataPath == "" {
		var err error
		metadataPath, err = install.DefaultMetadataPath()
		if err != nil {
			return err
		}
	}
	metadata, err := install.ReadMetadata(metadataPath)
	if err != nil {
		return nil
	}
	if metadata.Method != install.MethodScript {
		return nil
	}

	currentVersion := metadata.Version
	if currentVersion == "" {
		currentVersion = info.Version
	}
	if currentVersion == "" || currentVersion == "dev" {
		return nil
	}

	statePath := options.StatePath
	if statePath == "" {
		var err error
		statePath, err = defaultNoticeStatePath()
		if err != nil {
			return err
		}
	}
	if !noticeDue(statePath, now(), options.CheckInterval) {
		return nil
	}

	repo := metadata.Repo
	if repo == "" {
		repo = install.DefaultRepo
	}

	client := options.ReleaseClient
	release, err := client.LatestRelease(ctx, repo)
	// Throttle attempted checks too. If GitHub or the network is unavailable,
	// repeatedly trying on every axprobe invocation is worse than a stale notice.
	_ = writeNoticeState(statePath, noticeState{LastCheckedAt: now()})
	if err != nil {
		return err
	}

	latestVersion := normalizeVersion(release.TagName)
	comparison, err := compareVersions(currentVersion, latestVersion)
	if err != nil {
		return err
	}
	if comparison >= 0 {
		return nil
	}

	fmt.Fprintf(errOut, "axprobe %s is available (current %s). Run `axprobe update` to install it.\n", latestVersion, currentVersion)
	return nil
}

func defaultNoticeStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".axprobe", "update-check.json"), nil
}

func noticeDue(path string, now time.Time, interval time.Duration) bool {
	state, err := readNoticeState(path)
	if err != nil || state.LastCheckedAt.IsZero() {
		return true
	}
	return !state.LastCheckedAt.Add(interval).After(now)
}

func readNoticeState(path string) (noticeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return noticeState{}, err
	}
	var state noticeState
	if err := json.Unmarshal(data, &state); err != nil {
		return noticeState{}, err
	}
	return state, nil
}

func writeNoticeState(path string, state noticeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update check state: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create update check state directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write update check state: %w", err)
	}
	return nil
}
