package scanner

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/pkg/logging"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"google.golang.org/protobuf/proto"
)

// fingerprintHexLen is the byte length of the hex-encoded SHA256
// fingerprint stored at the head of every parser-results cache file.
const fingerprintHexLen = 64

// fingerprintProject walks projectDir recursively and produces a hex
// SHA256 of every file's (relative-path, mtime-ns, size). Skip dirs
// come from [cache.SkipDirs] so the parser-result cache and the
// source-freshness check stay in lockstep. The extra argument is mixed
// in first so the fingerprint also changes when parser inputs
// (RawOptions / RawOptionsFormat) change — otherwise a tfvars file
// swap that doesn't touch any *.tf would be a cache hit.
//
// mtime-based fingerprinting is deliberately content-blind: a re-save
// with identical content invalidates the cache, and an upstream module
// bump (within a >= constraint) that doesn't touch local files
// doesn't. Both trade-offs are acceptable for the typical "edit one
// project in a 10k-project repo" case; the escape hatch is
// `infracost cache clear`.
func fingerprintProject(projectDir string, extra []byte) (string, error) {
	h := sha256.New()
	if len(extra) > 0 {
		h.Write(extra)
		h.Write([]byte{0})
	}

	var sizeBuf, nsBuf [8]byte
	err := filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != projectDir && cache.SkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(nsBuf[:], uint64(info.ModTime().UnixNano()))
		h.Write(nsBuf[:])
		binary.BigEndian.PutUint64(sizeBuf[:], uint64(info.Size()))
		h.Write(sizeBuf[:])
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
	f, err := os.Open(path)
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
