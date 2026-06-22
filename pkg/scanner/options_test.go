package scanner

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildProviderOptions(t *testing.T) {
	const k8s = "infracost/kubernetes"
	clusterJSON := []byte(`{"cloud_provider":"aws","region":"us-east-1"}`)

	tests := []struct {
		name       string
		opts       *ScanProjectOptions
		provider   string
		wantRaw    []byte
		wantFormat string
	}{
		{
			name:       "matching provider returns its options as json",
			opts:       &ScanProjectOptions{ProviderOptions: map[string][]byte{k8s: clusterJSON}},
			provider:   k8s,
			wantRaw:    clusterJSON,
			wantFormat: "application/json",
		},
		{
			name:     "other provider gets nothing",
			opts:     &ScanProjectOptions{ProviderOptions: map[string][]byte{k8s: clusterJSON}},
			provider: "infracost/ai",
		},
		{
			name:     "no provider options at all",
			opts:     &ScanProjectOptions{},
			provider: k8s,
		},
		{
			name:     "empty options value is treated as absent",
			opts:     &ScanProjectOptions{ProviderOptions: map[string][]byte{k8s: {}}},
			provider: k8s,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, format := buildProviderOptions(tt.opts, tt.provider)
			require.Equal(t, tt.wantRaw, raw)
			require.Equal(t, tt.wantFormat, format)
		})
	}
}
