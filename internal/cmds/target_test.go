package cmds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTargetNotExistError(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")

	err := targetNotExistError(missing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "target directory does not exist")
	assert.Contains(t, err.Error(), missing)
	assert.Contains(t, err.Error(), "looked for it from:")
}

func TestNearbyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "Terraform"), 0750))

	t.Run("case difference is suggested", func(t *testing.T) {
		assert.Equal(t, filepath.Join(dir, "Terraform"), nearbyPath(filepath.Join(dir, "terraform")))
	})

	t.Run("prefix is suggested", func(t *testing.T) {
		assert.Equal(t, filepath.Join(dir, "Terraform"), nearbyPath(filepath.Join(dir, "Terra")))
	})

	t.Run("nothing alike yields nothing", func(t *testing.T) {
		assert.Empty(t, nearbyPath(filepath.Join(dir, "modules")))
	})

	t.Run("two candidates are ambiguous, so neither is offered", func(t *testing.T) {
		ambiguous := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(ambiguous, "envs-dev"), 0750))
		require.NoError(t, os.Mkdir(filepath.Join(ambiguous, "envs-prod"), 0750))

		assert.Empty(t, nearbyPath(filepath.Join(ambiguous, "envs")))
	})

	t.Run("missing parent yields nothing", func(t *testing.T) {
		assert.Empty(t, nearbyPath(filepath.Join(dir, "gone", "deeper")))
	})
}

func TestNearbyPathIgnoresVeryShortSiblings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "s"), 0750))

	assert.Empty(t, nearbyPath(filepath.Join(dir, "src-typo")))
}
