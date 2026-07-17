package cmds

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
)

// TestApplyActiveOrg_AgentsEnabled covers the FIX-416 gate: Agents is enabled
// when the coast-access entitlement is set on either the user directly or the
// active org. A user-level grant is always-on regardless of the active org.
func TestApplyActiveOrg_AgentsEnabled(t *testing.T) {
	tests := []struct {
		name        string
		userEnabled bool
		orgEnabled  bool
		want        bool
	}{
		{"neither user nor org enabled", false, false, false},
		{"org enabled only", false, true, true},
		{"user enabled only is always-on", true, false, true},
		{"both enabled", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			uc := &auth.UserCache{AgentsEnabled: tt.userEnabled}
			org := auth.CachedOrganization{ID: "org-1", Slug: "acme", AgentsEnabled: tt.orgEnabled}

			applyActiveOrg(cfg, uc, org)

			assert.Equal(t, tt.want, cfg.AgentsEnabled)
			assert.Equal(t, "org-1", cfg.OrgID)
			assert.Equal(t, "acme", cfg.OrgSlug)
		})
	}
}

// TestApplyActiveOrgByID exercises the by-ID lookup, including the multi-org
// case at the heart of FIX-416: a user with user-level coast-access whose
// active org doesn't have it should still pass the gate.
func TestApplyActiveOrgByID(t *testing.T) {
	orgs := []auth.CachedOrganization{
		{ID: "org-a", Slug: "org-a", AgentsEnabled: false},
		{ID: "org-b", Slug: "org-b", AgentsEnabled: true},
	}

	t.Run("user-level flag enables gate even when active org is off", func(t *testing.T) {
		cfg := &config.Config{}
		uc := &auth.UserCache{AgentsEnabled: true, Organizations: orgs}

		applyActiveOrgByID(cfg, uc, "org-a")

		assert.Equal(t, "org-a", cfg.OrgID)
		assert.True(t, cfg.AgentsEnabled)
	})

	t.Run("falls back to the active org flag when user-level is off", func(t *testing.T) {
		cfg := &config.Config{}
		uc := &auth.UserCache{AgentsEnabled: false, Organizations: orgs}

		applyActiveOrgByID(cfg, uc, "org-a")
		assert.False(t, cfg.AgentsEnabled, "org-a has Agents off")

		// Switching to an org that has it on flips the gate.
		applyActiveOrgByID(cfg, uc, "org-b")
		assert.True(t, cfg.AgentsEnabled, "org-b has Agents on")
	})

	t.Run("unknown id sets OrgID defensively", func(t *testing.T) {
		cfg := &config.Config{}
		uc := &auth.UserCache{AgentsEnabled: false, Organizations: orgs}

		applyActiveOrgByID(cfg, uc, "missing")

		assert.Equal(t, "missing", cfg.OrgID)
	})
}

// TestCacheUser_PersistsAgentsEnabled guards the wire from the dashboard
// currentUser response through to the cached user: without copying
// user.AgentsEnabled into the UserCache the whole user-level gate is a no-op.
func TestCacheUser_PersistsAgentsEnabled(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Auth: auth.Config{
			InternalConfig: auth.InternalConfig{
				UserCachePath: filepath.Join(dir, "user.json"),
			},
		},
	}

	user := dashboard.CurrentUser{
		ID:            "user-1",
		Name:          "Alice",
		Email:         "alice@acme.io",
		AgentsEnabled: true,
		Organizations: []dashboard.Organization{
			{ID: "org-a", Name: "Org A", Slug: "org-a", AgentsEnabled: false},
			{ID: "org-b", Name: "Org B", Slug: "org-b", AgentsEnabled: true},
		},
	}

	uc := cacheUser(cfg, user)
	require.NotNil(t, uc)
	assert.True(t, uc.AgentsEnabled, "user-level agentsEnabled should be cached")
	require.Len(t, uc.Organizations, 2)
	assert.False(t, uc.Organizations[0].AgentsEnabled)
	assert.True(t, uc.Organizations[1].AgentsEnabled)

	// It should also round-trip through the on-disk cache so later CLI
	// invocations (which reload rather than re-fetch) keep the grant.
	loaded, err := cfg.Auth.LoadUserCache()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.True(t, loaded.AgentsEnabled)
}
