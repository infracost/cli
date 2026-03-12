package cache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)


const sessionMaxAge = 24 * time.Hour

// Manifest holds the index of all cache entries.
type Manifest struct {
	// Entries is keyed by the cache key (hash of the absolute source path).
	Entries map[string]ManifestEntry `json:"entries"`

	// Sessions maps session IDs to cache keys for fast session-based lookups.
	Sessions map[string]string `json:"sessions"`
}

// ManifestEntry holds metadata for a single cached result.
type ManifestEntry struct {
	// Version is the schema version of the cache entry.
	Version int
	// CreatedAt is when this entry was written.
	CreatedAt time.Time
	// SourcePath is the absolute path of the directory that was scanned.
	SourcePath string
	// SessionID is the session that produced this entry, if any.
	SessionID string
}

func (c *Config) LoadManifest() (*Manifest, error) {
	if c.manifest != nil {
		// just load the manifest once
		return c.manifest, nil
	}

	path := filepath.Join(c.Cache, "manifest.json")

	c.manifest = &Manifest{
		Entries:  make(map[string]ManifestEntry),
		Sessions: make(map[string]string),
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c.manifest, nil
		}
		return nil, err
	}
	defer func () {
		_ = f.Close()
	}()

	if err := json.NewDecoder(f).Decode(c.manifest); err != nil {
		return nil, err
	}

	return c.manifest, nil
}

func (c *Config) SaveManifest(m *Manifest) error {
	// Prune stale session mappings. The entry data is kept — only the
	// session shortcut is removed so the Sessions map doesn't grow unbounded.
	for sid, key := range m.Sessions {
		if e, ok := m.Entries[key]; !ok || time.Since(e.CreatedAt) > sessionMaxAge {
			delete(m.Sessions, sid)
		}
	}

	path := filepath.Join(c.Cache, "manifest.json")

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	return json.NewEncoder(f).Encode(m)
}
