package cache

import (
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/logging"
)

type Config struct {
	// Cache is where the cache files should go.
	Cache string `env:"INFRACOST_CLI_CACHE_DIRECTORY"`

	// TTL is how long cached results remain valid.
	TTL time.Duration `env:"INFRACOST_CLI_CACHE_TTL" default:"1h"`

	// SessionID is an explicit session ID set by the caller to group runs.
	// When set, lookups by session skip TTL and staleness checks.
	SessionID string `env:"INFRACOST_SESSION_ID"`

	// TermSessionID is the terminal session ID, typically set by the terminal
	// emulator (e.g. TERM_SESSION_ID). Used as a fallback session identifier
	// when SessionID is not set. Lookups by terminal session still check TTL.
	//
	// Like explicit sessions, terminal session lookups ignore the path. This is
	// important because some commands (e.g. price) use temporary directories, so
	// the path changes every invocation and path+TTL alone cannot track iterative
	// changes.
	TermSessionID string `env:"TERM_SESSION_ID"`

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

	if c.SessionID != "" {
		logging.Infof("using explicit session id %s", c.SessionID)
	} else if c.TermSessionID != "" {
		logging.Infof("using terminal session id %s", c.TermSessionID)
	}
	sid, _ := c.sessionID()
	events.RegisterMetadata("session", sid)
}

// sessionID returns the effective session ID and whether it is an explicit
// (trusted) session. Explicit sessions skip TTL and staleness checks.
// Terminal sessions are used for matching but still respect TTL.
func (c *Config) sessionID() (string, bool) {
	if c.SessionID != "" {
		return c.SessionID, true
	}
	return c.TermSessionID, false
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
