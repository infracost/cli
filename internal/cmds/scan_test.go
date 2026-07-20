package cmds

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePluginOptions(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		want    map[string]map[string]any
	}{
		{
			name:    "no options",
			options: nil,
			want:    map[string]map[string]any{},
		},
		{
			name:    "single nested key",
			options: []string{"terraform.sourceMap.http=https"},
			want: map[string]map[string]any{
				"terraform": {"sourceMap": map[string]any{"http": "https"}},
			},
		},
		{
			name:    "flat key",
			options: []string{"terraform.foo=bar"},
			want: map[string]map[string]any{
				"terraform": {"foo": "bar"},
			},
		},
		{
			name:    "missing value defaults to boolean true",
			options: []string{"terraform.foo"},
			want: map[string]map[string]any{
				"terraform": {"foo": true},
			},
		},
		{
			name:    "value containing an equals sign is preserved",
			options: []string{"terraform.foo=a=b"},
			want: map[string]map[string]any{
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
			want: map[string]map[string]any{
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
			want: map[string]map[string]any{
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
