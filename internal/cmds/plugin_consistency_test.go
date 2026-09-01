package cmds

import (
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPluginCommands_DevOverrideRefusalShape checks the command wrappers that
// own their own refusal strings (update-all, update-one, uninstall) all reject
// with the same INFRACOST_CLI_PLUGIN_DIR message shape and a non-nil error
// (non-zero exit), independent of the shared pkg-level check the install path
// delegates to. Only the verb differs between commands.
func TestPluginCommands_DevOverrideRefusalShape(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Plugins: plugins.Config{Dir: dir, Cache: dir}}

	updateAllErr := pluginsUpdateCmd(cfg).RunE(newTestCmd(), nil)
	updateOneErr := pluginsUpdateCmd(cfg).RunE(newTestCmd(), []string{"acme/kubewidget"})
	uninstallErr := pluginsUninstallCmd(cfg).RunE(newTestCmd(), []string{"acme/kubewidget"})

	for _, err := range []error{updateAllErr, updateOneErr, uninstallErr} {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR is set ("+dir+")")
		assert.Contains(t, err.Error(), "unset it to manage plugins automatically")
	}

	assert.Contains(t, updateAllErr.Error(), "plugin updates are disabled")
	assert.Contains(t, updateOneErr.Error(), "plugin updates are disabled")
	assert.Contains(t, uninstallErr.Error(), "plugin uninstalls are disabled")
}
