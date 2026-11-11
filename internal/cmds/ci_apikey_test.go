package cmds

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCIAPIKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{
			name: "a CLI token is accepted",
			key:  "eyJhbGciOiJSUzI1NiJ9.payload.signature",
		},
		{
			name: "an empty key is left to the missing-key path",
			key:  "",
		},
		{
			name:    "a v0.1 API key is named outright",
			key:     "ico-abc123",
			wantErr: "is a v0.1 API key (ico-…)",
		},
		{
			name:    "an unexpanded Azure variable is named",
			key:     "$(INFRACOST_API_KEY)",
			wantErr: "unexpanded variable reference",
		},
		{
			name:    "an unexpanded shell variable is named",
			key:     "${INFRACOST_API_KEY}",
			wantErr: "unexpanded variable reference",
		},
		{
			name:    "surrounding whitespace is named",
			key:     "token\n",
			wantErr: "leading or trailing whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCIAPIKey(tt.key)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
