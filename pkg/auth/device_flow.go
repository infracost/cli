package auth

import (
	"context"
	"fmt"
	"sync"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth/browser"
	"golang.org/x/oauth2"
)

var deviceFlowVerifiers sync.Map

func (c *Config) DeviceFlow(ctx context.Context) (oauth2.TokenSource, *oauth2.Token, error) {
	caller, _ := events.GetMetadata[string]("caller")

	config := c.OAuth2Config()
	verifier := oauth2.GenerateVerifier()

	response, err := config.DeviceAuth(ctx, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("audience", c.Audience))
	if err != nil {
		return nil, nil, err
	}

	fmt.Printf("Please go to the following URL to log in:\n%s\n", ui.Code(response.VerificationURI))
	fmt.Printf("And enter the code:\n%s\n", ui.Code(response.UserCode))

	browserCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	browser.WaitAndOpen(browserCtx, response.VerificationURIComplete, len(caller) > 0)

	token, err := config.DeviceAccessToken(ctx, response, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, nil, loginDeniedFromOAuthError(err)
	}
	return config.TokenSource(ctx, token), token, nil
}

// StartDeviceFlow starts a device auth flow for non-blocking clients (for
// example the LSP) and stores the PKCE verifier needed by PollDeviceFlow.
func (c *Config) StartDeviceFlow(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	config := c.OAuth2Config()
	verifier := oauth2.GenerateVerifier()
	response, err := config.DeviceAuth(ctx, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("audience", c.Audience))
	if err != nil {
		return nil, err
	}
	deviceFlowVerifiers.Store(response.DeviceCode, verifier)
	return response, nil
}

// PollDeviceFlow waits for the user to complete a device auth flow started by
// StartDeviceFlow, saves the token, and returns a token source that persists
// refreshed tokens back to the cache.
func (c *Config) PollDeviceFlow(ctx context.Context, resp *oauth2.DeviceAuthResponse) (oauth2.TokenSource, error) {
	var opts []oauth2.AuthCodeOption
	if verifier, ok := deviceFlowVerifiers.LoadAndDelete(resp.DeviceCode); ok {
		opts = append(opts, oauth2.VerifierOption(verifier.(string)))
	}

	token, err := c.OAuth2Config().DeviceAccessToken(ctx, resp, opts...)
	if err != nil {
		if denied := loginDeniedFromOAuthError(err); denied != err {
			return nil, denied
		}
		return nil, fmt.Errorf("device flow token exchange: %w", err)
	}

	if err := c.SaveCache(token); err != nil {
		return nil, fmt.Errorf("saving token: %w", err)
	}

	return c.wrapWithCache(c.OAuth2Config().TokenSource(ctx, token), token), nil
}
