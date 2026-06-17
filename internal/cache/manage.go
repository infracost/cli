package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/infracost/cli/pkg/logging"
)

// DefaultPruneAge is the cutoff used by [Prune] when called without an
// explicit age (auto-prune at the top of `infracost scan`, and the
// `infracost cache prune` command's default).
const DefaultPruneAge = 24 * time.Hour

// ParseAge accepts any [time.ParseDuration]-compatible string plus a
// trailing `d` (days) or `w` (weeks) — e.g. "30d", "2w", "12h30m".
// Returned to the caller as a time.Duration so the rest of the package
// stays standard.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if len(s) > 1 {
		last := s[len(s)-1]
		var mult time.Duration
		switch last {
		case 'd':
			mult = 24 * time.Hour
		case 'w':
			mult = 7 * 24 * time.Hour
		}
		if mult > 0 {
			n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid duration %q: %w", s, err)
			}
			if n < 0 {
				return 0, fmt.Errorf("duration must not be negative: %q", s)
			}
			return time.Duration(n) * mult, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration must not be negative: %q", s)
	}
	return d, nil
}

// SubdirInfo is a single row in [Info]'s output — one of the named caches
// (results / parser / parser-results) and its total on-disk size in
// bytes. Returned in the display order Info wants to render.
type SubdirInfo struct {
	Name  string
	Label string
	Bytes int64
}

// Prune is best-effort: it never errors out the caller. It logs and
// continues past every failure — a corrupt manifest, a permission denied,
// a directory walk that gives up partway, all just produce a warning so
// the rest of the cache can still get tidied. Called automatically before
// each `infracost scan` and on demand via `infracost cache prune`.
//
// What gets cleaned, by area:
//
//   - root: every file under [Root] except [UpdateCheckFilename] is
//     removed; directories are left alone (the four canonical caches
//     plus anything a future version adds).
//   - results/: <key>.json files older than age, then the manifest is
//     reconciled to mirror what's actually on disk — entries pointing
//     at files that don't exist get dropped, and orphan files with no
//     manifest entry get removed too (the disk is authoritative).
//   - parser/: top-level entries (module CacheKey dirs and their
//     `.link` sidecars) older than age. Then any `.link` sidecar whose
//     matching module directory exists but is empty has both the
//     sidecar and the empty dir removed — abandoned half-fetches.
//   - parser-results/: per-project Parse() responses older than age
//     (layout is parser-results/<plugin>/<version>/<key>.pb; empty
//     version/ and plugin/ dirs left behind are swept too).
//   - plugins/: subdirectories (legacy pre-flat-layout), plus any
//     .sha256/.version sidecar whose matching executable is gone — age
//     does not apply here.
func Prune(age time.Duration) {
	pruneRoot()
	pruneLegacy()
	pruneResults(age)
	pruneByMtime(ParserDir(), false, age)
	pruneParserResults(age)
	pruneParserLinks()
	prunePlugins()
}

// legacyDirs lists subdirectories under [Root] that older CLI versions
// wrote to and the current version no longer touches. Removed wholesale
// on every prune so upgrade paths don't leave dead caches lying around
// forever.
var legacyDirs = []string{
	// pre-rename location for what is now results/.
	"cache",
}

// pruneLegacy removes any [legacyDirs] entry that still exists. Each is
// removed independently so one failure doesn't strand the others.
func pruneLegacy() {
	root := Root()
	for _, name := range legacyDirs {
		target := filepath.Join(root, name)
		if err := os.RemoveAll(target); err != nil {
			logging.Warnf("failed to remove legacy cache directory %q: %s", target, err)
		}
	}
}

// Clear wipes the three caches `cache info` reports on — results,
// parser and parser-results — and leaves plugins/ and update-check.json
// alone. Best-effort: each subdir is removed independently so a failure
// on one doesn't strand the others.
func Clear() {
	for _, dir := range []string{ResultsDir(), ParserDir(), ParserResultsDir()} {
		if err := os.RemoveAll(dir); err != nil {
			logging.Warnf("failed to clear cache directory %q: %s", dir, err)
		}
	}
}

// Info returns the on-disk size of each reportable cache (results,
// parser, parser-results), in the display order `cache info` renders.
// Missing directories report Bytes=0 (not an error — a fresh install or
// a freshly-cleared cache is normal). Best-effort: walk errors are
// logged and the partial total is returned.
func Info() []SubdirInfo {
	return []SubdirInfo{
		{Name: resultsSubdir, Label: "results cache", Bytes: dirSize(ResultsDir())},
		{Name: parserSubdir, Label: "parser cache", Bytes: dirSize(ParserDir())},
		{Name: parserResultsSubdir, Label: "parser results cache", Bytes: dirSize(ParserResultsDir())},
	}
}

// pruneRoot removes every file directly under [Root] except
// [UpdateCheckFilename]. Directories are left alone: the four canonical
// caches own their own cleanup below, and any future version of the CLI
// is free to add new subdirs without this sweep nuking them. Catches
// legacy protocache blobs (extensionless files at root).
func pruneRoot() {
	root := Root()
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read cache root %q: %s", root, err)
		}
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == UpdateCheckFilename {
			continue
		}
		target := filepath.Join(root, e.Name())
		if err := os.Remove(target); err != nil {
			logging.Warnf("failed to remove stray cache file %q: %s", target, err)
		}
	}
}

// pruneParserLinks tidies abandoned half-fetched modules under parser/.
// Layout is parser/<CacheKey>/ for the module contents and
// parser/<CacheKey>.link for the sidecar; an empty <CacheKey>/ paired
// with a sidecar means the fetch never completed and both are stale.
// Sidecars live at the top of parser/ — no recursion.
func pruneParserLinks() {
	dir := ParserDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read parser cache %q: %s", dir, err)
		}
		return
	}

	dirs := make(map[string]struct{})
	for _, e := range entries {
		if e.IsDir() {
			dirs[e.Name()] = struct{}{}
		}
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".link") {
			continue
		}
		base := strings.TrimSuffix(name, ".link")
		if _, ok := dirs[base]; !ok {
			continue
		}
		baseDir := filepath.Join(dir, base)
		baseEntries, err := os.ReadDir(baseDir)
		if err != nil {
			logging.Warnf("failed to read module dir %q: %s", baseDir, err)
			continue
		}
		if len(baseEntries) != 0 {
			continue
		}
		if err := os.Remove(baseDir); err != nil {
			logging.Warnf("failed to remove empty module dir %q: %s", baseDir, err)
			continue
		}
		linkPath := filepath.Join(dir, name)
		if err := os.Remove(linkPath); err != nil {
			logging.Warnf("failed to remove orphan link %q: %s", linkPath, err)
		}
	}
}

// pruneResults trims results/ to only entries newer than age AND
// present in the manifest. The disk is authoritative: if the manifest
// references a file that's gone, the entry is dropped; if a <key>.json
// sits there with no manifest entry it's removed because nothing can
// read it.
func pruneResults(age time.Duration) {
	dir := ResultsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read results cache %q: %s", dir, err)
		}
		return
	}

	cutoff := time.Now().Add(-age)
	keptKeys := make(map[string]struct{})
	for _, e := range entries {
		name := e.Name()
		if name == "manifest.json" || e.IsDir() {
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			logging.Warnf("failed to stat results entry %q: %s", path, err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				logging.Warnf("failed to remove stale results entry %q: %s", path, err)
				continue
			}
			continue
		}
		keptKeys[strings.TrimSuffix(name, ".json")] = struct{}{}
	}

	reconcileManifest(dir, keptKeys)
}

// reconcileManifest loads the manifest, drops entries whose <key>.json
// no longer exists, and removes any <key>.json files that don't have a
// manifest entry (so the on-disk state and the index always agree). A
// missing manifest is fine — there's nothing to reconcile. A corrupt
// manifest is deleted (the result files survive but become orphans, and
// the orphan sweep below removes them next pass).
func reconcileManifest(dir string, keptKeys map[string]struct{}) {
	manifestPath := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(manifestPath) //nolint:gosec // G304: manifestPath is derived from the cache root, not user input
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read manifest %q: %s", manifestPath, err)
		}
		// No manifest → every <key>.json is an orphan. Remove them.
		removeOrphans(dir, keptKeys, nil)
		return
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil || m.Entries == nil {
		logging.Warnf("manifest %q unreadable, deleting it: %v", manifestPath, err)
		if rmErr := os.Remove(manifestPath); rmErr != nil {
			logging.Warnf("failed to delete bad manifest %q: %s", manifestPath, rmErr)
		}
		removeOrphans(dir, keptKeys, nil)
		return
	}

	changed := false
	for key := range m.Entries {
		if _, ok := keptKeys[key]; !ok {
			delete(m.Entries, key)
			changed = true
		}
	}

	removeOrphans(dir, keptKeys, m.Entries)

	if !changed {
		return
	}
	out, err := json.Marshal(m)
	if err != nil {
		logging.Warnf("failed to marshal updated manifest: %s", err)
		return
	}
	if err := os.WriteFile(manifestPath, out, 0600); err != nil {
		logging.Warnf("failed to write updated manifest %q: %s", manifestPath, err)
	}
}

// removeOrphans deletes any <key>.json in results/ that has no
// corresponding manifest entry. When entries is nil, every kept key is
// considered orphaned (used after the manifest is missing or deleted).
func removeOrphans(dir string, keptKeys map[string]struct{}, entries map[string]ManifestEntry) {
	for key := range keptKeys {
		if entries != nil {
			if _, ok := entries[key]; ok {
				continue
			}
		}
		path := filepath.Join(dir, key+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to remove orphan results entry %q: %s", path, err)
		}
	}
}

// pruneParserResults trims parser-results/<plugin>/<version>/*.pb files
// older than age, then removes empty version/ and plugin/ dirs left
// behind. Layout is owned by pkg/scanner.parserCacheDir.
func pruneParserResults(age time.Duration) {
	root := ParserResultsDir()
	plugins, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read parser-results cache %q: %s", root, err)
		}
		return
	}

	cutoff := time.Now().Add(-age)
	for _, plug := range plugins {
		if !plug.IsDir() {
			continue
		}
		pluginDir := filepath.Join(root, plug.Name())
		versions, err := os.ReadDir(pluginDir)
		if err != nil {
			logging.Warnf("failed to read parser-results plugin dir %q: %s", pluginDir, err)
			continue
		}
		for _, ver := range versions {
			if !ver.IsDir() {
				continue
			}
			verDir := filepath.Join(pluginDir, ver.Name())
			pruneOldFilesIn(verDir, cutoff)
			removeIfEmpty(verDir)
		}
		removeIfEmpty(pluginDir)
	}
}

// pruneOldFilesIn removes files in dir whose mtime is before cutoff.
// Doesn't recurse — assumes a flat directory of leaf files.
func pruneOldFilesIn(dir string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		logging.Warnf("failed to read parser-results version dir %q: %s", dir, err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if err := os.Remove(target); err != nil {
			logging.Warnf("failed to remove stale parser-results entry %q: %s", target, err)
		}
	}
}

// removeIfEmpty deletes dir if it has no remaining entries. Best
// effort, errors swallowed — non-empty dirs return ENOTEMPTY which is
// not a failure here.
func removeIfEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(dir)
}

// pruneByMtime removes immediate children of dir whose mtime is older
// than age. When dirOnly is true only subdirectories are considered
// (used for parser/, where each child is a CacheKey-keyed module dir);
// otherwise both files and subdirs are checked. Best effort, errors
// logged.
func pruneByMtime(dir string, dirOnly bool, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read cache directory %q: %s", dir, err)
		}
		return
	}

	cutoff := time.Now().Add(-age)
	for _, e := range entries {
		if dirOnly && !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			logging.Warnf("failed to stat %q: %s", filepath.Join(dir, e.Name()), err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		target := filepath.Join(dir, e.Name())
		if err := os.RemoveAll(target); err != nil {
			logging.Warnf("failed to remove stale cache entry %q: %s", target, err)
		}
	}
}

// prunePlugins removes:
//   - any *directory* under plugins/ (legacy pre-flat layout — current
//     installs put binaries directly at <plugins>/<name>).
//   - any sidecar file (.sha256 / .version) whose matching plugin binary
//     no longer exists.
func prunePlugins() {
	dir := PluginsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Warnf("failed to read plugins cache %q: %s", dir, err)
		}
		return
	}

	executables := make(map[string]struct{})
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sha256") && !strings.HasSuffix(name, ".version") {
			executables[name] = struct{}{}
		}
	}

	for _, e := range entries {
		name := e.Name()
		target := filepath.Join(dir, name)

		if e.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				logging.Warnf("failed to remove legacy plugin directory %q: %s", target, err)
			}
			continue
		}

		var base string
		switch {
		case strings.HasSuffix(name, ".sha256"):
			base = strings.TrimSuffix(name, ".sha256")
		case strings.HasSuffix(name, ".version"):
			base = strings.TrimSuffix(name, ".version")
		default:
			continue
		}
		if _, ok := executables[base]; ok {
			continue
		}
		if err := os.Remove(target); err != nil {
			logging.Warnf("failed to remove orphan plugin sidecar %q: %s", target, err)
		}
	}
}

// dirSize recursively sums file sizes under dir. Missing dir → 0 (the
// caller treats absence as an empty cache). Walk errors are logged and
// the partial total is returned.
func dirSize(dir string) int64 {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) && path == dir {
				return fs.SkipAll
			}
			logging.Warnf("failed walking %q: %s", path, walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.Warnf("failed to compute size of %q: %s", dir, err)
	}
	return total
}

// FormatBytes renders a byte count as a human-readable string with one
// decimal place at the appropriate IEC scale. Exposed so the cobra
// command can render Info() results without duplicating the math.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
