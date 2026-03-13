package cache

import (
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/logging"
)

type Config struct {
	// Cache is where the cache files should go.
	Cache string `env:"INFRACOST_CLI_CACHE_DIRECTORY"`

	// TTL is how long cached results remain valid.
	TTL time.Duration `env:"INFRACOST_CLI_CACHE_TTL" default:"1h"`

	// manifest is the in-memory manifest, lazily loaded on first access.
	manifest *Manifest
}

func (c *Config) Process() {
	if len(c.Cache) == 0 {
		c.Cache = defaultCachePath()
	}
	if c.TTL == 0 {
		c.TTL = time.Hour
	}
}

func defaultCachePath() string {
	dir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(dir, "infracost", "cache")
	}
	logging.WithError(err).Msg("failed to load user cache dir, falling back to home directory")

	dir, err = os.UserHomeDir()
	if err == nil {
		return filepath.Join(dir, ".infracost", "cache")
	}

	logging.WithError(err).Msg("failed to load user home dir, falling back to current directory")
	return filepath.Join(".infracost", "cache")
}
