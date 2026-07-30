package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisabledAuth(t *testing.T) {
	// Self-hosted pricing mode disables all Infracost Cloud authentication:
	// Token must fail with an actionable error instead of starting a login
	// flow, and TokenFromCache must return nil without validating any cached
	// token (validation fetches JWKS over the network).
	cfg := &Config{Disabled: true}

	_, err := cfg.Token(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "INFRACOST_CLI_PRICING_API_KEY")

	require.Nil(t, cfg.TokenFromCache(context.Background()))
}

func TestDisabledAuthWinsOverAuthenticationToken(t *testing.T) {
	// Disabled short-circuits even an explicitly configured static
	// authentication token — in self-hosted pricing mode nothing should be
	// sent to Infracost Cloud at all.
	cfg := &Config{
		Disabled:       true,
		ExternalConfig: ExternalConfig{AuthenticationToken: "static-token"},
	}

	_, err := cfg.Token(context.Background())
	require.Error(t, err)
}
