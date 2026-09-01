package cmds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePluginNameVersion(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantVer  string
	}{
		{"infracost/terraform", "infracost/terraform", ""},
		{"acme/tf@1.2.3", "acme/tf", "1.2.3"},
		{"acme/tf@", "acme/tf", ""},
	}
	for _, c := range cases {
		name, ver := parsePluginNameVersion(c.in)
		assert.Equal(t, c.wantName, name, c.in)
		assert.Equal(t, c.wantVer, ver, c.in)
	}
}

// TestPluginInstall_UnknownNameSuggestion exercises the command's resolve step
// against a stub registry, asserting an unknown name surfaces a suggestion.
func TestPluginInstall_UnknownNameSuggestion(t *testing.T) {
	manifest := registry.Registry{
		SchemaVersion: registry.SupportedSchemaVersion,
		Plugins: []registry.Entry{{
			Name:     "infracost/terraform",
			Official: true,
			Components: []registry.Component{{
				Type:       registry.ComponentTypeParser,
				BinaryName: "infracost-parser-terraform",
				Platforms:  []string{"linux/amd64", "darwin/arm64", "darwin/amd64", "windows/amd64"},
				Download:   "https://example.com/{version}/data.tar.gz",
				Checksums:  "https://example.com/{version}/data.tar.gz.sha256",
			}},
			VersionURL: "https://example.com/version",
		}},
	}
	body, err := json.Marshal(manifest)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := &registry.Client{URL: srv.URL, CachePath: filepath.Join(t.TempDir(), "registry.json")}
	reg, err := client.Load(context.Background())
	require.NoError(t, err)

	// A close typo of a real entry surfaces a "did you mean" suggestion.
	_, err = reg.Resolve("infracost/terraformm", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in registry")
	assert.Contains(t, err.Error(), "infracost/terraform")
}
