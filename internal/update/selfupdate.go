package update

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/Masterminds/semver/v3"
	"github.com/creativeprojects/go-selfupdate"
)

const repoOwner = "Kocoro-lab"
const repoName = "ShanClaw"

func CheckForUpdate(currentVersion string) (*selfupdate.Release, bool, error) {
	// Skip update check for non-semver versions (e.g. "dev")
	if _, err := semver.NewVersion(currentVersion); err != nil {
		return nil, false, nil
	}

	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return nil, false, err
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:    source,
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return nil, false, err
	}

	release, found, err := updater.DetectLatest(
		context.Background(),
		selfupdate.NewRepositorySlug(repoOwner, repoName),
	)
	if err != nil || !found {
		return nil, false, err
	}

	if release.LessOrEqual(currentVersion) {
		return nil, false, nil
	}

	return release, true, nil
}

func DoUpdate(currentVersion string) (string, error) {
	// Reject non-semver versions (e.g. "dev")
	if _, err := semver.NewVersion(currentVersion); err != nil {
		return currentVersion, fmt.Errorf("cannot update from non-semver version: %s", currentVersion)
	}

	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return "", err
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{Source: source})
	if err != nil {
		return "", err
	}

	release, found, err := updater.DetectLatest(
		context.Background(),
		selfupdate.NewRepositorySlug(repoOwner, repoName),
	)
	if err != nil {
		return "", err
	}
	if !found || release.LessOrEqual(currentVersion) {
		return currentVersion, fmt.Errorf("already up to date (%s)", currentVersion)
	}

	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return "", fmt.Errorf("find executable: %w", err)
	}

	if err := updater.UpdateTo(context.Background(), release, exe); err != nil {
		return "", fmt.Errorf("update failed: %w", err)
	}

	return release.Version(), nil
}

func PlatformInfo() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}

// AutoUpdate performs a background-safe update check + download.
// Returns a user-facing message (empty if nothing to report).
// Skips if: dev build or cache is fresh.
func AutoUpdate(currentVersion, shannonDir string) string {
	if _, err := semver.NewVersion(currentVersion); err != nil {
		return ""
	}

	cachePath := filepath.Join(shannonDir, "update-check.json")
	cache := NewUpdateCache(cachePath)

	if !cache.ShouldCheck() {
		return ""
	}

	release, found, err := CheckForUpdate(currentVersion)
	if err != nil || !found {
		// Still record the check to avoid hammering API on errors
		cache.Record(currentVersion)
		return ""
	}

	cache.Record(release.Version())

	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return fmt.Sprintf("Update available: v%s — run \"shan update\" or download from GitHub", release.Version())
	}

	// Auto-download and replace
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return fmt.Sprintf("Update available: v%s — run \"shan update\"", release.Version())
	}
	updater, err := selfupdate.NewUpdater(selfupdate.Config{Source: source})
	if err != nil {
		return fmt.Sprintf("Update available: v%s — run \"shan update\"", release.Version())
	}
	if err := updater.UpdateTo(context.Background(), release, exe); err != nil {
		return fmt.Sprintf("Update available: v%s (auto-update failed: %v)", release.Version(), err)
	}

	return fmt.Sprintf("Updated to v%s (restart to use)", release.Version())
}
