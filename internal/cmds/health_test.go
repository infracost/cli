package cmds_test

import (
	"bytes"
	"context"
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
	"golang.org/x/oauth2"
)

func healthTestConfig(t *testing.T, mockClient *mocks.MockClient) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Dashboard: dashboard.Config{
			Client: func(_ *http.Client) dashboard.Client {
				return mockClient
			},
		},
	}
	cfg.Auth.SetTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	return cfg
}

func TestHealth_AllPass(t *testing.T) {
	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(dashboard.CurrentUser{
			ID:    "user-1",
			Name:  "Alice",
			Email: "alice@example.com",
			Organizations: []dashboard.Organization{
				{ID: "org-1", Name: "Acme Corp", Slug: "acme"},
			},
		}, nil)

	cfg := healthTestConfig(t, mockClient)
	cmd := cmds.Health(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := cmd.Execute()

	// Version check may warn (running "dev"), but auth and config should pass.
	// We don't assert no error since the version check hitting GitHub may fail in CI.
	out := buf.String()
	assert.Contains(t, out, "Infracost Health")
	assert.Contains(t, out, "✓ Credentials found")
	assert.Contains(t, out, "✓ Token valid")
	assert.Contains(t, out, `"Acme Corp"`)
	assert.Contains(t, out, "✓ API reachable")
	assert.Contains(t, out, "✓ Config file valid")

	_ = err // version check may cause a warning or failure depending on network
}

func TestHealth_NoCredentials(t *testing.T) {
	mockClient := mocks.NewMockClient(t)
	cfg := &config.Config{
		Dashboard: dashboard.Config{
			Client: func(_ *http.Client) dashboard.Client {
				return mockClient
			},
		},
	}
	// No token source set, no AuthenticationToken — no credentials.

	cmd := cmds.Health(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := cmd.Execute()

	require.Error(t, err)
	out := buf.String()
	assert.Contains(t, out, "✗ No credentials found")
	assert.Contains(t, out, "⊘ Token valid")
	assert.Contains(t, out, "⊘ Organization accessible")
	assert.Contains(t, out, "⊘ API reachable")
}

func TestHealth_AuthenticationToken(t *testing.T) {
	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(dashboard.CurrentUser{
			ID:    "user-1",
			Name:  "Bob",
			Email: "bob@example.com",
			Organizations: []dashboard.Organization{
				{ID: "org-1", Name: "Beta Inc", Slug: "beta"},
			},
		}, nil)

	cfg := &config.Config{
		Auth: auth.Config{
			ExternalConfig: auth.ExternalConfig{
				AuthenticationToken: "service-token",
			},
		},
		Dashboard: dashboard.Config{
			Client: func(_ *http.Client) dashboard.Client {
				return mockClient
			},
		},
	}

	cmd := cmds.Health(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	_ = cmd.Execute()

	out := buf.String()
	assert.Contains(t, out, "✓ Credentials found")
	assert.Contains(t, out, `"Beta Inc"`)
}

func TestHealth_APIError(t *testing.T) {
	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(dashboard.CurrentUser{}, assert.AnError)

	cfg := healthTestConfig(t, mockClient)
	cmd := cmds.Health(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	err := cmd.Execute()

	require.Error(t, err)
	out := buf.String()
	assert.Contains(t, out, "✓ Credentials found")
	assert.Contains(t, out, "✗ Organization not accessible")
	assert.Contains(t, out, "⊘ API reachable")
}
