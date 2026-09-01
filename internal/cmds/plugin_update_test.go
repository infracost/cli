package cmds

import (
	"context"
	"testing"

	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUpdateRegistry overrides the registry-load seam used by the update command
// so resolution paths can be exercised without a network fetch. It returns a
// pointer that reports whether the seam was consulted.
func stubUpdateRegistry(t *testing.T, reg *registry.Registry, err error) *bool {
	t.Helper()
	loaded := false
	orig := updateRegistryLoad
	updateRegistryLoad = func(context.Context) (*registry.Registry, error) {
		loaded = true
		return reg, err
	}
	t.Cleanup(func() { updateRegistryLoad = orig })
	return &loaded
}

func TestRunPluginUpdate_RefusesUnderDevOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := newUninstallTestConfig(t, dir)
	cfg.Plugins.Dir = dir // dev-override in effect

	t.Run("update-all", func(t *testing.T) {
		err := runPluginUpdateAll(newTestCmd(), cfg, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
	})

	t.Run("update <name>", func(t *testing.T) {
		err := runPluginUpdateOne(newTestCmd(), cfg, "terraform", false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
	})
}

func TestRunPluginUpdateOne_UnknownNameHintsPluginList(t *testing.T) {
	cfg := newUninstallTestConfig(t, t.TempDir())
	reg := &registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{{
			Name:       "acme/other",
			Components: []registry.Component{{Type: registry.ComponentTypeParser, BinaryName: "acme-other"}},
		}},
	}
	stubUpdateRegistry(t, reg, nil)

	err := runPluginUpdateOne(newTestCmd(), cfg, "does-not-exist", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plugin list")
}

func TestRunPluginUpdateOne_NotInstalledEntry(t *testing.T) {
	cfg := newUninstallTestConfig(t, t.TempDir())
	reg := &registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{{
			Name:       "acme/tf",
			Components: []registry.Component{{Type: registry.ComponentTypeParser, BinaryName: "acme-parser-tf"}},
		}},
	}
	stubUpdateRegistry(t, reg, nil)

	// No provenance record for acme/tf → an explicit update reports it is not
	// installed rather than trying to update it.
	err := runPluginUpdateOne(newTestCmd(), cfg, "acme/tf", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
}

func TestRunPluginUpdateOne_RequiredNameBypassesRegistry(t *testing.T) {
	cfg := newUninstallTestConfig(t, t.TempDir())
	// A dead base URL makes the managed required-set download fail fast; the point
	// of the test is that the registry seam is never consulted for a built-in name.
	cfg.Plugins.BaseURL = "http://127.0.0.1:1"
	loaded := stubUpdateRegistry(t, &registry.Registry{SchemaVersion: registry.SupportedSchemaVersion}, nil)

	// "terraform" is a built-in required key → the required-set path handles it.
	_ = runPluginUpdateOne(newTestCmd(), cfg, "terraform", false)
	assert.False(t, *loaded, "a built-in required name must not trigger a registry fetch")
}

func TestNamedComponent(t *testing.T) {
	entry := &registry.Entry{
		Name: "acme/tf",
		Components: []registry.Component{
			{Type: registry.ComponentTypeParser, BinaryName: "acme-parser-tf"},
			{Type: registry.ComponentTypeProvider, BinaryName: "acme-provider-tf"},
		},
	}

	assert.Equal(t, "acme-parser-tf", namedComponent(entry, "acme-parser-tf"))
	assert.Equal(t, "acme-provider-tf", namedComponent(entry, "acme-provider-tf"))
	assert.Equal(t, "acme-parser-tf", namedComponent(entry, "acme-parser-tf.exe"))
	assert.Equal(t, "", namedComponent(entry, "acme/tf"))
	assert.Equal(t, "", namedComponent(entry, "unrelated"))
}
