package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/format"
)

var skipDirs = map[string]bool{
	".terraform":        true,
	".terragrunt-cache": true,
	".git":              true,
	"node_modules":      true,
	".idea":             true,
	".vscode":           true,
}

// Key returns a cache key derived from the absolute path (first 16 hex chars of SHA256).
func Key(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(h[:])[:16]
}

// Write writes the output data to the cache for the given absolute path and
// updates the manifest accordingly.
func (c *Config) Write(absPath string, data *format.Output) error {
	if err := os.MkdirAll(c.Cache, 0700); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	key := Key(absPath)

	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal cache data: %w", err)
	}

	path := filepath.Join(c.Cache, key+".json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	m, err := c.LoadManifest()
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	m.Entries[key] = ManifestEntry{
		Version:    1,
		CreatedAt:  time.Now(),
		SourcePath: absPath,
	}

	if err := c.SaveManifest(m); err != nil {
		return fmt.Errorf("failed to save manifest: %w", err)
	}

	return nil
}

// ForPath returns cached results for the given absolute path.
//
// The entry must be within TTL and the source files must not have been modified
// since the entry was created.
func (c *Config) ForPath(absPath string) (*format.Output, error) {
	m, err := c.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to load manifest: %w", err)
	}

	key := Key(absPath)

	e, ok := m.Entries[key]
	if !ok || c.TTL <= 0 || time.Since(e.CreatedAt) > c.TTL {
		return nil, fmt.Errorf("no cached results found")
	}

	if e.SourcePath != "" {
		if changed := newerFile(e.SourcePath, e.CreatedAt); changed != "" {
			return nil, fmt.Errorf("cached results stale (source file changed: %s)", changed)
		}
	}

	return readDataFile(c.Cache, key)
}

// ForPathAllowStale returns cached results for the given absolute path without
// checking whether source files have been modified since the entry was created.
// The entry must still be within TTL.
func (c *Config) ForPathAllowStale(absPath string) (*format.Output, error) {
	m, err := c.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to load manifest: %w", err)
	}

	key := Key(absPath)

	e, ok := m.Entries[key]
	if !ok || c.TTL <= 0 || time.Since(e.CreatedAt) > c.TTL {
		return nil, fmt.Errorf("no cached results found")
	}

	return readDataFile(c.Cache, key)
}

// Latest returns the most recent cached result within TTL.
//
// When allowStale is false, the entry is also rejected if any source file has
// been modified since the entry was created. Set allowStale to true when
// reading for comparison (e.g. diffing against a prior run).
func (c *Config) Latest(allowStale bool) (*format.Output, error) {
	m, err := c.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to load manifest: %w", err)
	}

	var newestKey string
	var newestTime time.Time
	for key, e := range m.Entries {
		if c.TTL > 0 && time.Since(e.CreatedAt) > c.TTL {
			continue
		}
		if newestKey == "" || e.CreatedAt.After(newestTime) {
			newestKey = key
			newestTime = e.CreatedAt
		}
	}

	if newestKey == "" {
		return nil, fmt.Errorf("no cached results found")
	}

	if !allowStale {
		best := m.Entries[newestKey]
		if best.SourcePath != "" {
			if changed := newerFile(best.SourcePath, best.CreatedAt); changed != "" {
				return nil, fmt.Errorf("cached results stale (source file changed: %s)", changed)
			}
		}
	}

	return readDataFile(c.Cache, newestKey)
}

func readDataFile(cacheDir, key string) (*format.Output, error) {
	path := filepath.Join(cacheDir, key+".json")

	// nolint:gosec // G304: Cache path is derived internally.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var output format.Output
	if err := json.NewDecoder(f).Decode(&output); err != nil {
		return nil, fmt.Errorf("failed to decode cache data: %w", err)
	}

	return &output, nil
}

// newerFile walks root looking for any file modified after since. It returns
// the path of the first such file, or "" if none are found. If root does not
// exist (e.g. a temporary directory that has been cleaned up), the walk
// silently returns "" so the cached entry is still considered fresh.
func newerFile(root string, since time.Time) string {
	var changed string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(since) {
			changed = path
			return filepath.SkipAll
		}
		return nil
	})
	return changed
}

// ReadFile reads a raw JSON output file (not a cache wrapper).
func ReadFile(path string) (*format.Output, error) {
	// nolint:gosec // G304: User provides the path explicitly via --file flag.
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var output format.Output
	if err := json.Unmarshal(b, &output); err != nil {
		return nil, fmt.Errorf("failed to decode JSON: %w", err)
	}

	return &output, nil
}