package cmds

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/plugins"
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
