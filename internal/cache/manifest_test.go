package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadManifestMissing(t *testing.T) {
	c := testConfig(t)

	m, err := c.LoadManifest()
	require.NoError(t, err)
	assert.Empty(t, m.Entries)
}

func TestLoadManifestCached(t *testing.T) {
	c := testConfig(t)

	m1, err := c.LoadManifest()
	require.NoError(t, err)

	m2, err := c.LoadManifest()
	require.NoError(t, err)

	assert.Same(t, m1, m2, "LoadManifest should return the same pointer on subsequent calls")
}

func TestSaveAndLoadManifest(t *testing.T) {
	c := testConfig(t)
	require.NoError(t, os.MkdirAll(c.Cache, 0700))

	m := &Manifest{
		Entries: map[string]ManifestEntry{
			"abc123": {
				Version:    1,
				CreatedAt:  time.Now(),
				SourcePath: "/test/path",
			},
		},
	}

	err := c.SaveManifest(m)
	require.NoError(t, err)

	// Load into a fresh config to verify persistence.
	c2 := &Config{Cache: c.Cache}
	loaded, err := c2.LoadManifest()
	require.NoError(t, err)

	assert.Len(t, loaded.Entries, 1)
	assert.Equal(t, "/test/path", loaded.Entries["abc123"].SourcePath)
}

func TestSaveManifestWritesToDisk(t *testing.T) {
	c := testConfig(t)
	require.NoError(t, os.MkdirAll(c.Cache, 0700))

	m := &Manifest{
		Entries: map[string]ManifestEntry{},
	}

	err := c.SaveManifest(m)
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(c.Cache, "manifest.json"))
	require.NoError(t, err, "manifest.json should exist on disk after save")
}