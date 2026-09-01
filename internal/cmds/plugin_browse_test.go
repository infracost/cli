package cmds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubBrowseRegistry overrides the registry-load seam used by search/info so
// listing and resolution paths can be exercised without a network fetch.
func stubBrowseRegistry(t *testing.T, reg *registry.Registry, err error) {
	t.Helper()
	orig := browseRegistryLoad
	browseRegistryLoad = func(context.Context) (*registry.Registry, error) { return reg, err }
	t.Cleanup(func() { browseRegistryLoad = orig })
}

// currentPlatform is the goos/goarch pair the running test binary is on, used
// so fixture components support "this" platform without hard-coding one.
func currentPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// browseFixtureRegistry returns a two-entry registry: one official single
// component plugin and one unofficial dual-component plugin.
func browseFixtureRegistry() *registry.Registry {
	return &registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{
			{
				Name:        "infracost/terraform",
				DisplayName: "Terraform",
				Description: "Parse Terraform HCL to price infrastructure.",
				Official:    true,
				Homepage:    "https://infracost.io",
				License:     "Apache-2.0",
				Components: []registry.Component{{
					Type:       registry.ComponentTypeParser,
					BinaryName: "infracost-parser-terraform",
					Platforms:  []string{currentPlatform(), "linux/amd64"},
					Download:   "https://example.com/{version}/data.tar.gz",
					Checksums:  "https://example.com/{version}/data.tar.gz.sha256",
				}},
			},
			{
				Name:        "acme/kubewidget",
				DisplayName: "Kube Widget",
				Description: "A community kubernetes cost widget with a very long description that keeps going well past any reasonable terminal width so it exercises truncation.",
				Author:      "Acme Corp",
				Official:    false,
				Components: []registry.Component{
					{
						Type:       registry.ComponentTypeParser,
						BinaryName: "acme-parser-kubewidget",
						Platforms:  []string{currentPlatform()},
						Download:   "https://example.com/p/{version}/data.tar.gz",
						Checksums:  "https://example.com/p/{version}/data.tar.gz.sha256",
					},
					{
						Type:       registry.ComponentTypeProvider,
						BinaryName: "acme-provider-kubewidget",
						Platforms:  []string{currentPlatform()},
						Download:   "https://example.com/pr/{version}/data.tar.gz",
						Checksums:  "https://example.com/pr/{version}/data.tar.gz.sha256",
					},
				},
			},
		},
	}
}

func TestPluginSearch_FullListing(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())
	stubBrowseRegistry(t, browseFixtureRegistry(), nil)

	out := captureStdout(t, func() {
		require.NoError(t, pluginsSearchCmd(cfg).RunE(newTestCmd(), nil))
	})

	// Both entries appear once, with capabilities and description.
	assert.Contains(t, out, "infracost/terraform")
	assert.Contains(t, out, "acme/kubewidget")
	assert.Contains(t, out, "parser + provider")
	assert.Contains(t, out, "Parse Terraform HCL")
	// Official vs author rendering.
	assert.Contains(t, out, "official")
	assert.Contains(t, out, "by Acme Corp")
	// Long description is truncated with an ellipsis.
	assert.Contains(t, out, "…")
}

func TestPluginSearch_FiltersByQuery(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())
	stubBrowseRegistry(t, browseFixtureRegistry(), nil)

	out := captureStdout(t, func() {
		require.NoError(t, pluginsSearchCmd(cfg).RunE(newTestCmd(), []string{"kube"}))
	})

	assert.Contains(t, out, "acme/kubewidget")
	assert.NotContains(t, out, "infracost/terraform")
}

func TestPluginSearch_NoMatchExitsZero(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())
	stubBrowseRegistry(t, browseFixtureRegistry(), nil)

	out := captureStdout(t, func() {
		require.NoError(t, pluginsSearchCmd(cfg).RunE(newTestCmd(), []string{"nomatchxyz"}))
	})

	assert.Contains(t, out, "No plugins matched")
}

func TestPluginSearch_InstalledAnnotation(t *testing.T) {
	dir := t.TempDir()
	cfg := newListTestConfig(dir)

	// Fully install the dual-component (non-required) entry via provenance +
	// both binaries so the entry is annotated with its shared version. A
	// required name would collide with the compiled-in set (required wins in
	// List()), so a registry-only name is used here.
	parserBin := "acme-parser-kubewidget"
	providerBin := "acme-provider-kubewidget"
	require.NoError(t, os.WriteFile(filepath.Join(dir, testBinaryName(parserBin)), []byte("binary"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, testBinaryName(providerBin)), []byte("binary"), 0o700))
	seedListRecord(t, cfg, plugins.StateRecord{
		Name:    "acme/kubewidget",
		Version: "1.4.2",
		Components: []plugins.StateComponent{
			{Type: "parser", BinaryName: parserBin},
			{Type: "provider", BinaryName: providerBin},
		},
		Author:      "Acme Corp",
		InstalledAt: time.Now().UTC(),
	})

	stubBrowseRegistry(t, browseFixtureRegistry(), nil)

	out := captureStdout(t, func() {
		require.NoError(t, pluginsSearchCmd(cfg).RunE(newTestCmd(), []string{"kube"}))
	})

	assert.Contains(t, out, "installed 1.4.2")
}

func TestPluginSearch_PlatformUnavailable(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())
	reg := &registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{{
			Name:        "acme/other-os",
			Description: "Only ships for a platform we are not on.",
			Author:      "Acme Corp",
			Components: []registry.Component{{
				Type:       registry.ComponentTypeParser,
				BinaryName: "acme-parser-otheros",
				Platforms:  []string{"plan9/386"},
				Download:   "https://example.com/{version}/data.tar.gz",
				Checksums:  "https://example.com/{version}/data.tar.gz.sha256",
			}},
		}},
	}
	stubBrowseRegistry(t, reg, nil)

	out := captureStdout(t, func() {
		require.NoError(t, pluginsSearchCmd(cfg).RunE(newTestCmd(), nil))
	})

	assert.Contains(t, out, "acme/other-os")
	assert.Contains(t, out, "not available for "+currentPlatform())
}

func TestPluginSearch_NoCacheUnreachable(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())

	orig := browseRegistryLoad
	browseRegistryLoad = func(ctx context.Context) (*registry.Registry, error) {
		// A real client against a dead host with an absent cache returns the
		// hard error naming the registry URL.
		client := &registry.Client{
			URL:       "http://127.0.0.1:1/registry.json",
			CachePath: filepath.Join(t.TempDir(), "missing.json"),
		}
		return client.Load(ctx)
	}
	t.Cleanup(func() { browseRegistryLoad = orig })

	err := pluginsSearchCmd(cfg).RunE(newTestCmd(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "127.0.0.1")
}

func TestPluginSearchJSON_Shape(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())
	reg := browseFixtureRegistry()
	entries := filterRegistryEntries(reg, "")
	byBinary := indexListByBinary(cfg.Plugins.List())

	out := captureStdout(t, func() {
		require.NoError(t, printSearchJSON(entries, byBinary))
	})

	var got []browseEntryJSON
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.Len(t, got, 2)

	byName := map[string]browseEntryJSON{}
	for _, e := range got {
		byName[e.Name] = e
	}

	tf := byName["infracost/terraform"]
	assert.True(t, tf.Official)
	assert.Equal(t, "parser", tf.Capabilities)
	assert.Len(t, tf.Components, 1)
	// Search JSON omits the (network-resolved) latest version.
	assert.Empty(t, tf.LatestVersion)

	kw := byName["acme/kubewidget"]
	assert.False(t, kw.Official)
	assert.Equal(t, "Acme Corp", kw.Author)
	assert.Equal(t, "parser + provider", kw.Capabilities)
	assert.Len(t, kw.Components, 2)
}

func TestPluginInfo_ShowsMetadataAndLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1.5.0\n"))
	}))
	defer srv.Close()

	cfg := newListTestConfig(t.TempDir())
	reg := browseFixtureRegistry()
	reg.Plugins[1].VersionURL = srv.URL // acme/kubewidget
	stubBrowseRegistry(t, reg, nil)

	out := captureStdout(t, func() {
		require.NoError(t, pluginsInfoCmd(cfg).RunE(newTestCmd(), []string{"acme/kubewidget"}))
	})

	assert.Contains(t, out, "acme/kubewidget")
	assert.Contains(t, out, "Acme Corp")
	assert.Contains(t, out, "1.5.0")
	assert.Contains(t, out, "parser + provider")
	assert.Contains(t, out, "acme-parser-kubewidget")
	assert.Contains(t, out, "acme-provider-kubewidget")
	// Nothing installed in a fresh dir.
	assert.Contains(t, out, "not installed")
}

func TestPluginInfo_UnknownNameSuggestion(t *testing.T) {
	cfg := newListTestConfig(t.TempDir())
	stubBrowseRegistry(t, browseFixtureRegistry(), nil)

	err := pluginsInfoCmd(cfg).RunE(newTestCmd(), []string{"infracost/terraformm"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in registry")
	assert.Contains(t, err.Error(), "infracost/terraform")
}

func TestPluginInfoJSON_Shape(t *testing.T) {
	dir := t.TempDir()
	cfg := newListTestConfig(dir)

	// Install the parser component only → partial install of the dual entry.
	bin := "acme-parser-kubewidget"
	require.NoError(t, os.WriteFile(filepath.Join(dir, testBinaryName(bin)), []byte("binary"), 0o700))
	seedListRecord(t, cfg, plugins.StateRecord{
		Name:        "acme/kubewidget",
		Version:     "0.9.0",
		Components:  []plugins.StateComponent{{Type: "parser", BinaryName: bin}},
		Author:      "Acme Corp",
		InstalledAt: time.Now().UTC(),
	})

	reg := browseFixtureRegistry()
	entry := reg.ByName("acme/kubewidget")
	require.NotNil(t, entry)
	byBinary := indexListByBinary(cfg.Plugins.List())

	out := captureStdout(t, func() {
		require.NoError(t, printInfoJSON(entry, byBinary, "1.0.0"))
	})

	var got browseEntryJSON
	require.NoError(t, json.Unmarshal([]byte(out), &got))

	assert.Equal(t, "acme/kubewidget", got.Name)
	assert.False(t, got.Official)
	assert.Equal(t, "1.0.0", got.LatestVersion)
	assert.True(t, got.Installable)
	// Only one of two components present → entry is not fully installed.
	assert.False(t, got.Installed)
	require.Len(t, got.Components, 2)

	var parser, provider browseComponentJSON
	for _, c := range got.Components {
		switch c.Type {
		case "parser":
			parser = c
		case "provider":
			provider = c
		}
	}
	assert.True(t, parser.Installed)
	assert.Equal(t, "0.9.0", parser.Version)
	assert.False(t, provider.Installed)
}
