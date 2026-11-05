package scanner

import (
	"testing"

	repoconfig "github.com/infracost/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGenericOptions_PrivateSourceAllowlist(t *testing.T) {
	opts := &ScanProjectOptions{
		RootDir:                t.TempDir(),
		Project:                &repoconfig.Project{Name: "p"},
		PrivateSourceAllowlist: []string{"10.0.0.0/24", "code.example.com"},
	}
	assert.Equal(t, []string{"10.0.0.0/24", "code.example.com"}, buildGenericOptions(opts).GetPrivateSourceAllowlist())

	opts.PrivateSourceAllowlist = nil
	assert.Empty(t, buildGenericOptions(opts).GetPrivateSourceAllowlist(), "unset by default: the guard stays fully on")
}

// A parse that was cached under one allowlist must not be served under another:
// a module the guard refused is a different result from one it fetched.
func TestGenericOptionsFingerprint_PrivateSourceAllowlist(t *testing.T) {
	opts := &ScanProjectOptions{RootDir: t.TempDir(), Project: &repoconfig.Project{Name: "p"}}
	without, err := genericOptionsFingerprint(buildGenericOptions(opts))
	require.NoError(t, err)

	opts.PrivateSourceAllowlist = []string{"10.0.0.0/24"}
	with, err := genericOptionsFingerprint(buildGenericOptions(opts))
	require.NoError(t, err)
	assert.NotEqual(t, without, with)

	opts.PrivateSourceAllowlist = []string{"10.0.1.0/24"}
	other, err := genericOptionsFingerprint(buildGenericOptions(opts))
	require.NoError(t, err)
	assert.NotEqual(t, with, other)
}
