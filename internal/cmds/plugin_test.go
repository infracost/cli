package cmds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testBinaryName mirrors the production filename rule (adds .exe on Windows).
func testBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func newListTestConfig(dir string) *config.Config {
	return &config.Config{Plugins: plugins.Config{Cache: dir}}
}

func seedListRecord(t *testing.T, cfg *config.Config, rec plugins.StateRecord) {
	t.Helper()
	st := cfg.Plugins.LoadState()
	st.Records = append(st.Records, rec)
	require.NoError(t, cfg.Plugins.SaveState(st))
}

func TestPluginList_StockOutputUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := newListTestConfig(dir)

	out := captureStdout(t, func() { printPluginList(cfg) })

	assert.Contains(t, out, "Parsers:")
	assert.Contains(t, out, "Providers:")
	// No registry/unmanaged installs => no extra groups or markers.
	assert.NotContains(t, out, "Unknown:")
	assert.NotContains(t, out, "unmanaged")
	assert.NotContains(t, out, "unofficial")
	assert.NotContains(t, out, "(pinned)")
}

func TestPluginList_UnofficialAuthorAndPinnedMarkers(t *testing.T) {
	dir := t.TempDir()
	cfg := newListTestConfig(dir)

	bin := "infracost-parser-widget"
	require.NoError(t, os.WriteFile(filepath.Join(dir, testBinaryName(bin)), []byte("binary"), 0o700))
	seedListRecord(t, cfg, plugins.StateRecord{
		Name:        "acme/widget",
		Version:     "1.2.3",
		Components:  []plugins.StateComponent{{Type: "parser", BinaryName: bin}},
		Pinned:      true,
		Official:    false,
		Author:      "Acme Corp",
		InstalledAt: time.Now().UTC(),
	})

	out := captureStdout(t, func() { printPluginList(cfg) })

	assert.Contains(t, out, "acme/widget")
	assert.Contains(t, out, "by Acme Corp")
	assert.Contains(t, out, "unofficial")
	assert.Contains(t, out, "1.2.3 (pinned)")
}

func TestPluginList_UnmanagedMarker(t *testing.T) {
	dir := t.TempDir()
	cfg := newListTestConfig(dir)

	bin := "infracost-plugin-handcopied"
	require.NoError(t, os.WriteFile(filepath.Join(dir, testBinaryName(bin)), []byte("binary"), 0o700))

	out := captureStdout(t, func() { printPluginList(cfg) })

	// Non-queryable hand-copied binary has an unknown type, so it lands in the
	// Unknown group and is marked unmanaged.
	assert.Contains(t, out, "Unknown:")
	assert.Contains(t, out, "unmanaged")
}

func TestPluginList_JSONCoversProvenanceAndRequired(t *testing.T) {
	dir := t.TempDir()
	cfg := newListTestConfig(dir)

	bin := "infracost-provider-widget"
	require.NoError(t, os.WriteFile(filepath.Join(dir, testBinaryName(bin)), []byte("binary"), 0o700))
	seedListRecord(t, cfg, plugins.StateRecord{
		Name:        "acme/widget",
		Version:     "2.0.0",
		Components:  []plugins.StateComponent{{Type: "provider", BinaryName: bin}},
		Pinned:      false,
		Official:    false,
		Author:      "Acme Corp",
		InstalledAt: time.Now().UTC(),
	})

	out := captureStdout(t, func() { require.NoError(t, printPluginListJSON(cfg)) })

	var items []plugins.ListItem
	require.NoError(t, json.Unmarshal([]byte(out), &items))
	require.NotEmpty(t, items)

	// Every required plugin appears (installed or not).
	var requiredCount int
	var widget *plugins.ListItem
	for i := range items {
		if items[i].Required {
			requiredCount++
		}
		if items[i].Name == "acme/widget" {
			widget = &items[i]
		}
	}
	assert.Positive(t, requiredCount, "expected required entries in JSON output")

	require.NotNil(t, widget, "expected registry entry in JSON output")
	assert.Equal(t, plugins.SourceRegistry, widget.Source)
	assert.Equal(t, "Acme Corp", widget.Author)
	assert.False(t, widget.Official)
	assert.Equal(t, "2.0.0", widget.Version)
}
