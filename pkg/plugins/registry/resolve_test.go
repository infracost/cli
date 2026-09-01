package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := Parse([]byte(requiredPluginsManifest))
	require.NoError(t, err)
	return reg
}

func TestResolveByName(t *testing.T) {
	reg := testRegistry(t)
	e, err := reg.Resolve("infracost/terraform", nil)
	require.NoError(t, err)
	assert.Equal(t, "infracost/terraform", e.Name)
}

func TestResolveByBinaryName(t *testing.T) {
	reg := testRegistry(t)

	e, err := reg.Resolve("infracost-provider-kubernetes", nil)
	require.NoError(t, err)
	assert.Equal(t, "infracost/kubernetes", e.Name)

	// A Windows-style filename with .exe still resolves to the entry.
	e, err = reg.Resolve("infracost-parser-terraform.exe", nil)
	require.NoError(t, err)
	assert.Equal(t, "infracost/terraform", e.Name)
}

func TestResolveByAlias(t *testing.T) {
	reg := testRegistry(t)
	aliases := map[string]string{
		"terraform":                  "infracost/terraform",
		"infracost-plugin-terraform": "infracost/terraform", // legacy binary name
		"kubernetes":                 "infracost/kubernetes",
	}

	e, err := reg.Resolve("terraform", aliases)
	require.NoError(t, err)
	assert.Equal(t, "infracost/terraform", e.Name)

	e, err = reg.Resolve("infracost-plugin-terraform", aliases)
	require.NoError(t, err)
	assert.Equal(t, "infracost/terraform", e.Name)
}

func TestResolveUnknownWithSuggestion(t *testing.T) {
	reg := testRegistry(t)
	_, err := reg.Resolve("infracost/terrafform", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in registry")
	assert.Contains(t, err.Error(), `did you mean "infracost/terraform"?`)
}

func TestResolveUnknownNoSuggestion(t *testing.T) {
	reg := testRegistry(t)
	_, err := reg.Resolve("something-completely-different-xyz", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in registry")
	assert.NotContains(t, err.Error(), "did you mean")
}

func TestResolveEmpty(t *testing.T) {
	reg := testRegistry(t)
	_, err := reg.Resolve("   ", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no plugin name provided")
}

func TestByName(t *testing.T) {
	reg := testRegistry(t)
	assert.NotNil(t, reg.ByName("infracost/aws"))
	assert.Nil(t, reg.ByName("infracost/nope"))
}
