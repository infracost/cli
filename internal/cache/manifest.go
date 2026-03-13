package cache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Manifest holds the index of all cache entries.
type Manifest struct {
	// Entries is keyed by the cache key (hash of the absolute source path).
	Entries map[string]ManifestEntry `json:"entries"`
}

// ManifestEntry holds metadata for a single cached result.
type ManifestEntry struct {
	// Version is the schema version of the cache entry.
	Version int
	// CreatedAt is when this entry was written.
	CreatedAt time.Time
	// SourcePath is the absolute path of the directory that was scanned.
	SourcePath string
}

func (c *Config) LoadManifest() (*Manifest, error) {
	if c.manifest != nil {
		// just load the manifest once
		return c.manifest, nil
	}

	path := filepath.Join(c.Cache, "manifest.json")

	c.manifest = &Manifest{
		Entries: make(map[string]ManifestEntry),
	}

	// nolint:gosec // G304: Cache path is derived internally.
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c.manifest, nil
		}
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	if err := json.NewDecoder(f).Decode(c.manifest); err != nil {
		return nil, err
	}

	return c.manifest, nil
}

func (c *Config) SaveManifest(m *Manifest) error {
	path := filepath.Join(c.Cache, "manifest.json")

	// nolint:gosec // G304: Cache path is derived internally.
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	return json.NewEncoder(f).Encode(m)
}