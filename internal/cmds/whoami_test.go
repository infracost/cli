package cmds_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
)

func testConfig(t *testing.T, mockClient *mocks.MockClient) *config.Config {
	t.Helper()

	return &config.Config{
		Auth: auth.Config{
			ExternalConfig: auth.ExternalConfig{
				AuthenticationToken: "test-token",
			},
		},
		Dashboard: dashboard.Config{
			Client: func(_ *http.Client) dashboard.Client {
				return mockClient
			},
		},
	}
}

func TestWhoAmI(t *testing.T) {
	tests := []struct {
		name       string
		user       dashboard.CurrentUser
		err        error
		wantErrMsg string
	}{
		{
			name: "single org owner",
			user: dashboard.CurrentUser{
				ID:    "user-1",
				Name:  "Alice Smith",
				Email: "alice@example.com",
				Organizations: []dashboard.Organization{
					{
						ID:   "org-1",
						Name: "Acme Corp",
						Slug: "acme",
						Roles: []dashboard.Role{
							{ID: "organization_owner"},
						},
					},
				},
			},
		},
		{
			name: "multiple orgs with mixed roles",
			user: dashboard.CurrentUser{
				ID:    "user-2",
				Name:  "Bob Jones",
				Email: "bob@example.com",
				Organizations: []dashboard.Organization{
					{
						ID:   "org-1",
						Name: "Acme Corp",
						Slug: "acme",
						Roles: []dashboard.Role{
							{ID: "organization_owner"},
						},
					},
					{
						ID:    "org-2",
						Name:  "Beta Inc",
						Slug:  "beta",
						Roles: []dashboard.Role{},
					},
				},
			},
		},
		{
			name: "no organizations",
			user: dashboard.CurrentUser{
				ID:    "user-3",
				Name:  "Carol White",
				Email: "carol@example.com",
			},
		},
		{
			name:       "api error",
			err:        errors.New("unauthorized"),
			wantErrMsg: "fetching current user: unauthorized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := mocks.NewMockClient(t)
			mockClient.EXPECT().
				CurrentUser(mock.Anything).
				Return(tt.user, tt.err)

			cfg := testConfig(t, mockClient)
			cmd := cmds.WhoAmI(cfg)
			cmd.SetContext(context.Background())

			err := cmd.Execute()
			if tt.wantErrMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrMsg)
				return
			}

			require.NoError(t, err)
		})
	}
}
