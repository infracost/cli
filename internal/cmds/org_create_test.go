package cmds_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
)

// orgCreateTestConfig wires a config with a static auth token and a
// mockable dashboard client, plus temp paths so the test never touches
// the user's real cache.
func orgCreateTestConfig(t *testing.T, mockClient *mocks.MockClient) *config.Config {
	t.Helper()
	nonInteractiveStdin(t)

	dir := t.TempDir()
	cfg := &config.Config{
		Auth: auth.Config{
			InternalConfig: auth.InternalConfig{
				UserCachePath:  filepath.Join(dir, "user.json"),
				TokenCachePath: filepath.Join(dir, "token.json"),
			},
		},
		Dashboard: dashboard.Config{
			Client: func(_ *http.Client) dashboard.Client {
				return mockClient
			},
		},
	}
	cfg.Auth.SetTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	return cfg
}

func TestOrgCreate_NonInteractiveWithName(t *testing.T) {
	mockClient := mocks.NewMockClient(t)

	mockClient.EXPECT().
		CreateOrganization(mock.Anything, "Acme Corp").
		Return(dashboard.Organization{ID: "org-1", Name: "Acme Corp", Slug: "acme-corp"}, nil)

	// createOrgAndCache refreshes the user cache so subsequent runs see
	// the new org without re-fetching. Return it shaped like a fresh
	// CurrentUser response.
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(dashboard.CurrentUser{
			ID:    "user-1",
			Email: "alice@acme.io",
			Organizations: []dashboard.Organization{
				{ID: "org-1", Name: "Acme Corp", Slug: "acme-corp"},
			},
		}, nil)

	cfg := orgCreateTestConfig(t, mockClient)

	orgCmd := cmds.Org(cfg)
	orgCmd.SetArgs([]string{"create", "Acme Corp"})
	orgCmd.SetContext(context.Background())
	orgCmd.SilenceUsage = true
	orgCmd.SilenceErrors = true

	require.NoError(t, orgCmd.Execute())

	// The new org should be pinned as the selected one so the next
	// resolveOrg call doesn't prompt.
	uc, err := cfg.Auth.LoadUserCache()
	require.NoError(t, err)
	require.NotNil(t, uc)
	assert.Equal(t, "org-1", uc.SelectedOrgID)
	assert.Equal(t, "org-1", cfg.OrgID)
}

func TestOrgCreate_NonInteractiveWithoutName(t *testing.T) {
	mockClient := mocks.NewMockClient(t)
	cfg := orgCreateTestConfig(t, mockClient)

	orgCmd := cmds.Org(cfg)
	orgCmd.SetArgs([]string{"create"})
	orgCmd.SetContext(context.Background())
	orgCmd.SilenceUsage = true
	orgCmd.SilenceErrors = true

	err := orgCmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no organization name provided")
}