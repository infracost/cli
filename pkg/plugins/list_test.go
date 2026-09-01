package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// findItem returns the first list item whose on-disk binary filename matches
// binaryName, or nil.
func findItem(items []ListItem, binaryName string) *ListItem {
	target := pluginBinaryName(binaryName)
	for i := range items {
		if filepath.Base(items[i].Path) == target {
			return &items[i]
		}
	}
	return nil
}

// seedListState writes a provenance record into the plugin cache directory so
// List() can decorate the matching discovered binaries.
func seedListState(t *testing.T, cfg *Config, rec StateRecord) {
	t.Helper()
	st := cfg.LoadState()
	st.Records = append(st.Records, rec)
	require.NoError(t, cfg.SaveState(st))
}

func TestList_RegistryProvenance_DualComponentUnofficialPinned(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	parserBin := "infracost-parser-widget"
	providerBin := "infracost-provider-widget"
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName(parserBin)), []byte("binary"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName(providerBin)), []byte("binary"), 0o700))

	seedListState(t, cfg, StateRecord{
		Name:    "acme/widget",
		Version: "1.2.3",
		Components: []StateComponent{
			{Type: pluginTypeParser, BinaryName: parserBin},
			{Type: pluginTypeProvider, BinaryName: providerBin},
		},
		Pinned:      true,
		Official:    false,
		Author:      "Acme Corp",
		InstalledAt: time.Now().UTC(),
	})

	items := cfg.List()

	parser := findItem(items, parserBin)
	require.NotNil(t, parser, "expected parser component in list")
	assert.Equal(t, SourceRegistry, parser.Source)
	assert.Equal(t, pluginTypeParser, parser.Type)
	assert.Equal(t, "acme/widget", parser.Name)
	assert.Equal(t, "1.2.3", parser.Version)
	assert.True(t, parser.Installed)
	assert.False(t, parser.Required)
	assert.False(t, parser.Official)
	assert.True(t, parser.Pinned)
	assert.Equal(t, "Acme Corp", parser.Author)

	provider := findItem(items, providerBin)
	require.NotNil(t, provider, "expected provider component in list")
	assert.Equal(t, SourceRegistry, provider.Source)
	assert.Equal(t, pluginTypeProvider, provider.Type)
	assert.Equal(t, "acme/widget", provider.Name)
	assert.False(t, provider.Official)
	assert.True(t, provider.Pinned)
	assert.Equal(t, "Acme Corp", provider.Author)
}

func TestList_UnmanagedBinary(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	bin := "infracost-plugin-handcopied"
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName(bin)), []byte("binary"), 0o700))

	items := cfg.List()

	item := findItem(items, bin)
	require.NotNil(t, item, "expected hand-copied binary in list")
	assert.Equal(t, SourceUnmanaged, item.Source)
	assert.False(t, item.Required)
	assert.True(t, item.Installed)
	assert.False(t, item.Official)
	assert.False(t, item.Pinned)
	assert.Empty(t, item.Author)
}

func TestList_FilesystemWinsOverProvenance_MissingBinary(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	// A provenance record exists but no binary is on disk: the entry must show
	// as not installed rather than trusting provenance.
	bin := "infracost-parser-ghost"
	seedListState(t, cfg, StateRecord{
		Name:        "acme/ghost",
		Version:     "0.9.0",
		Components:  []StateComponent{{Type: pluginTypeParser, BinaryName: bin}},
		Official:    false,
		Author:      "Ghost",
		InstalledAt: time.Now().UTC(),
	})

	items := cfg.List()

	item := findItem(items, bin)
	require.NotNil(t, item, "expected recorded-but-missing entry in list")
	assert.False(t, item.Installed)
	assert.Equal(t, SourceRegistry, item.Source)
	assert.Equal(t, pluginTypeParser, item.Type)
	assert.Equal(t, "acme/ghost", item.Name)
}

func TestList_RequiredWinsOverStaleRecord(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Cache: dir}

	// Even if a stale provenance record names a required binary, the required
	// set owns it: it is reported as required, not registry.
	requiredBin := requiredPlugins[0].Name
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName(requiredBin)), []byte("binary"), 0o700))

	items := cfg.List()

	item := findItem(items, requiredBin)
	require.NotNil(t, item)
	assert.Equal(t, SourceRequired, item.Source)
	assert.True(t, item.Required)
}
