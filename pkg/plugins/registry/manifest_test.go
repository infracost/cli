package registry

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validManifest is a minimal single-component manifest used across tests.
const validManifest = `{
  "schemaVersion": 1,
  "plugins": [
    {
      "name": "infracost/terraform",
      "displayName": "Terraform",
      "description": "Parses Terraform HCL.",
      "author": "infracost",
      "official": true,
      "homepage": "https://github.com/infracost/terraform",
      "license": "Apache-2.0",
      "versionUrl": "https://releases.infracost.io/infracost-parser-terraform/{os}/{arch}/latest/version",
      "components": [
        {
          "type": "parser",
          "binaryName": "infracost-parser-terraform",
          "platforms": ["linux/amd64", "darwin/arm64"],
          "download": "https://releases.infracost.io/infracost-parser-terraform/{os}/{arch}/{version}/data.tar.gz",
          "checksums": "https://releases.infracost.io/infracost-parser-terraform/{os}/{arch}/{version}/data.tar.gz.sha256"
        }
      ]
    }
  ]
}`

func TestParseValid(t *testing.T) {
	reg, err := Parse([]byte(validManifest))
	require.NoError(t, err)
	require.Len(t, reg.Plugins, 1)

	e := reg.Plugins[0]
	assert.Equal(t, "infracost/terraform", e.Name)
	assert.True(t, e.Official)
	assert.True(t, e.Installable())
	require.Len(t, e.Components, 1)
	assert.Equal(t, ComponentTypeParser, e.Components[0].Type)
	assert.Equal(t, "parser", e.Capabilities())
}

func TestParseDualComponentKubernetes(t *testing.T) {
	// A single infracost/kubernetes entry declaring both its parser and
	// provider components with separate binary artifacts.
	manifest := `{
      "schemaVersion": 1,
      "plugins": [
        {
          "name": "infracost/kubernetes",
          "displayName": "Kubernetes",
          "description": "Kubernetes parser and provider.",
          "author": "infracost",
          "official": true,
          "homepage": "https://github.com/infracost/kubernetes",
          "license": "Apache-2.0",
          "versionUrl": "https://releases.infracost.io/infracost-parser-kubernetes/{os}/{arch}/latest/version",
          "components": [
            {
              "type": "parser",
              "binaryName": "infracost-parser-kubernetes",
              "platforms": ["linux/amd64"],
              "download": "https://releases.infracost.io/infracost-parser-kubernetes/{os}/{arch}/{version}/data.tar.gz",
              "checksums": "https://releases.infracost.io/infracost-parser-kubernetes/{os}/{arch}/{version}/data.tar.gz.sha256"
            },
            {
              "type": "provider",
              "binaryName": "infracost-provider-kubernetes",
              "platforms": ["linux/amd64"],
              "download": "https://releases.infracost.io/infracost-provider-kubernetes/{os}/{arch}/{version}/data.tar.gz",
              "checksums": "https://releases.infracost.io/infracost-provider-kubernetes/{os}/{arch}/{version}/data.tar.gz.sha256"
            }
          ]
        }
      ]
    }`

	reg, err := Parse([]byte(manifest))
	require.NoError(t, err)
	require.Len(t, reg.Plugins, 1)

	e := reg.Plugins[0]
	require.Len(t, e.Components, 2)
	assert.Equal(t, "parser + provider", e.Capabilities())

	parser, ok := e.Component(ComponentTypeParser)
	require.True(t, ok)
	assert.Equal(t, "infracost-parser-kubernetes", parser.BinaryName)

	provider, ok := e.Component(ComponentTypeProvider)
	require.True(t, ok)
	assert.Equal(t, "infracost-provider-kubernetes", provider.BinaryName)
}

func TestParseRejections(t *testing.T) {
	entry := func(name string, components string) string {
		return fmt.Sprintf(`{"schemaVersion":1,"plugins":[{"name":%q,"versionUrl":"https://x/v","components":[%s]}]}`, name, components)
	}
	parserComp := `{"type":"parser","binaryName":"infracost-parser-x","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}`

	tests := []struct {
		name        string
		manifest    string
		wantErrPart string
	}{
		{
			name:        "bad name without namespace",
			manifest:    entry("terraform", parserComp),
			wantErrPart: "namespaced as <github-owner>/<github-repository>",
		},
		{
			name:        "bad name empty repo",
			manifest:    entry("infracost/", parserComp),
			wantErrPart: "invalid plugin name",
		},
		{
			name:        "empty components",
			manifest:    entry("infracost/x", ""),
			wantErrPart: "has no components",
		},
		{
			name:        "missing checksums",
			manifest:    entry("infracost/x", `{"type":"parser","binaryName":"b","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz"}`),
			wantErrPart: "no checksums URL template",
		},
		{
			name:        "missing download",
			manifest:    entry("infracost/x", `{"type":"parser","binaryName":"b","platforms":["linux/amd64"],"checksums":"https://x/{version}/d.tar.gz.sha256"}`),
			wantErrPart: "no download URL template",
		},
		{
			name:        "no platforms",
			manifest:    entry("infracost/x", `{"type":"parser","binaryName":"b","platforms":[],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}`),
			wantErrPart: "lists no platforms",
		},
		{
			name:        "bad platform format",
			manifest:    entry("infracost/x", `{"type":"parser","binaryName":"b","platforms":["linux"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}`),
			wantErrPart: "invalid platform",
		},
		{
			name:        "duplicate component type",
			manifest:    entry("infracost/x", `{"type":"parser","binaryName":"b1","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"},{"type":"parser","binaryName":"b2","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}`),
			wantErrPart: "more than one parser component",
		},
		{
			name:        "too many components",
			manifest:    entry("infracost/x", parserComp+`,{"type":"provider","binaryName":"p","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"},{"type":"parser","binaryName":"q","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}`),
			wantErrPart: "at most one parser and one provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.manifest))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErrPart)
		})
	}
}

func TestParseDuplicateName(t *testing.T) {
	manifest := `{
      "schemaVersion": 1,
      "plugins": [
        {"name":"infracost/dup","versionUrl":"https://x/v","components":[{"type":"parser","binaryName":"a","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}]},
        {"name":"infracost/dup","versionUrl":"https://x/v","components":[{"type":"provider","binaryName":"b","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}]}
      ]
    }`
	_, err := Parse([]byte(manifest))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate plugin name "infracost/dup"`)
}

func TestParseDuplicateBinaryNameAcrossEntries(t *testing.T) {
	manifest := `{
      "schemaVersion": 1,
      "plugins": [
        {"name":"infracost/a","versionUrl":"https://x/v","components":[{"type":"parser","binaryName":"shared","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}]},
        {"name":"infracost/b","versionUrl":"https://x/v","components":[{"type":"parser","binaryName":"shared","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}]}
      ]
    }`
	_, err := Parse([]byte(manifest))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate component binaryName "shared"`)
}

func TestParseUnknownComponentType(t *testing.T) {
	// An unknown component type must NOT reject the manifest; the entry is kept
	// but marked uninstallable so it still lists.
	manifest := `{
      "schemaVersion": 1,
      "plugins": [
        {"name":"acme/future","versionUrl":"https://x/v","components":[{"type":"linter","binaryName":"infracost-linter-acme","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}]}
      ]
    }`
	reg, err := Parse([]byte(manifest))
	require.NoError(t, err)
	require.Len(t, reg.Plugins, 1)

	e := reg.Plugins[0]
	assert.False(t, e.Installable())
	assert.Contains(t, e.UninstallableReason(), "linter")
	assert.Contains(t, e.UninstallableReason(), "newer CLI may be required")
}

func TestParseUnknownSchemaVersion(t *testing.T) {
	manifest := fmt.Sprintf(`{"schemaVersion": %d, "plugins": []}`, SupportedSchemaVersion+1)
	_, err := Parse([]byte(manifest))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer than this CLI supports")
	assert.Contains(t, err.Error(), "upgrade the Infracost CLI")
}

func TestParseMissingSchemaVersion(t *testing.T) {
	_, err := Parse([]byte(`{"plugins": []}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing a valid schemaVersion")
}

func TestParseInvalidJSON(t *testing.T) {
	_, err := Parse([]byte(`{"schemaVersion": 1, "plugins": [`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid registry manifest JSON")
}

func TestEntryPlatformSupport(t *testing.T) {
	manifest := `{
      "schemaVersion": 1,
      "plugins": [
        {"name":"infracost/x","versionUrl":"https://x/v","components":[
          {"type":"parser","binaryName":"p","platforms":["linux/amd64","darwin/arm64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"},
          {"type":"provider","binaryName":"q","platforms":["linux/amd64"],"download":"https://x/{version}/d.tar.gz","checksums":"https://x/{version}/d.tar.gz.sha256"}
        ]}
      ]
    }`
	reg, err := Parse([]byte(manifest))
	require.NoError(t, err)
	e := reg.Plugins[0]

	assert.True(t, e.SupportsPlatform("linux", "amd64"))
	// darwin/arm64 supported by parser but not provider — entry unsupported.
	assert.False(t, e.SupportsPlatform("darwin", "arm64"))

	unsupported := e.UnsupportedComponents("darwin", "arm64")
	require.Len(t, unsupported, 1)
	assert.Equal(t, "q", unsupported[0].BinaryName)
}

// TestRequiredPluginsExpressible confirms every current required plugin is
// representable in the manifest schema — including the dual-component
// infracost/kubernetes entry using the releases.infracost.io URL layout.
func TestRequiredPluginsExpressible(t *testing.T) {
	reg, err := Parse([]byte(requiredPluginsManifest))
	require.NoError(t, err)

	byName := map[string]Entry{}
	for _, e := range reg.Plugins {
		byName[e.Name] = e
		assert.True(t, e.Official, "required plugin %q should be official", e.Name)
		assert.True(t, e.Installable(), "required plugin %q should be installable", e.Name)
	}

	// Single-component officials.
	for _, name := range []string{
		"infracost/terraform", "infracost/terragrunt", "infracost/cloudformation",
		"infracost/ciscostacks", "infracost/arm", "infracost/terraform-plan",
		"infracost/aws", "infracost/google", "infracost/azure",
	} {
		e, ok := byName[name]
		require.True(t, ok, "missing required plugin %q", name)
		assert.Len(t, e.Components, 1)
	}

	// Kubernetes is a single dual-component entry.
	k8s, ok := byName["infracost/kubernetes"]
	require.True(t, ok)
	require.Len(t, k8s.Components, 2)
	_, hasParser := k8s.Component(ComponentTypeParser)
	_, hasProvider := k8s.Component(ComponentTypeProvider)
	assert.True(t, hasParser)
	assert.True(t, hasProvider)

	// Every declared download URL resolves to the existing releases layout.
	tf := byName["infracost/terraform"].Components[0]
	assert.Equal(t,
		"https://releases.infracost.io/infracost-parser-terraform/linux/amd64/0.1.0/data.tar.gz",
		tf.DownloadURL("0.1.0", "linux", "amd64"),
	)
}
