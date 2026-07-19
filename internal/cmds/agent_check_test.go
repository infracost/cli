package cmds

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsSkillStale(t *testing.T) {
	tests := []struct {
		installed, latest string
		want              bool
	}{
		{"0.1.0", "0.1.1", true},
		{"0.1.1", "0.1.1", false},
		{"0.2.0", "0.1.1", false}, // ahead of latest — not stale
		{"v0.1.0", "0.1.1", true}, // v-prefix tolerated by semver
		{"garbage", "0.1.1", false},
		{"0.1.0", "garbage", false},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, isSkillStale(tt.installed, tt.latest),
			"isSkillStale(%q,%q)", tt.installed, tt.latest)
	}
}

func TestPluginListVersion(t *testing.T) {
	dir := t.TempDir()

	// A fake agent CLI that prints a plugin-list line mentioning infracost.
	withVersion := filepath.Join(dir, "agent-with")
	require.NoError(t, os.WriteFile(withVersion,
		[]byte("#!/bin/sh\necho 'infracost@infracost  v0.1.1  enabled'\n"), 0o750)) //nolint:gosec // test fixture

	noInfracost := filepath.Join(dir, "agent-none")
	require.NoError(t, os.WriteFile(noInfracost,
		[]byte("#!/bin/sh\necho 'other-plugin  v9.9.9'\n"), 0o750)) //nolint:gosec // test fixture

	noVersion := filepath.Join(dir, "agent-noversion")
	require.NoError(t, os.WriteFile(noVersion,
		[]byte("#!/bin/sh\necho 'infracost  (installed)'\n"), 0o750)) //nolint:gosec // test fixture

	t.Run("parses version on infracost line", func(t *testing.T) {
		v, err := pluginListVersion(withVersion, "plugin", "list")
		require.NoError(t, err)
		assert.Equal(t, "0.1.1", v)
	})
	t.Run("no infracost line -> empty", func(t *testing.T) {
		v, err := pluginListVersion(noInfracost, "plugin", "list")
		require.NoError(t, err)
		assert.Empty(t, v)
	})
	t.Run("infracost but no semver -> empty", func(t *testing.T) {
		v, err := pluginListVersion(noVersion, "plugin", "list")
		require.NoError(t, err)
		assert.Empty(t, v)
	})
	t.Run("missing binary -> error", func(t *testing.T) {
		_, err := pluginListVersion(filepath.Join(dir, "does-not-exist"))
		require.Error(t, err)
	})
}

func TestReadPluginManifestVersion(t *testing.T) {
	repo := t.TempDir()
	manifestDir := filepath.Join(repo, "plugins", "infracost", ".claude-plugin")
	require.NoError(t, os.MkdirAll(manifestDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(manifestDir, "plugin.json"),
		[]byte(`{"name":"infracost","version":"0.1.1"}`), 0o600))

	assert.Equal(t, "0.1.1", readPluginManifestVersion(repo))
	assert.Empty(t, readPluginManifestVersion(t.TempDir()), "missing manifest -> empty")
}

func TestFormatStaleAgentsNotice(t *testing.T) {
	assert.Empty(t, FormatStaleAgentsNotice(nil))

	one := FormatStaleAgentsNotice([]StaleAgent{{Name: "GitLab Duo", Installed: "0.1.0", Latest: "0.1.1"}})
	assert.Contains(t, one, "GitLab Duo")
	assert.Contains(t, one, "0.1.0")
	assert.Contains(t, one, "infracost agent setup")

	many := FormatStaleAgentsNotice([]StaleAgent{
		{Name: "Claude Code", Installed: "0.1.0", Latest: "0.1.1"},
		{Name: "GitLab Duo", Installed: "0.0.5", Latest: "0.1.1"},
	})
	assert.Contains(t, many, "Claude Code")
	assert.Contains(t, many, "GitLab Duo")
	assert.Contains(t, many, "infracost agent setup")
}

// withCheckEnabled makes the staleness check believe it's running in a
// real (non-test) binary and redirects its cache to a temp file. Returns
// the cache path.
func withCheckEnabled(t *testing.T) string {
	t.Helper()
	orig := isAgentCheckTestBinary
	isAgentCheckTestBinary = func() bool { return false }
	t.Cleanup(func() { isAgentCheckTestBinary = orig })
	t.Setenv("INFRACOST_SKIP_AGENT_CHECK", "")

	path := filepath.Join(t.TempDir(), "agent-skills-check.json")
	origPath := agentCheckCachePath
	agentCheckCachePath = func() string { return path }
	t.Cleanup(func() { agentCheckCachePath = origPath })
	return path
}

func TestAgentCheckCacheRoundTrip(t *testing.T) {
	withCheckEnabled(t)

	require.Nil(t, CachedStaleAgents(), "no cache yet -> nil")

	want := &agentCheckCache{
		CheckedAt: time.Now(),
		Latest:    "0.1.1",
		Stale:     []StaleAgent{{Name: "GitLab Duo", Installed: "0.1.0", Latest: "0.1.1"}},
	}
	require.NoError(t, saveAgentCheckCache(want))

	got := CachedStaleAgents()
	require.Len(t, got, 1)
	assert.Equal(t, "GitLab Duo", got[0].Name)

	ClearStaleAgentsCache()
	assert.Nil(t, CachedStaleAgents(), "cleared cache -> nil")
}

func TestRefreshStaleAgentsIfStale_UsesFreshCache(t *testing.T) {
	withCheckEnabled(t)

	// A fresh cache must be returned verbatim without a live probe or
	// network call (which would otherwise be attempted and likely fail).
	require.NoError(t, saveAgentCheckCache(&agentCheckCache{
		CheckedAt: time.Now(),
		Latest:    "0.1.1",
		Stale:     []StaleAgent{{Name: "Claude Code", Installed: "0.1.0", Latest: "0.1.1"}},
	}))

	got := RefreshStaleAgentsIfStale(t.Context())
	require.Len(t, got, 1)
	assert.Equal(t, "Claude Code", got[0].Name)
}

func TestSkipAgentCheck(t *testing.T) {
	// In a test binary the check is skipped, so the hot-path helpers no-op.
	assert.True(t, skipAgentCheck(), "test binary should skip")
	assert.Nil(t, CachedStaleAgents())
	assert.Nil(t, RefreshStaleAgentsIfStale(t.Context()))

	// Opt-out env var also skips, even outside a test binary.
	orig := isAgentCheckTestBinary
	isAgentCheckTestBinary = func() bool { return false }
	t.Cleanup(func() { isAgentCheckTestBinary = orig })
	t.Setenv("INFRACOST_SKIP_AGENT_CHECK", "1")
	assert.True(t, skipAgentCheck(), "env opt-out should skip")
}
