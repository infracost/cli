package cmds

import (
	"testing"

	"github.com/infracost/cli/internal/format"
	pkgscanner "github.com/infracost/cli/pkg/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePluginOptions(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    pkgscanner.PluginOpts
	}{
		{
			name:    "no options",
			options: nil,
			want:    pkgscanner.PluginOpts{},
		},
		{
			name:    "single nested key",
			options: []string{"terraform.sourceMap.http=https"},
			want: pkgscanner.PluginOpts{
				"terraform": {"sourceMap": map[string]any{"http": "https"}},
			},
		},
		{
			name:    "flat key",
			options: []string{"terraform.foo=bar"},
			want: pkgscanner.PluginOpts{
				"terraform": {"foo": "bar"},
			},
		},
		{
			name:    "missing value defaults to boolean true",
			options: []string{"terraform.foo"},
			want: pkgscanner.PluginOpts{
				"terraform": {"foo": true},
			},
		},
		{
			name:    "value containing an equals sign is preserved",
			options: []string{"terraform.foo=a=b"},
			want: pkgscanner.PluginOpts{
				"terraform": {"foo": "a=b"},
			},
		},
		{
			name: "multiple options merge under the same plugin",
			options: []string{
				"terraform.sourceMap.http=https",
				"terraform.sourceMap.ssh=git",
				"terraform.other=x",
			},
			want: pkgscanner.PluginOpts{
				"terraform": {
					"sourceMap": map[string]any{"http": "https", "ssh": "git"},
					"other":     "x",
				},
			},
		},
		{
			name: "options for different plugins are kept separate",
			options: []string{
				"terraform.foo=1",
				"cloudformation.bar=2",
			},
			want: pkgscanner.PluginOpts{
				"terraform":      {"foo": "1"},
				"cloudformation": {"bar": "2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePluginOptions(tt.options)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCriticalDiagnosticsErr(t *testing.T) {
	project := func(diagSeverities ...string) format.ProjectOutput {
		var diags []format.DiagnosticOutput
		for _, s := range diagSeverities {
			diags = append(diags, format.DiagnosticOutput{Severity: s, Message: "boom"})
		}
		return format.ProjectOutput{ProjectName: "p", Diagnostics: diags}
	}

	t.Run("no diagnostics", func(t *testing.T) {
		r := &format.Output{Projects: []format.ProjectOutput{project()}}
		assert.NoError(t, criticalDiagnosticsErr(r))
	})

	t.Run("warnings only", func(t *testing.T) {
		r := &format.Output{Projects: []format.ProjectOutput{project("warning", "info")}}
		assert.NoError(t, criticalDiagnosticsErr(r))
	})

	t.Run("single critical", func(t *testing.T) {
		r := &format.Output{Projects: []format.ProjectOutput{project("critical")}}
		err := criticalDiagnosticsErr(r)
		require.Error(t, err)
		assert.Equal(t, "scan completed with 1 critical diagnostic", err.Error())
	})

	t.Run("criticals across projects", func(t *testing.T) {
		r := &format.Output{Projects: []format.ProjectOutput{
			project("critical", "warning"),
			project("critical"),
		}}
		err := criticalDiagnosticsErr(r)
		require.Error(t, err)
		assert.Equal(t, "scan completed with 2 critical diagnostics", err.Error())
	})
}

func TestParsePluginOptionsErrors(t *testing.T) {
	tests := []struct {
		name    string
		options []string
	}{
		{
			name:    "plugin name with no key",
			options: []string{"terraform"},
		},
		{
			name:    "plugin name with no key but a value",
			options: []string{"terraform=yes"},
		},
		{
			name: "intermediate key already set to a non-map value",
			options: []string{
				"terraform.foo=x",
				"terraform.foo.bar=y",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePluginOptions(tt.options)
			assert.Error(t, err)
		})
	}
}
