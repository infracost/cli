package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/pkg/logging"
)

// CachedOrganization is a minimal representation of an organization stored in the user cache.
type CachedOrganization struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Slug  string   `json:"slug"`
	Roles []string `json:"roles,omitempty"`
}

// UserCache stores the current user's identity and organization memberships.
// It is populated on login and used to resolve --org flag values without an API call.
type UserCache struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Email         string               `json:"email"`
	Organizations []CachedOrganization `json:"organizations"`
	SelectedOrgID string               `json:"selectedOrgId,omitempty"`
	UpdatedAt     time.Time            `json:"updatedAt"`
}

// userCacheTTL is how long the cached user data is considered fresh.
const userCacheTTL = 24 * time.Hour

// IsStale returns true if the cache is older than the TTL.
func (uc *UserCache) IsStale() bool {
	return time.Since(uc.UpdatedAt) > userCacheTTL
}

func (c *Config) LoadUserCache() (*UserCache, error) {
	path := os.ExpandEnv(c.UserCachePath)

	// nolint:gosec // G304: Path is derived from the user's own config directory.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening user cache: %w", err)
	}
	defer func() { _ = f.Close() }()

	var uc UserCache
	if err := json.NewDecoder(f).Decode(&uc); err != nil {
		return nil, fmt.Errorf("decoding user cache: %w", err)
	}

	return &uc, nil
}

func (c *Config) SaveUserCache(uc *UserCache) error {
	path := os.ExpandEnv(c.UserCachePath)

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating user cache directory: %w", err)
	}

	uc.UpdatedAt = time.Now()

	// nolint:gosec // G304: Path is derived from the user's own config directory.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("opening user cache file: %w", err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(uc); err != nil {
		return fmt.Errorf("encoding user cache: %w", err)
	}

	return nil
}

func (c *Config) ClearUserCache() error {
	path := os.ExpandEnv(c.UserCachePath)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clearing user cache: %w", err)
	}
	return nil
}

func defaultUserCachePath() string {
	dir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(dir, "infracost", "user.json")
	}
	logging.WithError(err).Msg("failed to load user config dir, falling back to home directory")

	dir, err = os.UserHomeDir()
	if err == nil {
		return filepath.Join(dir, ".infracost", "user.json")
	}

	logging.WithError(err).Msg("failed to load user home dir, falling back to current directory")
	return filepath.Join(".infracost", "user.json")
}
