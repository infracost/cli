package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveOrgID(t *testing.T) {
	orgs := []CachedOrganization{
		{ID: "org-1", Name: "Personal", Slug: "personal"},
		{ID: "org-2", Name: "Acme Corp", Slug: "acme-corp"},
	}

	tests := []struct {
		name    string
		flag    string
		wantID  string
		wantErr string
	}{
		{
			name:   "match by slug",
			flag:   "acme-corp",
			wantID: "org-2",
		},
		{
			name:   "match by slug case-insensitive",
			flag:   "Acme-Corp",
			wantID: "org-2",
		},
		{
			name:   "match by ID",
			flag:   "org-1",
			wantID: "org-1",
		},
		{
			name:    "empty value",
			flag:    "",
			wantErr: "--org was passed an empty value",
		},
		{
			name:    "no match with suggestion",
			flag:    "acme-corpp",
			wantErr: "Did you mean 'acme-corp'?",
		},
		{
			name:    "no match completely different",
			flag:    "totally-unknown-org",
			wantErr: "is not an organization you have access to",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, _, err := ResolveOrgID(tt.flag, orgs)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
		})
	}
}
