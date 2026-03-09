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

type Entry struct {
	Version    int           `json:"version"`
	CreatedAt  time.Time     `json:"created_at"`
	SourcePath string        `json:"source_path"`
	SessionID  string        `json:"session_id,omitempty"`
	Data       format.Output `json:"data"`
}

// SameSession reports whether this entry was written by the same session as
// the current process (same terminal, editor, or CI job).
func (e *Entry) SameSession(c *Config) bool {
	return e.SessionID != "" && e.SessionID == c.SessionID
}

// Key returns a cache key derived from the absolute path (first 16 hex chars of SHA256).
func Key(absPath string) string {
	h := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(h[:])[:16]
}

// Write writes the output data to the cache for the given absolute path.
func (c *Config) Write(absPath string, data *format.Output) error {
	if err := os.MkdirAll(c.Cache, 0700); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	entry := Entry{
		Version:    1,
		CreatedAt:  time.Now(),
		SourcePath: absPath,
		SessionID:  c.SessionID,
		Data:       *data,
	}

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal cache entry: %w", err)
	}

	path := filepath.Join(c.Cache, Key(absPath)+".json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// Read reads a cached entry for the given absolute path, returning an error if missing or expired.
//
// allowChanged can be set to true to return the entry even if the files have changed (so the cache is stale). This is
// useful for tracking changes in the results between runs, but should be set to false when inspecting results to make
// sure results are up-to-date.
func (c *Config) Read(absPath string, allowChanged bool) (*Entry, error) {
	path := filepath.Join(c.Cache, Key(absPath)+".json")
	return readEntryFile(path, c.TTL, allowChanged)
}

// ReadLatest reads the most recently modified cache entry, returning an error if missing or expired.
func (c *Config) ReadLatest() (*Entry, error) {
	entries, err := os.ReadDir(c.Cache)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no cached results found")
		}
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	var newest os.DirEntry
	var newestTime time.Time
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == nil || info.ModTime().After(newestTime) {
			newest = e
			newestTime = info.ModTime()
		}
	}

	if newest == nil {
		return nil, fmt.Errorf("no cached results found")
	}

	return readEntryFile(filepath.Join(c.Cache, newest.Name()), c.TTL, false)
}

func readEntryFile(path string, ttl time.Duration, allowChanged bool) (*Entry, error) {
	// nolint:gosec // G304: Cache path is derived internally.
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no cached results found")
		}
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	var entry Entry
	if err := json.Unmarshal(b, &entry); err != nil {
		return nil, fmt.Errorf("failed to decode cache entry: %w", err)
	}

	if ttl > 0 && time.Since(entry.CreatedAt) > ttl {
		return nil, fmt.Errorf("cached results expired")
	}

	if entry.SourcePath != "" && !allowChanged {
		if changed := newerFile(entry.SourcePath, entry.CreatedAt); changed != "" {
			return nil, fmt.Errorf("cached results stale (source file changed: %s)", changed)
		}
	}

	return &entry, nil
}

// hasNewerFile walks the source directory and returns true as soon as it finds
// any file modified after the given time. Skips known heavy directories.
func newerFile(root string, since time.Time) string {
	var changed string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort, skip errors
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
