package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

// persistingTokenSource wraps an oauth2.TokenSource so that whenever the
// underlying source returns a token with a rotated refresh token or a new
// access token, the new token is written back to the cache file. Without
// this, in-process refreshes driven by oauth2.Transport leave the rotated
// refresh token in memory only, and the next CLI invocation reads the
// now-invalidated refresh token from disk and fails with invalid_grant.
type persistingTokenSource struct {
	src         oauth2.TokenSource
	cfg         *Config
	mu          sync.Mutex
	lastAccess  string
	lastRefresh string
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	rotated := tok.RefreshToken != "" && tok.RefreshToken != p.lastRefresh
	refreshed := tok.AccessToken != "" && tok.AccessToken != p.lastAccess
	if rotated || refreshed {
		if saveErr := p.cfg.SaveCache(tok); saveErr != nil {
			logging.WithError(saveErr).Msg("failed to persist refreshed token")
		} else {
			p.lastAccess = tok.AccessToken
			p.lastRefresh = tok.RefreshToken
		}
	}
	return tok, nil
}

func (c *Config) wrapWithCache(src oauth2.TokenSource, token *oauth2.Token) oauth2.TokenSource {
	p := &persistingTokenSource{src: src, cfg: c}
	if token != nil {
		p.lastAccess = token.AccessToken
		p.lastRefresh = token.RefreshToken
	}
	return p
}

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
		return nil, nil, fmt.Errorf("failed to open token cache at %s: %w (use INFRACOST_CLI_OAUTH_TOKEN_CACHE_PATH to change the location)", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	var token oauth2.Token
	if err := json.NewDecoder(f).Decode(&token); err != nil {
		return nil, nil, fmt.Errorf("failed to decode token cache at %s: %w (the file may be corrupted, try deleting it and running `infracost auth login` again)", path, err)
	}

	ts := c.wrapWithCache(c.OAuth2Config().TokenSource(ctx, &token), &token)
	newToken, err := ts.Token() // check if the token is valid, and refresh if not. The wrapper persists any rotated refresh token.
	if err != nil {
		logging.WithError(err).Msg("failed to refresh token")
		return nil, nil, nil
	}

	return ts, newToken, nil
}

func (c *Config) SaveCache(token *oauth2.Token) error {
	if !c.UseAccessTokenCache {
		return nil
	}

	path := os.ExpandEnv(c.TokenCachePath)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create token cache directory: %w (use INFRACOST_CLI_OAUTH_TOKEN_CACHE_PATH to change the location)", err)
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
