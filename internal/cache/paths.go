package cache

import (
	"os"
	"path/filepath"

	"github.com/infracost/cli/pkg/logging"
)

// Subdir names under [Root]. Kept here so the prune sweep can recognize
// "known" directories at the root.
const (
	resultsSubdir       = "results"
	parserSubdir        = "parser"
	parserResultsSubdir = "parser-results"
	pluginsSubdir       = "plugins"

	// UpdateCheckFilename is the one file allowed at the cache root; the
	// update checker (internal/update) reads/writes it directly.
	UpdateCheckFilename = "update-check.json"
)


// Root resolves the on-disk root for every infracost cache. Tries the OS
// user-cache dir first, then the home dir, then CWD — matching the
// fallback chain previously inlined in internal/cache/config.go,
// internal/update/check.go and pkg/plugins/install.go.
func Root() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "infracost")
	} else {
		logging.WithError(err).Msg("failed to load user cache dir, falling back to home directory")
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".infracost")
	} else {
		logging.WithError(err).Msg("failed to load user home dir, falling back to current directory")
	}
	return ".infracost"
}

// ResultsDir is where internal/cache writes per-scan results
// (<key>.json + manifest.json).
func ResultsDir() string { return filepath.Join(Root(), resultsSubdir) }

// ParserDir is where the parser plugin writes terraform module
// downloads (keyed by source CacheKey).
func ParserDir() string { return filepath.Join(Root(), parserSubdir) }

// ParserResultsDir is where the CLI caches a per-project Parse()
// response keyed by a recursive mtime fingerprint of the project files.
func ParserResultsDir() string { return filepath.Join(Root(), parserResultsSubdir) }

// PluginsDir is where plugin binaries (+ .sha256/.version sidecars) live.
func PluginsDir() string { return filepath.Join(Root(), pluginsSubdir) }
