package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}

func pluginValidateTestConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	// Dir is the local plugin-dir override; PluginDir() returns it directly.
	cfg.Plugins.Dir = dir
	return cfg
}

func TestPluginValidateEmptyDirIsFriendlyAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	cmd := pluginsValidateCmd(pluginValidateTestConfig(t, dir))
	cmd.SetArgs([]string{})

	out := captureStdout(t, func() {
		require.NoError(t, cmd.Execute())
	})

	assert.Contains(t, out, "No plugins found")
	assert.Contains(t, out, dir)
}

func TestPluginValidateNonExecutableFileFailsAndExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	// A regular non-executable file fails check 1 without spawning a
	// subprocess (POSIX exec-bit check).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-parser-broken"), []byte("x"), 0o600))

	cmd := pluginsValidateCmd(pluginValidateTestConfig(t, dir))
	cmd.SetArgs([]string{})

	var runErr error
	_ = captureStdout(t, func() { runErr = cmd.Execute() })

	require.Error(t, runErr, "a failing binary must make validate exit non-zero")
	assert.ErrorIs(t, runErr, errPluginValidationFailed)
}

func TestPluginValidateJSONShape(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "infracost-parser-broken"), []byte("x"), 0o600))

	cmd := pluginsValidateCmd(pluginValidateTestConfig(t, dir))
	cmd.SetArgs([]string{"--json"})

	var runErr error
	out := captureStdout(t, func() { runErr = cmd.Execute() })

	// Non-executable file fails, so exit is non-zero even in JSON mode.
	assert.ErrorIs(t, runErr, errPluginValidationFailed)

	var envelope struct {
		OK      bool                       `json:"ok"`
		Results []plugins.ValidationResult `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))
	assert.False(t, envelope.OK)
	require.Len(t, envelope.Results, 1)

	// Every check carries a status and a name.
	require.NotEmpty(t, envelope.Results[0].Checks)
	for _, c := range envelope.Results[0].Checks {
		assert.NotEmpty(t, c.Status, "each check has a status")
		assert.NotEmpty(t, c.Name, "each check has a name")
		assert.NotEmpty(t, c.ID, "each check has a stable id")
	}
}

// stubValidateRegistry overrides the --release registry-load seam so name
// resolution runs against a fixture without a network fetch.
func stubValidateRegistry(t *testing.T, reg *registry.Registry, err error) {
	t.Helper()
	orig := validateRegistryLoad
	validateRegistryLoad = func(context.Context) (*registry.Registry, error) { return reg, err }
	t.Cleanup(func() { validateRegistryLoad = orig })
}

func TestValidateReleaseUnknownRegistryName(t *testing.T) {
	stubValidateRegistry(t, browseFixtureRegistry(), nil)

	err := runValidateRelease(newTestCmd(), "infracost/nonexistent", false, false, false)
	require.Error(t, err)
	// Mirrors install's "not found in registry" shape, with a suggestion.
	assert.Contains(t, err.Error(), "not found in registry")
}

func TestResolveReleaseEntryLocalFileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest-entry.json")
	entryJSON := `{
		"name": "acme/widget",
		"official": false,
		"components": [{
			"type": "parser",
			"binaryName": "acme-parser-widget",
			"platforms": ["linux/amd64"],
			"download": "https://example.com/{version}/{os}/{arch}/data.tar.gz",
			"checksums": "https://example.com/{version}/{os}/{arch}/data.tar.gz.sha256"
		}]
	}`
	require.NoError(t, os.WriteFile(path, []byte(entryJSON), 0o600))

	entry, source, version, err := resolveReleaseEntry(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, "file", source)
	assert.Empty(t, version)
	require.NotNil(t, entry)
	assert.Equal(t, "acme/widget", entry.Name)
}

func TestResolveReleaseEntryFilePinnedVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entry.json")
	entryJSON := `{
		"name": "acme/widget",
		"components": [{
			"type": "parser",
			"binaryName": "acme-parser-widget",
			"platforms": ["linux/amd64"],
			"download": "https://example.com/{version}/data.tar.gz",
			"checksums": "https://example.com/{version}/data.tar.gz.sha256"
		}]
	}`
	require.NoError(t, os.WriteFile(path, []byte(entryJSON), 0o600))

	// A name@version split where the name half is a file pins the version.
	_, source, version, err := resolveReleaseEntry(context.Background(), path+"@2.3.4")
	require.NoError(t, err)
	assert.Equal(t, "file", source)
	assert.Equal(t, "2.3.4", version)
}

func TestValidateReleaseRejectsPathAndReleaseCombo(t *testing.T) {
	cmd := pluginsValidateCmd(pluginValidateTestConfig(t, t.TempDir()))
	cmd.SetArgs([]string{"--release", "acme/widget", "/some/binary/path"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot combine")
}

func TestPluginValidateJSONEmptyDir(t *testing.T) {
	dir := t.TempDir()
	cmd := pluginsValidateCmd(pluginValidateTestConfig(t, dir))
	cmd.SetArgs([]string{"--json"})

	out := captureStdout(t, func() {
		require.NoError(t, cmd.Execute())
	})

	var envelope struct {
		OK      bool                       `json:"ok"`
		Results []plugins.ValidationResult `json:"results"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &envelope))
	assert.True(t, envelope.OK)
	assert.Empty(t, envelope.Results)
}
