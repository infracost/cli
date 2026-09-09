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

// TestApplyActiveOrg_AgentsEnabled pins the active org's flag as the only gate
// on Agents. There is deliberately no user-level override: it would let a user
// bypass an org that has AI features switched off.
func TestApplyActiveOrg_AgentsEnabled(t *testing.T) {
	tests := []struct {
		name       string
		orgEnabled bool
	}{
		{"org disabled", false},
		{"org enabled", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			org := auth.CachedOrganization{ID: "org-1", Slug: "acme", AgentsEnabled: tt.orgEnabled}

			applyActiveOrg(cfg, org)

			assert.Equal(t, tt.orgEnabled, cfg.AgentsEnabled)
			assert.Equal(t, "org-1", cfg.OrgID)
			assert.Equal(t, "acme", cfg.OrgSlug)
		})
	}
}

// TestApplyActiveOrgByID exercises the by-ID lookup, including that switching
// orgs re-resolves the gate rather than latching the first org's answer.
func TestApplyActiveOrgByID(t *testing.T) {
	orgs := []auth.CachedOrganization{
		{ID: "org-a", Slug: "org-a", AgentsEnabled: false},
		{ID: "org-b", Slug: "org-b", AgentsEnabled: true},
	}

	t.Run("tracks the active org's flag across switches", func(t *testing.T) {
		cfg := &config.Config{}
		uc := &auth.UserCache{Organizations: orgs}

		applyActiveOrgByID(cfg, uc, "org-a")
		assert.False(t, cfg.AgentsEnabled, "org-a has Agents off")

		applyActiveOrgByID(cfg, uc, "org-b")
		assert.True(t, cfg.AgentsEnabled, "org-b has Agents on")

		// Switching back must close the gate again, not keep org-b's answer.
		applyActiveOrgByID(cfg, uc, "org-a")
		assert.False(t, cfg.AgentsEnabled)
	})

	t.Run("unknown id sets OrgID defensively", func(t *testing.T) {
		cfg := &config.Config{}
		uc := &auth.UserCache{Organizations: orgs}

		applyActiveOrgByID(cfg, uc, "missing")

		assert.Equal(t, "missing", cfg.OrgID)
	})
}

// TestCacheUser_PersistsAgentsEnabled guards the wire from the dashboard
// currentUser response through to the cached user: without copying each org's
// AgentsEnabled into the cache the whole gate is a no-op.
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
		ID:    "user-1",
		Name:  "Alice",
		Email: "alice@acme.io",
		Organizations: []dashboard.Organization{
			{ID: "org-a", Name: "Org A", Slug: "org-a", AgentsEnabled: false},
			{ID: "org-b", Name: "Org B", Slug: "org-b", AgentsEnabled: true},
		},
	}

	uc := cacheUser(cfg, user)
	require.NotNil(t, uc)
	require.Len(t, uc.Organizations, 2)
	assert.False(t, uc.Organizations[0].AgentsEnabled)
	assert.True(t, uc.Organizations[1].AgentsEnabled)

	// It should also round-trip through the on-disk cache so later CLI
	// invocations (which reload rather than re-fetch) keep the entitlement.
	loaded, err := cfg.Auth.LoadUserCache()
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Len(t, loaded.Organizations, 2)
	assert.False(t, loaded.Organizations[0].AgentsEnabled)
	assert.True(t, loaded.Organizations[1].AgentsEnabled)
}
