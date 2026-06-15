package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerLoadParserPluginsMissingDir(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)

	plugins, err := mgr.LoadParserPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}

func TestManagerLoadParserPluginsSkipsSidecars(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-plugin-terraform.sha256"), []byte("abc"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-plugin-terraform.version"), []byte("1.0.0"), 0o600))

	mgr, err := NewManager(dir)
	require.NoError(t, err)

	plugins, err := mgr.LoadParserPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}

func TestManagerLoadParserPluginsSkipsKnownProviderPlugins(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"infracost-plugin-aws", "infracost-plugin-google", "infracost-plugin-azure"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("not really a binary"), 0o700))
	}

	mgr, err := NewManager(dir)
	require.NoError(t, err)

	plugins, err := mgr.LoadParserPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}
