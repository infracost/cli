package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

func (c *Config) LoadCache(ctx context.Context) (oauth2.TokenSource, *oauth2.Token, error) {
	if !c.UseAccessTokenCache {
		return nil, nil, nil
	}

	path := os.ExpandEnv(c.TokenCachePath)

	// nolint:gosec // G304: Users can choose where they cache their own token.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("failed to open token cache: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var token oauth2.Token
	if err := json.NewDecoder(f).Decode(&token); err != nil {
		return nil, nil, fmt.Errorf("failed to decode token cache: %w", err)
	}

	ts := c.OAuth2Config().TokenSource(ctx, &token)
	newToken, err := ts.Token() // check if the token is valid, and refresh if not.
	if err != nil {
		logging.WithError(err).Msg("failed to refresh token")
		return nil, nil, nil
	}

	if newToken.AccessToken != token.AccessToken {
		logging.Info("updating cached token")
		if err := c.SaveCache(newToken); err != nil {
			return nil, nil, fmt.Errorf("failed to save refreshed token: %w", err)
		}
	}

	return ts, newToken, nil
}

func (c *Config) SaveCache(token *oauth2.Token) error {
	if !c.UseAccessTokenCache {
		return nil
	}

	path := os.ExpandEnv(c.TokenCachePath)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create token cache directory: %w", err)
	}

	// nolint:gosec // G304: Users can choose where they cache their own token.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open token cache: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	//nolint:gosec // G117: Marshaling a token to JSON is not a security risk as the token is already stored in plaintext on disk.
	if err := json.NewEncoder(f).Encode(token); err != nil {
		return fmt.Errorf("failed to encode token cache: %w", err)
	}

	return nil
}

func (c *Config) ClearCache() error {
	path := os.ExpandEnv(c.TokenCachePath)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear token cache: %w", err)
	}
	return nil
}
