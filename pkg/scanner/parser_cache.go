package scanner

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"google.golang.org/protobuf/proto"
)

// fingerprintHexLen is the byte length of the hex-encoded SHA256
// fingerprint stored at the head of every parser-results cache file.
const fingerprintHexLen = 64

// fingerprintProject produces a hex SHA256 of every file's
// (path, mtime-ns, size) under projectDir and each of depPaths. depPaths
// are relative to rootDir and may be files, dirs or globs; a missing one
// hashes a marker so it appearing later is a cache miss. Hashed paths are
// relative to rootDir. Skip dirs come from [cache.SkipDirs] so the
// parser-result cache and the source-freshness check stay in lockstep.
// extra is mixed in first so parser input changes (RawOptions) also
// change the fingerprint.
//
// mtime-based fingerprinting is deliberately content-blind: a re-save
// with identical content invalidates the cache, and an upstream module
// bump (within a >= constraint) that doesn't touch local files
// doesn't. The escape hatch is `infracost cache clear`.
func fingerprintProject(rootDir, projectDir string, depPaths []string, extra []byte) (string, error) {
	h := sha256.New()
	if len(extra) > 0 {
		h.Write(extra)
		h.Write([]byte{0})
	}

	projectRel, err := filepath.Rel(rootDir, projectDir)
	if err != nil {
		return "", err
	}
	if err := hashTree(h, projectDir, projectRel); err != nil {
		return "", err
	}

	for _, dep := range resolveDependencyPaths(rootDir, projectDir, depPaths) {
		if dep.target == "" {
			h.Write([]byte("missing:" + dep.rel))
			h.Write([]byte{0})
			continue
		}
		if err := hashTree(h, dep.target, dep.rel); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dependencyPath is a dep keyed by its rootDir-relative path. target is
// the symlink-resolved path to walk, or "" when the dep is missing.
type dependencyPath struct {
	rel    string
	target string
}

// resolveDependencyPaths expands depPaths into sorted, de-duplicated
// rootDir-relative paths, dropping any inside projectDir. Symlinks are
// resolved; a dangling one is missing.
func resolveDependencyPaths(rootDir, projectDir string, depPaths []string) []dependencyPath {
	seen := make(map[string]bool)
	var out []dependencyPath
	add := func(abs string) {
		abs = filepath.Clean(abs)
		if isWithin(projectDir, abs) {
			return
		}
		rel, err := filepath.Rel(rootDir, abs)
		if err != nil {
			rel = abs
		}
		if seen[rel] {
			return
		}
		seen[rel] = true
		target, err := filepath.EvalSymlinks(abs)
		if err != nil {
			target = ""
		}
		out = append(out, dependencyPath{rel: rel, target: target})
	}

	for _, dep := range depPaths {
		abs := filepath.Join(rootDir, dep)
		if _, err := os.Lstat(abs); err == nil {
			add(abs)
			continue
		}
		matches, err := filepath.Glob(abs)
		if err != nil || len(matches) == 0 {
			add(abs)
			continue
		}
		for _, m := range matches {
			add(m)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

func isWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// hashTree hashes every file under root (or root itself, if a file),
// naming each by label joined with its path relative to root.
func hashTree(h hash.Hash, root, label string) error {
	var sizeBuf, nsBuf [8]byte
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && cache.SkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h.Write([]byte(filepath.Join(label, rel)))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(nsBuf[:], uint64(info.ModTime().UnixNano())) //nolint:gosec // G115: bit-pattern cast for hashing, sign irrelevant
		h.Write(nsBuf[:])
		binary.BigEndian.PutUint64(sizeBuf[:], uint64(info.Size())) //nolint:gosec // G115: file sizes are non-negative
		h.Write(sizeBuf[:])
		return nil
	})
}

// genericOptionsFingerprint returns the parts of GenericOptions that can
// change a parse, deterministically marshaled. Per-run and auth fields
// are cleared.
func genericOptionsFingerprint(g *options.GenericOptions) ([]byte, error) {
	if g == nil {
		return nil, nil
	}
	c := proto.Clone(g).(*options.GenericOptions) //nolint:errcheck,forcetypeassert // Clone returns the input's type
	c.TemporaryDirectory = ""
	c.FetchAuth = nil
	c.CredentialSets = nil
	c.AwsCredentials = nil
	return proto.MarshalOptions{Deterministic: true}.Marshal(c)
}

// parserCacheDir returns the subdirectory of parser-results that holds
// entries for one (plugin name, plugin version) pair. Sanitizes the
// plugin name by swapping `/` for `_` so plugin names that look like
// repo paths (e.g. `infracost/terraform`) don't escape the cache root.
// Version-keying means an upgraded plugin automatically misses the old
// cache; the 24h prune cleans the old version dir up.
func parserCacheDir(pluginName, pluginVersion string) string {
	safeName := strings.ReplaceAll(pluginName, "/", "_")
	if safeName == "" {
		safeName = "_unknown"
	}
	safeVersion := pluginVersion
	if safeVersion == "" {
		safeVersion = "_unknown"
	}
	return filepath.Join(cache.ParserResultsDir(), safeName, safeVersion)
}

// projectCacheFilename hashes the absolute project path into a stable
// filename (one file per project within a plugin/version dir). The
// fingerprint validates freshness inside that file.
func projectCacheFilename(absProjectPath string) string {
	h := sha256.Sum256([]byte(absProjectPath))
	return hex.EncodeToString(h[:]) + ".pb"
}

// loadParsedResponse returns a previously-cached Parse response for
// absProjectPath whose stored fingerprint matches the supplied one.
// Returns nil for any failure (cache miss, fingerprint mismatch,
// unreadable / corrupt file) — the caller falls back to re-parsing.
func loadParsedResponse(pluginName, pluginVersion, absProjectPath, fingerprint string) *pluginpb.ParseResponse {
	path := filepath.Join(parserCacheDir(pluginName, pluginVersion), projectCacheFilename(absProjectPath))
	f, err := os.Open(path) //nolint:gosec // G304: path is derived from the cache root, not user input
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Debugf("parser cache open failed for %q: %s", path, err)
		}
		return nil
	}
	defer func() { _ = f.Close() }()

	header := make([]byte, fingerprintHexLen)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil
	}
	if string(header) != fingerprint {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	var resp pluginpb.ParseResponse
	if err := proto.Unmarshal(data, &resp); err != nil {
		logging.Debugf("parser cache unmarshal failed for %q: %s", path, err)
		return nil
	}
	return &resp
}

// saveParsedResponse writes the Parse response for absProjectPath into
// the parser-results cache, prefixed with the supplied fingerprint so
// loadParsedResponse can detect a stale match. Uses os.CreateTemp so
// concurrent saves don't clobber a shared `.tmp` file. Best-effort —
// write failures are logged and swallowed so a flaky cache write never
// aborts a scan.
func saveParsedResponse(pluginName, pluginVersion, absProjectPath, fingerprint string, resp *pluginpb.ParseResponse) {
	dir := parserCacheDir(pluginName, pluginVersion)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logging.Warnf("failed to create parser results cache dir %q: %s", dir, err)
		return
	}

	data, err := proto.Marshal(resp)
	if err != nil {
		logging.Warnf("failed to marshal parser response for cache: %s", err)
		return
	}

	tmp, err := os.CreateTemp(dir, "parser-*.tmp")
	if err != nil {
		logging.Warnf("failed to create parser cache tmp file in %q: %s", dir, err)
		return
	}
	tmpPath := tmp.Name()
	out := make([]byte, 0, fingerprintHexLen+len(data))
	out = append(out, []byte(fingerprint)...)
	out = append(out, data...)
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		logging.Warnf("failed to write parser cache %q: %s", tmpPath, err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		logging.Warnf("failed to close parser cache %q: %s", tmpPath, err)
		return
	}

	finalPath := filepath.Join(dir, projectCacheFilename(absProjectPath))
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		logging.Warnf("failed to commit parser cache %q: %s", finalPath, err)
	}
}
