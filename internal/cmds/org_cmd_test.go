package cmds

import (
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/stretchr/testify/assert"
)

func TestOrgRole(t *testing.T) {
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
			assert.Equal(t, tt.want, orgRole(tt.org))
		})
	}
}

func TestCurrentOrgSlug(t *testing.T) {
	orgs := []auth.CachedOrganization{
		{ID: "id-a", Name: "Org A", Slug: "org-a"},
		{ID: "id-b", Name: "Org B", Slug: "org-b"},
	}

	tests := []struct {
		name          string
		cfgOrg        string
		selectedOrgID string
		wantSlug      string
		wantSource    orgSource
	}{
		{
			name:          "flag takes priority over selectedOrgID",
			cfgOrg:        "org-a",
			selectedOrgID: "id-b",
			wantSlug:      "org-a",
			wantSource:    orgSourceFlag,
		},
		{
			name:          "selectedOrgID used when no flag",
			selectedOrgID: "id-b",
			wantSlug:      "org-b",
			wantSource:    orgSourceGlobal,
		},
		{
			name:       "unknown flag returns none",
			cfgOrg:     "unknown-org",
			wantSlug:   "",
			wantSource: orgSourceNone,
		},
		{
			name:       "empty selection returns none",
			wantSlug:   "",
			wantSource: orgSourceNone,
		},
		{
			name:          "stale selectedOrgID not in list returns none",
			selectedOrgID: "id-unknown",
			wantSlug:      "",
			wantSource:    orgSourceNone,
		},
		{
			name:          "flag matching by ID",
			cfgOrg:        "id-a",
			selectedOrgID: "id-b",
			wantSlug:      "id-a",
			wantSource:    orgSourceFlag,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Org: tt.cfgOrg}
			slug, _, source := currentOrgSlug(cfg, orgs, tt.selectedOrgID)
			assert.Equal(t, tt.wantSlug, slug)
			assert.Equal(t, tt.wantSource, source)
		})
	}
}
