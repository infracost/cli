package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/version"
)

// Info describes an available newer release. It mirrors v0.10's
// update.Info so the end-of-run notice in main.go has the same shape.
type Info struct {
	LatestVersion string
	Cmd           string
}

// checkCacheTTL bounds how often we hit a remote endpoint to look up the
// latest version. v0.10 uses 24h; matching that.
const checkCacheTTL = 24 * time.Hour

// CheckForUpdate returns information about a newer release if one exists.
// It returns (nil, nil) when the user is already up to date or when the
// check should be skipped (dev build, env opt-out, test run).
//
// The latest version is cached on disk for [checkCacheTTL] so we don't hit
// the network on every invocation.
func CheckForUpdate(ctx context.Context) (*Info, error) {
	if skipUpdateCheck() {
		return nil, nil
	}

	current, err := semver.NewVersion(version.Version)
	if err != nil {
		// Not a real release build (e.g. "dev"): nothing meaningful to compare.
		return nil, nil
	}

	method := DetectInstallMethod()

	cached, _ := loadCheckCache()
	if cached != nil && time.Since(cached.CheckedAt) < checkCacheTTL {
		return compareVersions(current, cached.LatestVersion, method)
	}

	latest, err := fetchLatestVersion(ctx, method)
	if err != nil {
		return nil, err
	}

	if err := saveCheckCache(&checkCache{
		LatestVersion: latest,
		CheckedAt:     time.Now(),
	}); err != nil {
		logging.Debugf("failed to save update check cache: %v", err)
	}

	return compareVersions(current, latest, method)
}

func fetchLatestVersion(ctx context.Context, method InstallMethod) (string, error) {
	switch method {
	case InstallMethodBrew:
		return getLatestBrewVersion()
	case InstallMethodChocolatey:
		return getLatestChocolateyVersion()
	default:
		info, err := CheckLatestVersion(ctx)
		if err != nil {
			return "", err
		}
		return info.Latest, nil
	}
}

func compareVersions(current *semver.Version, latestStr string, method InstallMethod) (*Info, error) {
	latest, err := semver.NewVersion(latestStr)
	if err != nil {
		return nil, fmt.Errorf("parsing latest version %q: %w", latestStr, err)
	}
	if !latest.GreaterThan(current) {
		return nil, nil
	}
	return &Info{
		LatestVersion: ensureV(latest.String()),
		Cmd:           method.UpgradeCommand(),
	}, nil
}

// skipUpdateCheck mirrors v0.10's logic: opt-out env, test runs, dev builds.
// Self-hosted pricing mode also skips: those environments typically can't
// reach releases.infracost.io, and an unanswered check would block exit.
func skipUpdateCheck() bool {
	if os.Getenv("INFRACOST_SKIP_UPDATE_CHECK") != "" {
		return true
	}
	if os.Getenv("INFRACOST_CLI_PRICING_API_KEY") != "" {
		return true
	}
	if isTestBinaryFn() {
		return true
	}
	return version.Version == "dev"
}

// isTestBinaryFn is a var so tests can opt back into the CheckForUpdate
// paths. Production callers always see the .test suffix detector.
var isTestBinaryFn = func() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return len(exe) >= 5 && exe[len(exe)-5:] == ".test"
}

type checkCache struct {
	LatestVersion string    `json:"latestVersion"`
	CheckedAt     time.Time `json:"checkedAt"`
}

// checkCachePath is a var so tests can redirect it to a temp dir.
var checkCachePath = func() string {
	dir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(dir, "infracost", "update-check.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".infracost", "update-check.json")
	}
	return filepath.Join(".infracost", "update-check.json")
}

func loadCheckCache() (*checkCache, error) {
	path := checkCachePath()
	// nolint:gosec // G304: path is derived from the user's own cache dir.
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c checkCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveCheckCache(c *checkCache) error {
	path := checkCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
