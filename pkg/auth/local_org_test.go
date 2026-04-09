package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLocalOrg(t *testing.T) {
	t.Run("returns empty when file does not exist", func(t *testing.T) {
		slug, err := ReadLocalOrg(t.TempDir())
		require.NoError(t, err)
		assert.Empty(t, slug)
	})

	t.Run("reads slug from file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".infracost"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".infracost", "org"), []byte("acme-corp\n"), 0644))

		slug, err := ReadLocalOrg(dir)
		require.NoError(t, err)
		assert.Equal(t, "acme-corp", slug)
	})

	t.Run("trims whitespace", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".infracost"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".infracost", "org"), []byte("  acme-corp  \n"), 0644))

		slug, err := ReadLocalOrg(dir)
		require.NoError(t, err)
		assert.Equal(t, "acme-corp", slug)
	})
}

func TestWriteLocalOrg(t *testing.T) {
	t.Run("creates directory and file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, WriteLocalOrg(dir, "acme-corp"))

		data, err := os.ReadFile(filepath.Join(dir, ".infracost", "org"))
		require.NoError(t, err)
		assert.Equal(t, "acme-corp\n", string(data))
	})

	t.Run("overwrites existing file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, WriteLocalOrg(dir, "old-org"))
		require.NoError(t, WriteLocalOrg(dir, "new-org"))

		data, err := os.ReadFile(filepath.Join(dir, ".infracost", "org"))
		require.NoError(t, err)
		assert.Equal(t, "new-org\n", string(data))
	})
}
