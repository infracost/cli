package orgresolve_test

import (
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/orgresolve"
	"github.com/infracost/cli/pkg/auth"
	"github.com/stretchr/testify/assert"
)

func TestRole(t *testing.T) {
	tests := []struct {
		name string
		org  auth.CachedOrganization
		want string
	}{
		{
			name: "owner",
			org:  auth.CachedOrganization{Roles: []string{"organization_owner"}},
			want: "owner",
		},
		{
			name: "member",
			org:  auth.CachedOrganization{Roles: []string{"organization_member"}},
			want: "member",
		},
		{
			name: "no roles defaults to member",
			org:  auth.CachedOrganization{},
			want: "member",
		},
		{
			name: "owner among multiple roles",
			org:  auth.CachedOrganization{Roles: []string{"organization_member", "organization_owner"}},
			want: "owner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, orgresolve.Role(tt.org))
		})
	}
}

func TestCurrentSlug(t *testing.T) {
	orgs := []auth.CachedOrganization{
		{ID: "id-a", Name: "Org A", Slug: "org-a"},
		{ID: "id-b", Name: "Org B", Slug: "org-b"},
	}

	tests := []struct {
		name          string
		cfgOrg        string
		selectedOrgID string
		wantSlug      string
		wantSource    orgresolve.Source
	}{
		{
			name:          "flag takes priority over selectedOrgID",
			cfgOrg:        "org-a",
			selectedOrgID: "id-b",
			wantSlug:      "org-a",
			wantSource:    orgresolve.SourceFlag,
		},
		{
			name:          "selectedOrgID used when no flag",
			selectedOrgID: "id-b",
			wantSlug:      "org-b",
			wantSource:    orgresolve.SourceGlobal,
		},
		{
			name:       "unknown flag returns none",
			cfgOrg:     "unknown-org",
			wantSlug:   "",
			wantSource: orgresolve.SourceNone,
		},
		{
			name:       "empty selection returns none",
			wantSlug:   "",
			wantSource: orgresolve.SourceNone,
		},
		{
			name:          "stale selectedOrgID not in list returns none",
			selectedOrgID: "id-unknown",
			wantSlug:      "",
			wantSource:    orgresolve.SourceNone,
		},
		{
			name:          "flag matching by ID",
			cfgOrg:        "id-a",
			selectedOrgID: "id-b",
			wantSlug:      "id-a",
			wantSource:    orgresolve.SourceFlag,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Org: tt.cfgOrg}
			slug, _, source := orgresolve.CurrentSlug(cfg, orgs, tt.selectedOrgID)
			assert.Equal(t, tt.wantSlug, slug)
			assert.Equal(t, tt.wantSource, source)
		})
	}
}
