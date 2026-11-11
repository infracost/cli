package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoveExistingPluginPath covers the paths an update takes before it
// writes the new binary. The Windows-only behavior is exercised by calling
// displaceWindowsBinary directly, since the lock these guard against cannot be
// reproduced on another OS.
func TestRemoveExistingPluginPath(t *testing.T) {
	t.Run("missing path is not an error", func(t *testing.T) {
		require.NoError(t, removeExistingPluginPath(filepath.Join(t.TempDir(), "absent")))
	})

	t.Run("an old directory install is removed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "infracost-terraform")
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o750))

		require.NoError(t, removeExistingPluginPath(dir))
		assert.NoFileExists(t, dir)
	})

	t.Run("a file is left in place except on Windows", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "infracost-terraform")
		require.NoError(t, os.WriteFile(path, []byte("binary"), 0o600))

		require.NoError(t, removeExistingPluginPath(path))

		if runtime.GOOS == "windows" {
			assert.NoFileExists(t, path)
			return
		}
		// Elsewhere the rename over the top is atomic, so nothing is moved.
		assert.FileExists(t, path)
	})
}

func TestDisplaceWindowsBinary(t *testing.T) {
	t.Run("the binary is moved out of the way", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "infracost-terraform.exe")
		require.NoError(t, os.WriteFile(path, []byte("old binary"), 0o600))

		require.NoError(t, displaceWindowsBinary(path))

		assert.NoFileExists(t, path, "the path must be free for the replacement")
	})

	t.Run("a missing binary is not an error", func(t *testing.T) {
		require.NoError(t, displaceWindowsBinary(filepath.Join(t.TempDir(), "absent.exe")))
	})

	t.Run("displaced copies left by earlier updates are swept", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "infracost-terraform.exe")
		stale := path + ".123" + displacedSuffix
		require.NoError(t, os.WriteFile(path, []byte("old binary"), 0o600))
		require.NoError(t, os.WriteFile(stale, []byte("older binary"), 0o600))

		require.NoError(t, displaceWindowsBinary(path))

		assert.NoFileExists(t, stale)
	})
}

// TestIsPluginSidecar keeps a displaced or half-written binary from being
// loaded as a plugin, which would run the version the update just replaced.
func TestIsPluginSidecar(t *testing.T) {
	tests := map[string]bool{
		"infracost-terraform":             false,
		"infracost-terraform.exe":         false,
		"infracost-terraform.sha256":      true,
		"infracost-terraform.version":     true,
		"infracost-terraform.exe.123.old": true,
		"infracost-terraform.exe.tmp":     true,
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, want, isPluginSidecar(name))
		})
	}
}
