package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleRecord(name, version string) StateRecord {
	return StateRecord{
		Name:    name,
		Version: version,
		Components: []StateComponent{
			{Type: pluginTypeParser, BinaryName: "infracost-parser-" + filepath.Base(name)},
		},
		Official:    false,
		Author:      "someone",
		InstalledAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// readRawState decodes the on-disk state file and fails the test if it isn't
// valid JSON — the core invariant every mutation must preserve.
func readRawState(t *testing.T, dir string) State {
	t.Helper()
	data, err := os.ReadFile(stateFilePath(dir))
	require.NoError(t, err, "state file should exist and be readable")
	var s State
	require.NoError(t, json.Unmarshal(data, &s), "state file should be valid JSON")
	return s
}

func TestStateMissingFileYieldsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := loadState(dir)
	require.NotNil(t, s)
	assert.Equal(t, stateSchemaVersion, s.SchemaVersion)
	assert.Empty(t, s.Records)
	// No file is created just by reading.
	_, err := os.Stat(stateFilePath(dir))
	assert.True(t, os.IsNotExist(err), "loadState must not create a file")
}

// TestStateInstallUninstallUpdateRoundTrip covers the install (upsert),
// update (rewrite version), and uninstall (remove) mutations, asserting valid
// JSON on disk after each.
func TestStateInstallUninstallUpdateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Install: one record covering all components.
	s := loadState(dir)
	s.upsert(sampleRecord("acme/one", "1.0.0"))
	s.upsert(sampleRecord("acme/two", "2.0.0"))
	require.NoError(t, s.save(dir))

	raw := readRawState(t, dir)
	assert.Equal(t, stateSchemaVersion, raw.SchemaVersion)
	require.Len(t, raw.Records, 2)

	// Update: rewrite one entry's version.
	s = loadState(dir)
	rec := s.find("acme/one")
	require.NotNil(t, rec)
	rec.Version = "1.1.0"
	require.NoError(t, s.save(dir))

	s = loadState(dir)
	assert.Equal(t, "1.1.0", s.find("acme/one").Version)
	assert.Equal(t, "2.0.0", s.find("acme/two").Version)

	// Uninstall: remove a record.
	assert.True(t, s.remove("acme/one"))
	require.NoError(t, s.save(dir))

	s = loadState(dir)
	assert.Nil(t, s.find("acme/one"))
	require.Len(t, s.Records, 1)
	assert.Equal(t, "acme/two", s.Records[0].Name)
}

func TestStateUpsertReplacesInPlace(t *testing.T) {
	s := emptyState()
	s.upsert(sampleRecord("acme/one", "1.0.0"))
	s.upsert(sampleRecord("acme/one", "9.9.9"))
	require.Len(t, s.Records, 1)
	assert.Equal(t, "9.9.9", s.Records[0].Version)
}

func TestStateRemoveMissingIsNoop(t *testing.T) {
	s := emptyState()
	s.upsert(sampleRecord("acme/one", "1.0.0"))
	assert.False(t, s.remove("acme/absent"))
	assert.Len(t, s.Records, 1)
}

func TestStatePreservesProvenanceFields(t *testing.T) {
	dir := t.TempDir()
	s := emptyState()
	s.upsert(StateRecord{
		Name:    "acme/dual",
		Version: "3.2.1",
		Components: []StateComponent{
			{Type: pluginTypeParser, BinaryName: "infracost-parser-dual"},
			{Type: pluginTypeProvider, BinaryName: "infracost-provider-dual"},
		},
		Pinned:      true,
		Official:    true,
		Author:      "acme inc",
		InstalledAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	})
	require.NoError(t, s.save(dir))

	got := loadState(dir).find("acme/dual")
	require.NotNil(t, got)
	assert.Equal(t, "3.2.1", got.Version)
	assert.True(t, got.Pinned)
	assert.True(t, got.Official)
	assert.Equal(t, "acme inc", got.Author)
	assert.Len(t, got.Components, 2)
	assert.True(t, got.InstalledAt.Equal(time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)))
}

// TestStateCorruptFileIsSetAside verifies a corrupt file does not crash the
// load, is moved aside, and yields an empty state.
func TestStateCorruptFileIsSetAside(t *testing.T) {
	warnCorruptStateOnce = sync.Once{}
	dir := t.TempDir()
	path := stateFilePath(dir)
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))

	s := loadState(dir)
	require.NotNil(t, s)
	assert.Empty(t, s.Records)

	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "corrupt file should be moved aside")
	assert.FileExists(t, path+".corrupt", "corrupt file should be preserved for debugging")

	// A subsequent save starts clean and produces valid JSON.
	s.upsert(sampleRecord("acme/one", "1.0.0"))
	require.NoError(t, s.save(dir))
	raw := readRawState(t, dir)
	require.Len(t, raw.Records, 1)
}

// TestStateHandDeletedFileDegradesGracefully covers deleting the state file
// out from under a loaded, mutated state: the next save just recreates it.
func TestStateHandDeletedFileDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	s := loadState(dir)
	s.upsert(sampleRecord("acme/one", "1.0.0"))
	require.NoError(t, s.save(dir))

	require.NoError(t, os.Remove(stateFilePath(dir)))

	// Loading the now-missing file degrades to empty, no error.
	s = loadState(dir)
	assert.Empty(t, s.Records)
}

// TestStateDropsRecordsForRequiredNames covers the edge case where a record
// exists for a plugin that later joined the compiled-in required set: it is
// dropped on the next write.
func TestStateDropsRecordsForRequiredNames(t *testing.T) {
	dir := t.TempDir()
	// Pick a real required plugin display name.
	required := requiredPlugins[0].DisplayName
	require.True(t, isRequiredName(required))

	s := emptyState()
	s.upsert(sampleRecord(required, "1.0.0"))
	s.upsert(sampleRecord("acme/community", "2.0.0"))
	require.NoError(t, s.save(dir))

	s = loadState(dir)
	assert.Nil(t, s.find(required), "record for a required name must be dropped on write")
	assert.NotNil(t, s.find("acme/community"))
}

// TestIsPluginSidecarIgnoresStateFiles documents that discovery skips the
// state file and its siblings so they're never exec'd or listed as plugins.
func TestIsPluginSidecarIgnoresStateFiles(t *testing.T) {
	for _, name := range []string{
		stateFileName,
		stateFileName + ".corrupt",
		".state-123456.tmp",
	} {
		assert.True(t, isPluginSidecar(name), "%q should be treated as a non-plugin sidecar", name)
	}
	assert.False(t, isPluginSidecar("infracost-parser-terraform"))
}

// TestStateConcurrentWritesSurvive stresses the atomic replace: many
// concurrent saves must leave a valid, fully-formed JSON file (last write
// wins is acceptable; a corrupt/half-written file is not).
func TestStateConcurrentWritesSurvive(t *testing.T) {
	dir := t.TempDir()

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			s := loadState(dir)
			s.upsert(sampleRecord("acme/one", "1.0.0"))
			_ = s.save(dir)
		})
	}
	wg.Wait()

	// The final file must be valid JSON, whichever writer landed last.
	raw := readRawState(t, dir)
	assert.Equal(t, stateSchemaVersion, raw.SchemaVersion)
	assert.NotNil(t, loadState(dir).find("acme/one"))
}

func TestConfigStateUsesCacheDir(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Cache: dir}

	s := c.LoadState()
	s.upsert(sampleRecord("acme/one", "1.0.0"))
	require.NoError(t, c.SaveState(s))

	assert.FileExists(t, filepath.Join(dir, stateFileName))
	assert.NotNil(t, c.LoadState().find("acme/one"))
}
