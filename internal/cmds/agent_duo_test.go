package cmds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDuoConfigDir_Precedence(t *testing.T) {
	// Isolate from whatever the host environment has set.
	t.Setenv("GLAB_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	t.Run("GLAB_CONFIG_DIR wins", func(t *testing.T) {
		t.Setenv("GLAB_CONFIG_DIR", "/custom/glab")
		t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
		dir, err := duoConfigDir()
		require.NoError(t, err)
		assert.Equal(t, "/custom/glab", dir)
	})

	t.Run("XDG_CONFIG_HOME is next", func(t *testing.T) {
		t.Setenv("GLAB_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
		dir, err := duoConfigDir()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/custom/xdg", "gitlab", "duo"), dir)
	})

	t.Run("defaults to home", func(t *testing.T) {
		t.Setenv("GLAB_CONFIG_DIR", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		dir, err := duoConfigDir()
		require.NoError(t, err)
		// On Windows the default is %APPDATA%\GitLab\duo, so only assert
		// the ~/.gitlab/duo shape where UserHomeDir drives it.
		if home != "" && os.Getenv("APPDATA") == "" {
			assert.Equal(t, filepath.Join(home, ".gitlab", "duo"), dir)
		}
	})
}

func TestWriteDuoSkills(t *testing.T) {
	// Build a minimal fake agent-skills checkout.
	repoDir := t.TempDir()
	skillsSrc := filepath.Join(repoDir, "plugins", "infracost", "skills")
	for _, name := range duoSkills {
		require.NoError(t, os.MkdirAll(filepath.Join(skillsSrc, name), 0o750))
		body := "---\nname: infracost-" + name + "\ndescription: test\n---\n\nSee [BINDINGS.md](../../BINDINGS.md).\n"
		require.NoError(t, os.WriteFile(filepath.Join(skillsSrc, name, "SKILL.md"), []byte(body), 0o600))
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(repoDir, "plugins", "infracost", "BINDINGS.md"),
		[]byte("# command matrix\n"), 0o600))

	baseDir := t.TempDir()
	require.NoError(t, writeDuoSkills(repoDir, baseDir))

	for _, name := range duoSkills {
		skillPath := filepath.Join(baseDir, "skills", name, "SKILL.md")
		bindingsPath := filepath.Join(baseDir, "skills", name, "BINDINGS.md")

		skill, err := os.ReadFile(skillPath) //nolint:gosec // test-controlled path
		require.NoError(t, err, "skill %s should be written", name)
		// The ../../ link is rewritten to the co-located copy...
		assert.Contains(t, string(skill), "](BINDINGS.md)")
		// ...and the original escaping link no longer appears.
		assert.NotContains(t, string(skill), "../../BINDINGS.md")

		assert.FileExists(t, bindingsPath, "BINDINGS.md should be co-located with skill %s", name)
	}

	// fix-findings is MCP-only and must not be installed for Duo.
	assert.NoDirExists(t, filepath.Join(baseDir, "skills", "fix-findings"))
}

func TestWriteDuoSkills_WithoutBindings(t *testing.T) {
	// A revision that ships the skills but not BINDINGS.md (e.g. the
	// MCP-only skills currently on agent-skills main) must still install
	// the skills rather than hard-failing.
	repoDir := t.TempDir()
	skillsSrc := filepath.Join(repoDir, "plugins", "infracost", "skills")
	for _, name := range duoSkills {
		require.NoError(t, os.MkdirAll(filepath.Join(skillsSrc, name), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(skillsSrc, name, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: t\n---\n"), 0o600))
	}
	// deliberately no BINDINGS.md

	baseDir := t.TempDir()
	require.NoError(t, writeDuoSkills(repoDir, baseDir))
	for _, name := range duoSkills {
		assert.FileExists(t, filepath.Join(baseDir, "skills", name, "SKILL.md"))
		assert.NoFileExists(t, filepath.Join(baseDir, "skills", name, "BINDINGS.md"))
	}
}

func TestWriteDuoSkills_EmptyCloneErrors(t *testing.T) {
	baseDir := t.TempDir()
	err := writeDuoSkills(t.TempDir(), baseDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Infracost skills found")
}

func TestWriteDuoSkills_IsIdempotent(t *testing.T) {
	repoDir := t.TempDir()
	skillsSrc := filepath.Join(repoDir, "plugins", "infracost", "skills")
	for _, name := range duoSkills {
		require.NoError(t, os.MkdirAll(filepath.Join(skillsSrc, name), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(skillsSrc, name, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: t\n---\n"), 0o600))
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(repoDir, "plugins", "infracost", "BINDINGS.md"), []byte("m"), 0o600))

	baseDir := t.TempDir()
	// Drop a stale file into a skill dir; a re-install should wipe it.
	staleDir := filepath.Join(baseDir, "skills", "scan")
	require.NoError(t, os.MkdirAll(staleDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(staleDir, "stale.md"), []byte("old"), 0o600))

	require.NoError(t, writeDuoSkills(repoDir, baseDir))
	require.NoError(t, writeDuoSkills(repoDir, baseDir))

	assert.NoFileExists(t, filepath.Join(staleDir, "stale.md"), "stale files should be cleared on re-install")
	assert.FileExists(t, filepath.Join(staleDir, "SKILL.md"))
}
