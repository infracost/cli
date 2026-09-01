package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/infracost/cli/pkg/plugins/registry"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recFor builds a provenance record for a harness entry at the given version.
func recFor(h *stageHarness, version string) *StateRecord {
	comps := make([]StateComponent, 0, len(h.comps))
	for _, c := range h.comps {
		comps = append(comps, StateComponent{Type: c.typ, BinaryName: c.binaryName})
	}
	return &StateRecord{Name: h.entry.Name, Version: version, Components: comps}
}

// oldVersionQuery makes the stager report an installed binary at an older
// version so classify treats it as outdated.
func oldVersionQuery(h *stageHarness, oldVersion string) {
	h.stager.queryInfo = func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Name: h.entry.Name, Version: oldVersion}, nil
	}
}

func TestUpdateEntry_HappyUpdate(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-v2")}
	h := newStageHarness(t, "acme/tf", true, "2.0.0", parser)

	h.preinstall(parser, []byte("parser-v1-OLD"))
	oldVersionQuery(h, "1.0.0")

	eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "1.0.0"), nil, true)
	require.NoError(t, eu.Err)
	assert.Equal(t, UpdateStatusUpdated, eu.Status)
	assert.Equal(t, "1.0.0", eu.FromVersion)
	assert.Equal(t, "2.0.0", eu.ToVersion)

	got, err := os.ReadFile(h.binaryPath(parser))
	require.NoError(t, err)
	assert.Equal(t, parser.content, got)

	// Provenance now records the new version, unpinned.
	rec := loadState(h.cacheDir).find("acme/tf")
	require.NotNil(t, rec)
	assert.Equal(t, "2.0.0", rec.Version)
	assert.False(t, rec.Pinned)
}

func TestUpdateEntry_DualComponentAtomicity(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-k8s", content: []byte("parser-v2")}
	provider := testComponent{typ: registry.ComponentTypeProvider, binaryName: "acme-provider-k8s", content: []byte("provider-v2")}
	h := newStageHarness(t, "acme/k8s", true, "2.0.0", parser, provider)

	oldParser := []byte("parser-v1-OLD")
	oldProvider := []byte("provider-v1-OLD")
	parserPath := h.preinstall(parser, oldParser)
	providerPath := h.preinstall(provider, oldProvider)
	oldVersionQuery(h, "1.0.0")

	// The provider's staged handshake reports a mismatched version → the whole
	// entry update must fail with nothing committed.
	h.stager.probe = func(path string) probeResult {
		base := strings.TrimSuffix(filepath.Base(path), ".staged")
		want := provider.binaryName
		if runtime.GOOS == "windows" {
			want += ".exe"
		}
		if base == want {
			return probeResult{info: &pb.GetPluginInfoResponse{Name: h.entry.Name, Type: pb.PluginType_PROVIDER, Version: "9.9.9"}}
		}
		return probeResult{info: &pb.GetPluginInfoResponse{Name: h.entry.Name, Type: pb.PluginType_PARSER, Version: "2.0.0"}}
	}

	eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "1.0.0"), nil, true)
	assert.Equal(t, UpdateStatusFailed, eu.Status)
	require.Error(t, eu.Err)
	assert.Contains(t, eu.Err.Error(), "refusing to install")

	// Both prior binaries are untouched — the failed provider handshake aborts
	// the commit for the whole entry.
	gotParser, err := os.ReadFile(parserPath)
	require.NoError(t, err)
	assert.Equal(t, oldParser, gotParser)
	gotProvider, err := os.ReadFile(providerPath)
	require.NoError(t, err)
	assert.Equal(t, oldProvider, gotProvider)
}

func TestUpdateEntry_DevComponentSkipsEntry(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("data")}
	h := newStageHarness(t, "acme/tf", true, "2.0.0", parser)

	devContent := []byte("locally-built")
	devPath := h.preinstall(parser, devContent)
	h.stager.queryInfo = func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Name: h.entry.Name, Version: devPluginVersion}, nil
	}

	eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "dev"), nil, true)
	assert.Equal(t, UpdateStatusSkippedDev, eu.Status)

	got, err := os.ReadFile(devPath)
	require.NoError(t, err)
	assert.Equal(t, devContent, got)
}

func TestUpdateEntry_MissingComponentSkippedInUpdateAllReinstalledExplicitly(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-v2")}

	t.Run("update-all skips a missing recorded component", func(t *testing.T) {
		h := newStageHarness(t, "acme/tf", true, "2.0.0", parser)
		// Binary intentionally absent on disk.
		eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "1.0.0"), nil, true)
		assert.Equal(t, UpdateStatusSkippedMissing, eu.Status)
		assert.Equal(t, "acme-parser-tf", eu.Detail)
		_, statErr := os.Stat(h.binaryPath(parser))
		assert.True(t, os.IsNotExist(statErr), "update-all must not install the missing component")
	})

	t.Run("explicit update reinstalls the missing component", func(t *testing.T) {
		h := newStageHarness(t, "acme/tf", true, "2.0.0", parser)
		eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "1.0.0"), nil, false)
		require.NoError(t, eu.Err)
		assert.Equal(t, UpdateStatusUpdated, eu.Status)
		got, err := os.ReadFile(h.binaryPath(parser))
		require.NoError(t, err)
		assert.Equal(t, parser.content, got)
	})
}

func TestUpdateEntry_ExplicitUpdateClearsPinOnNoOp(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("current")}
	h := newStageHarness(t, "acme/tf", true, "1.0.0", parser)

	// Already at the resolved version → the update is a no-op.
	h.preinstall(parser, []byte("current"))

	// Seed a pinned provenance record on disk so clearPin can find it.
	st := loadState(h.cacheDir)
	st.upsert(StateRecord{
		Name:       "acme/tf",
		Version:    "1.0.0",
		Pinned:     true,
		Components: []StateComponent{{Type: pluginTypeParser, BinaryName: "acme-parser-tf"}},
	})
	require.NoError(t, st.save(h.cacheDir))
	rec := loadState(h.cacheDir).find("acme/tf")
	require.NotNil(t, rec)
	require.True(t, rec.Pinned)

	eu := h.stager.updateEntry(context.Background(), h.entry, rec, nil, false)
	require.NoError(t, eu.Err)
	assert.Equal(t, UpdateStatusUpToDate, eu.Status)

	// The pin is cleared even though nothing was downloaded.
	after := loadState(h.cacheDir).find("acme/tf")
	require.NotNil(t, after)
	assert.False(t, after.Pinned, "explicit update clears a pin even on a no-op")
}

func TestUpdateEntry_UpdateAllRespectsPin(t *testing.T) {
	// updateRecordedEntries, not updateEntry, enforces the pin skip for update-all
	// — assert it here at the Config level.
	dir := t.TempDir()
	c := &Config{Cache: dir}
	st := c.LoadState()
	st.upsert(StateRecord{
		Name:       "acme/tf",
		Version:    "1.0.0",
		Pinned:     true,
		Components: []StateComponent{{Type: pluginTypeParser, BinaryName: "acme-parser-tf"}},
	})
	require.NoError(t, c.SaveState(st))

	reg := &registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{{
			Name:       "acme/tf",
			Components: []registry.Component{{Type: registry.ComponentTypeParser, BinaryName: "acme-parser-tf"}},
		}},
	}

	got := c.updateRecordedEntries(context.Background(), reg, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, UpdateStatusSkippedPinned, got[0].Status)
	assert.Equal(t, "1.0.0", got[0].FromVersion)
}

func TestUpdateEntry_UnofficialDeclineAndFailure(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-v2")}

	t.Run("clean decline skips the entry", func(t *testing.T) {
		h := newStageHarness(t, "acme/tf", false, "2.0.0", parser)
		h.preinstall(parser, []byte("parser-v1"))
		oldVersionQuery(h, "1.0.0")

		decline := func(*registry.Entry) (bool, error) { return false, nil }
		eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "1.0.0"), decline, true)
		require.NoError(t, eu.Err)
		assert.Equal(t, UpdateStatusSkippedUnofficial, eu.Status)

		// Nothing was overwritten.
		got, err := os.ReadFile(h.binaryPath(parser))
		require.NoError(t, err)
		assert.Equal(t, []byte("parser-v1"), got)
	})

	t.Run("trust error fails the entry", func(t *testing.T) {
		h := newStageHarness(t, "acme/tf", false, "2.0.0", parser)
		h.preinstall(parser, []byte("parser-v1"))
		oldVersionQuery(h, "1.0.0")

		boom := func(*registry.Entry) (bool, error) { return false, assert.AnError }
		eu := h.stager.updateEntry(context.Background(), h.entry, recFor(h, "1.0.0"), boom, true)
		assert.Equal(t, UpdateStatusFailed, eu.Status)
		assert.ErrorIs(t, eu.Err, assert.AnError)
	})
}

func TestUpdateRecordedEntries_RemovedFromRegistryIsSkippedNeverUninstalled(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Cache: dir}
	// Record present, but the binary stays on disk (never uninstalled).
	binPath := filepath.Join(dir, "acme-gone")
	require.NoError(t, os.WriteFile(binPath, []byte("still-here"), 0o750)) //nolint:gosec
	st := c.LoadState()
	st.upsert(StateRecord{Name: "acme/gone", Version: "1.0.0", Components: []StateComponent{{Type: pluginTypeParser, BinaryName: "acme-gone"}}})
	require.NoError(t, c.SaveState(st))

	reg := &registry.Registry{SchemaVersion: registry.SupportedSchemaVersion} // empty — entry not present

	got := c.updateRecordedEntries(context.Background(), reg, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, UpdateStatusSkippedRemoved, got[0].Status)

	// The binary and its record are untouched.
	assert.FileExists(t, binPath)
	assert.NotNil(t, c.LoadState().find("acme/gone"))
}

func TestUpdateRecordedEntries_RegistryUnavailableFailsEachEntry(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Cache: dir}
	st := c.LoadState()
	st.upsert(StateRecord{Name: "acme/tf", Version: "1.0.0", Components: []StateComponent{{Type: pluginTypeParser, BinaryName: "acme-parser-tf"}}})
	require.NoError(t, c.SaveState(st))

	got := c.updateRecordedEntries(context.Background(), nil, assert.AnError, nil)
	require.Len(t, got, 1)
	assert.Equal(t, UpdateStatusFailed, got[0].Status)
	assert.ErrorIs(t, got[0].Err, assert.AnError)
}

func TestHasRegistryInstalls(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Cache: dir}
	assert.False(t, c.HasRegistryInstalls(), "an empty state has no registry installs")

	st := c.LoadState()
	st.upsert(StateRecord{Name: "acme/tf", Version: "1.0.0"})
	require.NoError(t, c.SaveState(st))
	assert.True(t, c.HasRegistryInstalls())
}

func TestDiscoverUnmanaged(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Cache: dir}

	// A required binary (excluded), a recorded registry component (excluded), a
	// sidecar (excluded), and a genuine hand-copied binary (reported).
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName("infracost-parser-terraform")), []byte("x"), 0o750)) //nolint:gosec
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName("acme-parser-tf")), []byte("x"), 0o750))              //nolint:gosec
	require.NoError(t, os.WriteFile(filepath.Join(dir, pluginBinaryName("some-random-plugin")), []byte("x"), 0o750))          //nolint:gosec
	require.NoError(t, os.WriteFile(filepath.Join(dir, "acme-parser-tf.sha256"), []byte("deadbeef"), 0o600))

	st := c.LoadState()
	st.upsert(StateRecord{Name: "acme/tf", Version: "1.0.0", Components: []StateComponent{{Type: pluginTypeParser, BinaryName: "acme-parser-tf"}}})
	require.NoError(t, c.SaveState(st))

	got := c.discoverUnmanaged()
	assert.Equal(t, []string{"some-random-plugin"}, got)
}

func TestUpdateRequiredSet_CollectsFailuresWithoutAborting(t *testing.T) {
	// A bogus base URL makes every required-plugin download fail. The run must
	// attempt them all and collect each failure rather than aborting on the first.
	c := &Config{Cache: t.TempDir(), BaseURL: "http://127.0.0.1:1", AutoUpdate: true}
	mgr := NewManager(ManagerOptions{Dir: c.PluginDir(), Cache: c.Cache, BaseURL: c.BaseURL, AutoUpdate: true})
	defer mgr.Close()

	failures := c.updateRequiredSet(context.Background(), mgr)
	assert.Len(t, failures, len(requiredPlugins))
	for _, f := range failures {
		assert.Equal(t, UpdateStatusFailed, f.Status)
		require.Error(t, f.Err)
	}
}

func TestUpdatePlugins_RefusesUnderDevOverride(t *testing.T) {
	c := &Config{Dir: t.TempDir(), BaseURL: "http://127.0.0.1:1"}
	err := c.UpdatePlugins(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
}

func TestUpdateAll_RefusesUnderDevOverride(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Cache: t.TempDir()}
	_, err := c.UpdateAll(context.Background(), nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
}

// TestUpdateAll_RequiredRegistryAndUnmanaged drives the full update-all
// aggregation: the built-in required set (served over HTTP), one recorded
// registry entry (via the injected stager), and a hand-copied binary that must
// be reported as unmanaged.
func TestUpdateAll_RequiredRegistryAndUnmanaged(t *testing.T) {
	// Registry entry served by its own harness.
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("acme-v2")}
	h := newStageHarness(t, "acme/tf", true, "2.0.0", parser)
	h.preinstall(parser, []byte("acme-v1"))
	oldVersionQuery(h, "1.0.0")

	// Seed the registry entry's provenance in the same cache dir the config uses.
	st := loadState(h.cacheDir)
	st.upsert(StateRecord{Name: "acme/tf", Version: "1.0.0", Components: []StateComponent{{Type: pluginTypeParser, BinaryName: "acme-parser-tf"}}})
	require.NoError(t, st.save(h.cacheDir))

	// A hand-copied binary with no provenance.
	require.NoError(t, os.WriteFile(filepath.Join(h.cacheDir, pluginBinaryName("some-random-plugin")), []byte("x"), 0o750)) //nolint:gosec

	// Serve every required plugin archive so the required set succeeds.
	archiveName := pluginArchiveName()
	archiveData := map[string][]byte{}
	archiveSHA := map[string]string{}
	archiveDir := t.TempDir()
	for _, r := range requiredPlugins {
		p := createPluginArchive(t, archiveDir, archiveName+"-"+r.Name, pluginBinaryName(r.Name), []byte(r.Name+"-v2"))
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		archiveData[r.Name] = data
		archiveSHA[r.Name] = fileSHA256(t, p)
	}
	reqSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 5 || parts[1] != runtime.GOOS || parts[2] != runtime.GOARCH || parts[3] != "latest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		name := parts[0]
		if _, ok := archiveData[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch parts[4] {
		case "version":
			_, _ = w.Write([]byte("0.0.2\n"))
		case archiveName + ".sha256":
			_, _ = w.Write([]byte(archiveSHA[name] + "\n"))
		case archiveName:
			_, _ = w.Write(archiveData[name])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer reqSrv.Close()

	c := &Config{
		Cache:      h.cacheDir,
		BaseURL:    reqSrv.URL,
		AutoUpdate: true,
		newStager:  func() *registryStager { return h.stager },
	}

	reg := &registry.Registry{SchemaVersion: registry.SupportedSchemaVersion, Plugins: []registry.Entry{*h.entry}}

	res, err := c.UpdateAll(context.Background(), reg, nil, nil)
	require.NoError(t, err)
	assert.False(t, res.Failed(), "no entry should have failed")

	// The registry entry updated.
	var acme *EntryUpdate
	for i := range res.Entries {
		if res.Entries[i].Name == "acme/tf" {
			acme = &res.Entries[i]
		}
	}
	require.NotNil(t, acme)
	assert.Equal(t, UpdateStatusUpdated, acme.Status)
	assert.Equal(t, "2.0.0", acme.ToVersion)

	// The hand-copied binary is reported unmanaged.
	assert.Contains(t, res.Unmanaged, "some-random-plugin")

	// Required plugins were downloaded to the cache.
	got, err := os.ReadFile(filepath.Join(h.cacheDir, pluginBinaryName("infracost-parser-terraform")))
	require.NoError(t, err)
	assert.Equal(t, []byte("infracost-parser-terraform-v2"), got)
}
