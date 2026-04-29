package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/environment"
	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

var (
	_ process.Processor = (*Config)(nil)

	defaultValues = map[string]map[string]string{
		environment.Production: {
			"client_id":     "uMF6vrdl42thCHGI6Os6UqMqr2Px412O",
			"auth_endpoint": "login.infracost.io",
			"audience":      "https://dashboard.api.infracost.io",
		},
		environment.Development: {
			"client_id":     "tpEor2E0AXg7JntyL5pCYq9KNaFdmScL",
			"auth_endpoint": "login.dev.infracost.io",
			"audience":      "https://dashboard.api.dev.infracost.io",
		},
		environment.Local: {
			"client_id":     "tpEor2E0AXg7JntyL5pCYq9KNaFdmScL",
			"auth_endpoint": "login.dev.infracost.io",
			"audience":      "https://dashboard.api.dev.infracost.io",
		},
	}
)

// Config contains the configuration for authenticating with Infracost.
type Config struct {
	InternalConfig
	ExternalConfig

	Environment string `flagvalue:"environment"`

	source oauth2.TokenSource
}

// SetTokenSource sets the token source directly, bypassing the login flow.
// This is intended for testing.
func (c *Config) SetTokenSource(ts oauth2.TokenSource) {
	c.source = ts
}

// InternalConfig contains the configuration for authenticating with Auth0. End users should never be setting these
// directly, so the flags and environment variables are hidden. These are exposed as flags mainly to help with
// development and testing.
type InternalConfig struct {
	// ClientID should the Auth0 Application Client ID.
	ClientID string `env:"INFRACOST_CLI_OAUTH_CLIENT_ID" flag:"oauth-client-id;hidden" usage:"The client ID to use for authentication"`

	// AuthEndpoint should be the Auth0 domain.
	AuthEndpoint string `env:"INFRACOST_CLI_OAUTH_ENDPOINT" flag:"oauth-endpoint;hidden" usage:"The auth endpoint to use for authentication"`

	// CallbackPort is the port to listen on for the callback from Auth0.
	CallbackPort int `env:"INFRACOST_CLI_OAUTH_CALLBACK_PORT" flag:"oauth-callback-port;hidden" usage:"The callback port to use for authentication" default:"8080"`

	// Audience is the expected audience of the token (i.e., the Infracost API URL).
	Audience string `env:"INFRACOST_CLI_OAUTH_AUDIENCE" flag:"oauth-audience;hidden" usage:"The audience to use for authentication"`

	// TokenCachePath is the path to the token cache file.
	TokenCachePath string `env:"INFRACOST_CLI_OAUTH_TOKEN_CACHE_PATH" flag:"access-token-cache-path;hidden" usage:"The path to the token cache file"`

	// UserCachePath is the path to the user cache file.
	UserCachePath string `env:"INFRACOST_CLI_USER_CACHE_PATH" flag:"user-cache-path;hidden" usage:"The path to the user cache file"`
}

// ExternalConfig contains the configuration settings that end users should know about and can set.
type ExternalConfig struct {
	// AuthenticationToken is an optional authentication token to use for authentication instead of requiring the
	// user to authenticate with Auth0.
	AuthenticationToken AuthenticationToken `env:"INFRACOST_CLI_AUTHENTICATION_TOKEN"`

	// UseDeviceFlow indicates whether to use the device flow for authentication.
	UseDeviceFlow bool `env:"INFRACOST_CLI_OAUTH_USE_DEVICE_FLOW" flag:"oauth-use-device-flow" usage:"Use device flow for authentication instead of PKCE (useful when you don't have access to localhost)"`

	// UseAccessTokenCache indicates whether to use the token cache for authentication.
	UseAccessTokenCache bool `env:"INFRACOST_CLI_ACCESS_TOKEN_USE_CACHE" flag:"access-token-use-cache" default:"true" usage:"Read and save access tokens from a cache file (disable to force a fresh login)"`
}

func (c *Config) Process() {
	if c.ClientID == "" {
		c.ClientID = defaultValues[c.Environment]["client_id"]
	}

	if c.AuthEndpoint == "" {
		c.AuthEndpoint = defaultValues[c.Environment]["auth_endpoint"]
	}

	if c.Audience == "" {
		c.Audience = defaultValues[c.Environment]["audience"]
	}

	if len(c.TokenCachePath) == 0 {
		c.TokenCachePath = defaultTokenCachePath()
	}

	if len(c.UserCachePath) == 0 {
		c.UserCachePath = defaultUserCachePath()
	}
}

// OAuth2Config returns the OAuth2 config for authenticating with Auth0.
func (c *Config) OAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID: c.ClientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:       fmt.Sprintf("https://%s/authorize", c.AuthEndpoint),
			DeviceAuthURL: fmt.Sprintf("https://%s/oauth/device/code", c.AuthEndpoint),
			TokenURL:      fmt.Sprintf("https://%s/oauth/token", c.AuthEndpoint),
		},
		RedirectURL: fmt.Sprintf("http://localhost:%d/callback", c.CallbackPort),
		Scopes:      []string{"offline_access", "orgs:read", "policies:read", "prices:read", "runs:write"},
	}
}

// Token will attempt to retrieve an access token. If there is no cached token, or the cached token cannot be refreshed,
// then this function will prompt the user to log in.
func (c *Config) Token(ctx context.Context) (oauth2.TokenSource, error) {
	if c.source != nil {
		return c.source, nil
	}
	ts, err := c.login(ctx)
	if err != nil {
		return nil, err
	}
	c.source = ts
	return ts, nil
}

// TokenFromCache will also attempt to retrieve an access token, like Token. But, it will not attempt to log in if there
// is no token or it could not be refreshed and will instead return a nil token source.
//
// This function allows reuse of an earlier login attempt without risking the log in flow launching when the caller
// requires a non-interactive prompt (such as when logging errors).
func (c *Config) TokenFromCache(ctx context.Context) oauth2.TokenSource {
	if c.source != nil {
		return c.source
	}

	ts, token, err := c.LoadCache(ctx)
	switch {
	case err != nil:
		logging.WithError(err).Msg("failed to load cached token")
		return nil
	case token != nil:
		if _, err := c.validateToken(token); err != nil {
			logging.WithError(err).Msg("cached token is invalid")
			return nil
		}
	}

	c.source = ts
	return ts
}

// login performs the authentication flow (PKCE or Device Flow) and saves the token to the cache.
func isInteractive() bool {
	fileInfo, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fileInfo.Mode() & os.ModeCharDevice) != 0
}

// Login performs the authentication flow (PKCE or Device Flow) and saves the token to the cache.
// It first tries to load the token from the cache and validate it.
func (c *Config) login(ctx context.Context) (oauth2.TokenSource, error) {
	if len(c.AuthenticationToken) > 0 {
		return c.AuthenticationToken, nil
	}

	ts, token, err := c.LoadCache(ctx)
	switch {
	case err != nil:
		logging.WithError(err).Msg("failed to load cached token")
		logging.Error("logging in again...")
	case token != nil:
		if _, err := c.validateToken(token); err != nil {
			logging.WithError(err).Msg("cached token is invalid")
			logging.Error("logging in again...")
		} else {
			logging.Info("reusing cached token")
			return ts, nil
		}
	default:
		logging.Info("no cached token found, logging in...")
	}

	// abort if not in interactive tty
	if !isInteractive() {
		caller, _ := events.GetMetadata[string]("caller")
		switch {
		case caller != "":
			return nil, fmt.Errorf("not logged in — run 'infracost auth login' in your terminal first, then retry")
		default:
			return nil, fmt.Errorf("not logged in — set INFRACOST_CLI_AUTHENTICATION_TOKEN for non-interactive environments, or run 'infracost auth login' in an interactive terminal first")
		}
	}

	login := c.PKCE
	if c.UseDeviceFlow {
		login = c.DeviceFlow
	}

	ts, token, err = login(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := c.validateToken(token); err != nil {
		return nil, err
	}

	if err := c.SaveCache(token); err != nil {
		return nil, err
	}

	return ts, nil
}

func (c *Config) validateToken(token *oauth2.Token) (string, error) {
	if token == nil {
		return "", fmt.Errorf("no token provided")
	}

	jwksURL := fmt.Sprintf("https://%s/.well-known/jwks.json", c.AuthEndpoint)
	k, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return "", fmt.Errorf("failed to create keyfunc: %w", err)
	}

	var claims struct {
		jwt.RegisteredClaims
	}

	parsedToken, err := jwt.ParseWithClaims(token.AccessToken, &claims, k.Keyfunc)
	if err != nil {
		return "", fmt.Errorf("error parsing token: %w", err)
	}

	if !parsedToken.Valid {
		return "", fmt.Errorf("invalid token")
	}

	// Auth0 doesn't embed the email in the token (even if you request it in the scopes, grrrrr...)
	// We'll return the subject for now, so we have something to print. We can add another action to Auth0
	// that can embed the email in the claims if we actually need this.

	if claims.Subject == "" {
		return "", fmt.Errorf("no subject found in token")
	}

	return claims.Subject, nil
}

func defaultTokenCachePath() string {
	dir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(dir, "infracost", "token.json")
	}
	logging.WithError(err).Msg("failed to load user config dir, falling back to home directory")

	dir, err = os.UserHomeDir()
	if err == nil {
		return filepath.Join(dir, ".infracost", "token.json")
	}

	logging.WithError(err).Msg("tokenCachePath: failed to load user home dir, falling back to current directory")
	return filepath.Join(".infracost", "token.json")
}
