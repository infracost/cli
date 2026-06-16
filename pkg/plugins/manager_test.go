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
	mgr := NewManager(ManagerOptions{Dir: filepath.Join(t.TempDir(), "missing"), SkipInstall: true})

	plugins, err := mgr.LoadParserPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}

func TestManagerLoadParserPluginsSkipsSidecars(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-plugin-terraform.sha256"), []byte("abc"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-plugin-terraform.version"), []byte("1.0.0"), 0o600))

	mgr := NewManager(ManagerOptions{Dir: dir, SkipInstall: true})

	plugins, err := mgr.LoadParserPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}

func TestManagerLoadProviderPluginsMissingDir(t *testing.T) {
	mgr := NewManager(ManagerOptions{Dir: filepath.Join(t.TempDir(), "missing"), SkipInstall: true})

	plugins, err := mgr.LoadProviderPlugins(context.Background())
	require.NoError(t, err)
	assert.Empty(t, plugins)
}
