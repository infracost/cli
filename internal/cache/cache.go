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

	sid, _ := c.sessionID()

	m.Entries[key] = ManifestEntry{
		Version:    1,
		CreatedAt:  time.Now(),
		SourcePath: absPath,
		SessionID:  sid,
	}

	if sid != "" {
		m.Sessions[sid] = key
	}

	if err := c.SaveManifest(m); err != nil {
		return fmt.Errorf("failed to save manifest: %w", err)
	}

	return nil
}

// Read looks up a cached result for the given absolute path.
//
// If a session ID is configured, it checks both the session-based entry and
// the path-based entry, returning whichever is most recent. The session-based
// entry is not subject to TTL. The path-based entry must be within TTL.
//
// If no session ID is configured, only the path+TTL lookup is used.
//
// When allowStale is false, the entry is rejected if any source file has been
// modified since the entry was created. Set allowStale to true when reading
// for comparison rather than inspection.
func (c *Config) Read(absPath string, allowStale bool) (*format.Output, error) {
	m, err := c.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to load manifest: %w", err)
	}

	key := Key(absPath)

	var pathEntry *ManifestEntry
	if e, ok := m.Entries[key]; ok && c.TTL > 0 && time.Since(e.CreatedAt) <= c.TTL {
		pathEntry = &e
	}

	sid, explicit := c.sessionID()

	var sessionEntry *ManifestEntry
	if sid != "" {
		if sessionKey, ok := m.Sessions[sid]; ok {
			if sessionKey == key && pathEntry != nil {
				// Session and path point to the same entry; no need to compare.
				if !allowStale && pathEntry.SourcePath != "" {
					if changed := newerFile(pathEntry.SourcePath, pathEntry.CreatedAt); changed != "" {
						return nil, fmt.Errorf("cached results stale (source file changed: %s)", changed)
					}
				}
				return readDataFile(c.Cache, key)
			}
			if e, ok := m.Entries[sessionKey]; ok {
				// Explicit sessions skip TTL; terminal sessions still check it.
				if explicit || (c.TTL > 0 && time.Since(e.CreatedAt) <= c.TTL) {
					sessionEntry = &e
				}
			}
		}
	}

	// Pick the best match.
	var best *ManifestEntry
	switch {
	case pathEntry != nil && sessionEntry != nil:
		if sessionEntry.CreatedAt.After(pathEntry.CreatedAt) {
			best = sessionEntry
		} else {
			best = pathEntry
		}
	case sessionEntry != nil:
		best = sessionEntry
	case pathEntry != nil:
		best = pathEntry
	default:
		return nil, fmt.Errorf("no cached results found")
	}

	if !allowStale && best.SourcePath != "" {
		if changed := newerFile(best.SourcePath, best.CreatedAt); changed != "" {
			return nil, fmt.Errorf("cached results stale (source file changed: %s)", changed)
		}
	}

	return readDataFile(c.Cache, Key(best.SourcePath))
}

// ReadLatest returns the most recent cached result.
//
// If a session ID is configured, the session's entry is returned. Otherwise,
// the entry with the most recent CreatedAt across the entire manifest is used.
func (c *Config) ReadLatest() (*format.Output, error) {
	m, err := c.LoadManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to load manifest: %w", err)
	}

	if len(m.Entries) == 0 {
		return nil, fmt.Errorf("no cached results found")
	}

	sid, explicit := c.sessionID()

	if sid != "" {
		if sessionKey, ok := m.Sessions[sid]; ok {
			if e, ok := m.Entries[sessionKey]; ok {
				if explicit {
					// Explicit session — caller manages freshness.
					return readDataFile(c.Cache, sessionKey)
				}
				// Terminal session — still check TTL.
				if c.TTL <= 0 || time.Since(e.CreatedAt) <= c.TTL {
					return readDataFile(c.Cache, sessionKey)
				}
			}
		}
	}

	// No session match — find the most recent entry within TTL.
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

	best := m.Entries[newestKey]
	if best.SourcePath != "" {
		if changed := newerFile(best.SourcePath, best.CreatedAt); changed != "" {
			return nil, fmt.Errorf("cached results stale (source file changed: %s)", changed)
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

// newerFile walks the source directory and returns the path of the first file
// modified after the given time. Skips known heavy directories.
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

