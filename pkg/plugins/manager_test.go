package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPluginIdentityDedupesOnNameAndType documents that discovery keys plugins
// on the (name, type) pair: a parser and a provider sharing a name are two
// distinct plugins, while two plugins of the same name and type collide.
func TestPluginIdentityDedupesOnNameAndType(t *testing.T) {
	seen := map[pluginIdentity]string{}

	parser := pluginIdentity{name: "infracost/kubernetes", typ: pb.PluginType_PARSER}
	provider := pluginIdentity{name: "infracost/kubernetes", typ: pb.PluginType_PROVIDER}

	seen[parser] = "infracost-plugin-kubernetes-parser"
	// Same name, different type — must not be treated as a duplicate.
	_, dup := seen[provider]
	assert.False(t, dup, "parser and provider sharing a name should be distinct identities")

	seen[provider] = "infracost-plugin-kubernetes"
	// Same name and type — must collide.
	_, dup = seen[pluginIdentity{name: "infracost/kubernetes", typ: pb.PluginType_PARSER}]
	assert.True(t, dup, "same name and type should be a duplicate")
}

func TestRemoveLegacyPluginsDropsRenamedBinaries(t *testing.T) {
	dir := t.TempDir()
	// A legacy-named binary left by an older CLI, plus its renamed
	// replacement and an unrelated third-party plugin.
	legacy := filepath.Join(dir, pluginBinaryName("infracost-plugin-terraform"))
	renamed := filepath.Join(dir, pluginBinaryName("infracost-parser-terraform"))
	thirdParty := filepath.Join(dir, pluginBinaryName("infracost-plugin-custom"))
	for _, p := range []string{legacy, renamed, thirdParty} {
		require.NoError(t, os.WriteFile(p, []byte("binary"), 0o600))
	}

	NewManager(ManagerOptions{Dir: dir}).removeLegacyPlugins()

	_, err := os.Stat(legacy)
	assert.True(t, os.IsNotExist(err), "legacy binary should be removed")
	assert.FileExists(t, renamed, "renamed binary should be kept")
	assert.FileExists(t, thirdParty, "non-required plugin should be kept")
}

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
