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

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/pkg/logging"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"google.golang.org/protobuf/proto"
)

// parserFingerprintSkipDirs mirrors internal/cache.skipDirs — directories
// that don't contribute to the parse result and would just slow down the
// walk (.git is the killer; node_modules is also pathological).
var parserFingerprintSkipDirs = map[string]bool{
	".terraform":        true,
	".terragrunt-cache": true,
	".git":              true,
	"node_modules":      true,
	".idea":             true,
	".vscode":           true,
}

// fingerprintHexLen is the byte length of the hex-encoded SHA256
// fingerprint stored at the head of every parser-results cache file.
const fingerprintHexLen = 64

// fingerprintProject walks projectDir recursively and produces a hex
// SHA256 of every file's (relative-path, mtime-ns, size). Skip dirs
// match the existing internal/cache.skipDirs convention. The extra
// argument is mixed in first so the fingerprint also changes when
// parser inputs (RawOptions / RawOptionsFormat) change — otherwise a
// tfvars file swap that doesn't touch any *.tf would be a cache hit.
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
			if path != projectDir && parserFingerprintSkipDirs[d.Name()] {
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

// projectCacheFilename hashes the absolute project path into a stable
// filename (one file per project). The fingerprint validates freshness
// inside that file — keying the filename by path means we overwrite the
// project's entry on every scan instead of accumulating one new file
// per content change.
func projectCacheFilename(absProjectPath string) string {
	h := sha256.Sum256([]byte(absProjectPath))
	return hex.EncodeToString(h[:]) + ".pb"
}

// loadParsedResponse returns a previously-cached Parse response for
// absProjectPath whose stored fingerprint matches the supplied one.
// Returns nil for any failure (cache miss, fingerprint mismatch,
// unreadable / corrupt file) — the caller falls back to re-parsing.
func loadParsedResponse(absProjectPath, fingerprint string) *pluginpb.ParseResponse {
	path := filepath.Join(cache.ParserResultsDir(), projectCacheFilename(absProjectPath))
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
// loadParsedResponse can detect a stale match. Best-effort — write
// failures are logged and swallowed so a flaky cache write never aborts
// a scan.
func saveParsedResponse(absProjectPath, fingerprint string, resp *pluginpb.ParseResponse) {
	dir := cache.ParserResultsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logging.Warnf("failed to create parser results cache dir %q: %s", dir, err)
		return
	}

	data, err := proto.Marshal(resp)
	if err != nil {
		logging.Warnf("failed to marshal parser response for cache: %s", err)
		return
	}

	path := filepath.Join(dir, projectCacheFilename(absProjectPath))
	tmp := path + ".tmp"
	out := make([]byte, 0, fingerprintHexLen+len(data))
	out = append(out, []byte(fingerprint)...)
	out = append(out, data...)
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		logging.Warnf("failed to write parser cache %q: %s", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logging.Warnf("failed to commit parser cache %q: %s", path, err)
		_ = os.Remove(tmp)
	}
}
