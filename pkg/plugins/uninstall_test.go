package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeBinary creates a fake plugin binary (and optionally its sidecars) in
// dir, returning the binary path.
func writeFakeBinary(t *testing.T, dir, binaryName string, sidecars bool) string {
	t.Helper()
	path := filepath.Join(dir, pluginBinaryName(binaryName))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte("fake-"+binaryName), 0o750)) //nolint:gosec
	if sidecars {
		require.NoError(t, os.WriteFile(path+".sha256", []byte("deadbeef"), 0o600))
		require.NoError(t, os.WriteFile(path+".version", []byte("1.0.0"), 0o600))
	}
	return path
}

// installedRecord seeds a provenance record for a registry entry with its
// component binaries written to dir.
func installedRecord(t *testing.T, cfg *Config, name, version string, comps ...StateComponent) {
	t.Helper()
	st := cfg.LoadState()
	st.upsert(StateRecord{
		Name:        name,
		Version:     version,
		Components:  comps,
		Official:    false,
		Author:      "someone",
		InstalledAt: time.Now().UTC(),
	})
	require.NoError(t, cfg.SaveState(st))
}

func TestResolveUninstall_RegistryEntryFullRemoval(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	parserPath := writeFakeBinary(t, dir, "acme-parser-k8s", true)
	providerPath := writeFakeBinary(t, dir, "acme-provider-k8s", true)
	installedRecord(t, cfg, "acme/k8s", "2.0.0",
		StateComponent{Type: pluginTypeParser, BinaryName: "acme-parser-k8s"},
		StateComponent{Type: pluginTypeProvider, BinaryName: "acme-provider-k8s"},
	)

	target, err := cfg.ResolveUninstall("acme/k8s")
	require.NoError(t, err)
	assert.True(t, target.HasRecord)
	assert.False(t, target.Required)
	assert.True(t, target.Actionable())
	require.Len(t, target.Components, 2)

	res, err := cfg.Uninstall(target)
	require.NoError(t, err)
	assert.Len(t, res.Removed, 2)
	assert.True(t, res.RecordRemoved)

	// Binaries and sidecars gone.
	for _, p := range []string{parserPath, providerPath} {
		assert.NoFileExists(t, p)
		assert.NoFileExists(t, p+".sha256")
		assert.NoFileExists(t, p+".version")
	}
	// Provenance record gone.
	assert.Nil(t, cfg.LoadState().find("acme/k8s"))
}

func TestResolveUninstall_ComponentNameResolvesToEntry(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	writeFakeBinary(t, dir, "acme-parser-k8s", false)
	writeFakeBinary(t, dir, "acme-provider-k8s", false)
	installedRecord(t, cfg, "acme/k8s", "2.0.0",
		StateComponent{Type: pluginTypeParser, BinaryName: "acme-parser-k8s"},
		StateComponent{Type: pluginTypeProvider, BinaryName: "acme-provider-k8s"},
	)

	// Naming a single component resolves to the owning entry with all its
	// components and flags the scope expansion.
	target, err := cfg.ResolveUninstall("acme-provider-k8s")
	require.NoError(t, err)
	assert.Equal(t, "acme/k8s", target.Name)
	assert.Equal(t, "acme-provider-k8s", target.NamedComponent)
	require.Len(t, target.Components, 2)
}

func TestUninstall_RecordWithoutBinaryIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	// Record exists but the binary was hand-deleted.
	installedRecord(t, cfg, "acme/tf", "1.0.0",
		StateComponent{Type: pluginTypeParser, BinaryName: "acme-parser-tf"},
	)

	target, err := cfg.ResolveUninstall("acme/tf")
	require.NoError(t, err)
	assert.True(t, target.Actionable(), "a record makes the target actionable even with no binary")

	res, err := cfg.Uninstall(target)
	require.NoError(t, err)
	assert.Empty(t, res.Removed)
	require.Len(t, res.Missing, 1)
	assert.True(t, res.RecordRemoved)
	assert.Nil(t, cfg.LoadState().find("acme/tf"))
}

func TestUninstall_PartiallyMissingComponent(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	parserPath := writeFakeBinary(t, dir, "acme-parser-k8s", false)
	// provider binary intentionally absent
	installedRecord(t, cfg, "acme/k8s", "2.0.0",
		StateComponent{Type: pluginTypeParser, BinaryName: "acme-parser-k8s"},
		StateComponent{Type: pluginTypeProvider, BinaryName: "acme-provider-k8s"},
	)

	target, err := cfg.ResolveUninstall("acme/k8s")
	require.NoError(t, err)

	res, err := cfg.Uninstall(target)
	require.NoError(t, err)
	require.Len(t, res.Removed, 1)
	assert.Equal(t, "acme-parser-k8s", res.Removed[0].BinaryName)
	require.Len(t, res.Missing, 1)
	assert.Equal(t, "acme-provider-k8s", res.Missing[0].BinaryName)
	assert.True(t, res.RecordRemoved)
	assert.NoFileExists(t, parserPath)
}

func TestResolveUninstall_RequiredByKeyAndName(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	writeFakeBinary(t, dir, "infracost-parser-terraform", false)

	for _, input := range []string{"terraform", "infracost/terraform", "infracost-parser-terraform"} {
		target, err := cfg.ResolveUninstall(input)
		require.NoError(t, err, input)
		assert.True(t, target.Required, input)
		assert.False(t, target.HasRecord, input)
		assert.Equal(t, "infracost/terraform", target.Name, input)
		require.Len(t, target.Components, 1, input)
		assert.True(t, target.Actionable(), input)
	}
}

func TestResolveUninstall_RequiredNotInstalledNotActionable(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	// No binary on disk; a required plugin still resolves but isn't actionable.
	target, err := cfg.ResolveUninstall("terraform")
	require.NoError(t, err)
	assert.True(t, target.Required)
	assert.False(t, target.Actionable())
}

func TestResolveUninstall_RequiredKubernetesRemovesBothComponents(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	writeFakeBinary(t, dir, "infracost-parser-kubernetes", false)
	writeFakeBinary(t, dir, "infracost-provider-kubernetes", false)

	// Naming one component of the shared-key kubernetes entry expands to both.
	target, err := cfg.ResolveUninstall("infracost-provider-kubernetes")
	require.NoError(t, err)
	assert.True(t, target.Required)
	assert.Equal(t, "infracost/kubernetes", target.Name)
	assert.Equal(t, "infracost-provider-kubernetes", target.NamedComponent)
	require.Len(t, target.Components, 2)

	res, err := cfg.Uninstall(target)
	require.NoError(t, err)
	assert.Len(t, res.Removed, 2)
	assert.False(t, res.RecordRemoved, "required plugins never carry a provenance record")
}

func TestResolveUninstall_UnmanagedBinary(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	path := writeFakeBinary(t, dir, "some-random-plugin", true)

	target, err := cfg.ResolveUninstall("some-random-plugin")
	require.NoError(t, err)
	assert.True(t, target.Unmanaged)
	assert.False(t, target.Required)
	assert.False(t, target.HasRecord)
	require.Len(t, target.Components, 1)

	res, err := cfg.Uninstall(target)
	require.NoError(t, err)
	require.Len(t, res.Removed, 1)
	assert.NoFileExists(t, path)
	assert.NoFileExists(t, path+".sha256")
	assert.NoFileExists(t, path+".version")
}

func TestResolveUninstall_UnknownNotInstalled(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	_, err := cfg.ResolveUninstall("acme/does-not-exist")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPluginNotInstalled))
}

func TestResolveUninstall_EmptyInput(t *testing.T) {
	cfg := &Config{Cache: t.TempDir()}
	_, err := cfg.ResolveUninstall("   ")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrPluginNotInstalled))
}

func TestUninstall_RefusesUnderDevOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Dir: dir, Cache: t.TempDir()}

	writeFakeBinary(t, dir, "some-plugin", false)
	target := &UninstallTarget{
		Name:      "some-plugin",
		Unmanaged: true,
		Components: []UninstallComponent{{
			BinaryName: "some-plugin",
			Path:       filepath.Join(dir, pluginBinaryName("some-plugin")),
			Present:    true,
		}},
	}

	_, err := cfg.Uninstall(target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
	// Nothing was removed.
	assert.FileExists(t, filepath.Join(dir, pluginBinaryName("some-plugin")))
}

func TestRemoveWithRetry_IdempotentOnMissing(t *testing.T) {
	assert.NoError(t, removeWithRetry(filepath.Join(t.TempDir(), "nope")))
}
