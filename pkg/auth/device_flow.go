package auth

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth/browser"
	"golang.org/x/oauth2"
)

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
		return nil, nil, err
	}
	return config.TokenSource(ctx, token), token, nil
}

// StartDeviceFlow is used by the LSP which needs to start the device flow but cannot block waiting for the token. The caller is expected to call PollDeviceFlow
// The LSP will call PollDeviceFlow in a loop until it returns a token source, at which point the LSP can use the token source for authenticated requests. If PollDeviceFlow returns an error, the LSP should log the error and stop polling.
func (c *Config) StartDeviceFlow(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	return c.OAuth2Config().DeviceAuth(ctx, oauth2.SetAuthURLParam("audience", c.Audience))
}

// PollDeviceFlow is used by the LSP to poll for the token after StartDeviceFlow has been called. If the user has not completed the device flow, this function will return nil, nil, nil. If the user has completed the device flow and a token is available, this function will return a token source that can be used for authenticated requests. If there is an error during polling (e.g. network error), this function will return an error.
func (c *Config) PollDeviceFlow(ctx context.Context, resp *oauth2.DeviceAuthResponse) (oauth2.TokenSource, error) {
	token, err := c.OAuth2Config().DeviceAccessToken(ctx, resp)
	if err != nil {
		return nil, fmt.Errorf("device flow token exchange: %w", err)
	}

	if err := c.SaveCache(token); err != nil {
		return nil, fmt.Errorf("saving token: %w", err)
	}

	return c.wrapWithCache(c.OAuth2Config().TokenSource(ctx, token), token), nil
}
