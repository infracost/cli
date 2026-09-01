package cmds

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubUninstallSeams overrides the required-plugin confirmation seams for the
// duration of a test and restores them afterwards.
func stubUninstallSeams(t *testing.T, interactive bool, confirm func(*plugins.UninstallTarget) (bool, error)) {
	t.Helper()
	origInteractive, origConfirm := uninstallIsInteractive, uninstallConfirm
	uninstallIsInteractive = func() bool { return interactive }
	uninstallConfirm = confirm
	t.Cleanup(func() {
		uninstallIsInteractive = origInteractive
		uninstallConfirm = origConfirm
	})
}

// stubUninstallRegistry overrides the registry-load seam so the not-installed
// message can be exercised without a network fetch.
func stubUninstallRegistry(t *testing.T, reg *registry.Registry, err error) {
	t.Helper()
	orig := uninstallRegistryLoad
	uninstallRegistryLoad = func(context.Context) (*registry.Registry, error) { return reg, err }
	t.Cleanup(func() { uninstallRegistryLoad = orig })
}

// newTestCmd returns a command carrying a background context, mirroring how
// cobra populates the context during Execute in production. RunE closures read
// cmd.Context() (e.g. for HTTP requests), which is nil on a bare command.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func newUninstallTestConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	return &config.Config{Plugins: plugins.Config{Cache: dir}}
}

func writeCmdFakeBinary(t *testing.T, dir, binaryName string) string {
	t.Helper()
	path := filepath.Join(dir, binaryName)
	require.NoError(t, os.WriteFile(path, []byte("fake-"+binaryName), 0o750)) //nolint:gosec
	return path
}

func seedRecord(t *testing.T, cfg *config.Config, name, binaryName string) {
	t.Helper()
	st := cfg.Plugins.LoadState()
	st.Records = append(st.Records, plugins.StateRecord{
		Name:        name,
		Version:     "1.0.0",
		Components:  []plugins.StateComponent{{Type: "parser", BinaryName: binaryName}},
		InstalledAt: time.Now().UTC(),
	})
	require.NoError(t, cfg.Plugins.SaveState(st))
}

func requiredTarget() *plugins.UninstallTarget {
	return &plugins.UninstallTarget{
		Name:     "infracost/terraform",
		Required: true,
		Components: []plugins.UninstallComponent{{
			Type:       "parser",
			BinaryName: "infracost-parser-terraform",
			Present:    true,
		}},
	}
}

// notCalledUninstall fails the test if the confirmation prompt is reached.
func notCalledUninstall(t *testing.T) func(*plugins.UninstallTarget) (bool, error) {
	return func(*plugins.UninstallTarget) (bool, error) {
		t.Helper()
		t.Fatal("uninstall confirmation should not have been reached")
		return false, nil
	}
}

func TestConfirmRequiredUninstall_YesSkipsPrompt(t *testing.T) {
	stubUninstallSeams(t, true, notCalledUninstall(t))
	proceed, err := confirmRequiredUninstall(requiredTarget(), true)
	require.NoError(t, err)
	assert.True(t, proceed)
}

func TestConfirmRequiredUninstall_TTYConfirms(t *testing.T) {
	stubUninstallSeams(t, true, func(*plugins.UninstallTarget) (bool, error) {
		return true, nil
	})
	proceed, err := confirmRequiredUninstall(requiredTarget(), false)
	require.NoError(t, err)
	assert.True(t, proceed)
}

func TestConfirmRequiredUninstall_TTYDeclines(t *testing.T) {
	stubUninstallSeams(t, true, func(*plugins.UninstallTarget) (bool, error) {
		return false, nil
	})
	proceed, err := confirmRequiredUninstall(requiredTarget(), false)
	require.NoError(t, err)
	assert.False(t, proceed, "a decline is not an error and leaves the plugin untouched")
}

func TestConfirmRequiredUninstall_NonTTYWithoutYesFails(t *testing.T) {
	stubUninstallSeams(t, false, notCalledUninstall(t))
	proceed, err := confirmRequiredUninstall(requiredTarget(), false)
	require.Error(t, err)
	assert.False(t, proceed)
	assert.Contains(t, err.Error(), "--yes")
}

func TestConfirmRequiredUninstall_NonTTYWithYesProceeds(t *testing.T) {
	stubUninstallSeams(t, false, notCalledUninstall(t))
	proceed, err := confirmRequiredUninstall(requiredTarget(), true)
	require.NoError(t, err)
	assert.True(t, proceed)
}

// TestRunPluginUninstall_RegistryEntryNoPrompt runs the full command flow for a
// registry entry: no confirmation is needed and the binary is removed.
func TestRunPluginUninstall_RegistryEntryNoPrompt(t *testing.T) {
	stubUninstallSeams(t, true, notCalledUninstall(t))

	dir := t.TempDir()
	cfg := newUninstallTestConfig(t, dir)

	path := writeCmdFakeBinary(t, dir, "acme-parser-tf")
	seedRecord(t, cfg, "acme/tf", "acme-parser-tf")

	err := runPluginUninstall(newTestCmd(), cfg, "acme/tf", false)
	require.NoError(t, err)
	assert.NoFileExists(t, path)
	assert.False(t, hasStateRecord(cfg, "acme/tf"))
}

// hasStateRecord reports whether the provenance state holds a record for name.
func hasStateRecord(cfg *config.Config, name string) bool {
	for _, r := range cfg.Plugins.LoadState().Records {
		if r.Name == name {
			return true
		}
	}
	return false
}

// TestRunPluginUninstall_RequiredDeclineLeavesUntouched confirms a declined
// required uninstall exits cleanly and removes nothing.
func TestRunPluginUninstall_RequiredDeclineLeavesUntouched(t *testing.T) {
	stubUninstallSeams(t, true, func(*plugins.UninstallTarget) (bool, error) {
		return false, nil
	})

	dir := t.TempDir()
	cfg := newUninstallTestConfig(t, dir)
	path := writeCmdFakeBinary(t, dir, "infracost-parser-terraform")

	err := runPluginUninstall(newTestCmd(), cfg, "terraform", false)
	require.NoError(t, err)
	assert.FileExists(t, path, "declined uninstall must leave the binary in place")
}

func TestRunPluginUninstall_UnknownNotInstalled(t *testing.T) {
	// Registry unreachable — the message degrades to a plain "not installed".
	stubUninstallRegistry(t, nil, assert.AnError)

	dir := t.TempDir()
	cfg := newUninstallTestConfig(t, dir)

	err := runPluginUninstall(newTestCmd(), cfg, "acme/nope", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not installed")
}

func TestRunPluginUninstall_InRegistryButNotInstalled(t *testing.T) {
	reg := &registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{{
			Name:       "acme/tf",
			Components: []registry.Component{{Type: registry.ComponentTypeParser, BinaryName: "acme-parser-tf"}},
		}},
	}
	stubUninstallRegistry(t, reg, nil)

	dir := t.TempDir()
	cfg := newUninstallTestConfig(t, dir)

	err := runPluginUninstall(newTestCmd(), cfg, "acme/tf", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exists in the registry but is not installed")
	assert.Contains(t, err.Error(), "infracost plugin install acme/tf")
}

func TestRunPluginUninstall_DevOverrideRefused(t *testing.T) {
	dir := t.TempDir()
	cfg := newUninstallTestConfig(t, dir)
	cfg.Plugins.Dir = dir

	err := runPluginUninstall(newTestCmd(), cfg, "acme/tf", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
}
