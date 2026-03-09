package cache

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/logging"
	"github.com/shirou/gopsutil/process"
)

type Config struct {
	// Cache is where the cache files should go.
	Cache string `env:"INFRACOST_CLI_CACHE_DIRECTORY"`

	// TTL is how long cached results remain valid.
	TTL time.Duration `env:"INFRACOST_CLI_CACHE_TTL" default:"1h"`

	// SessionID identifies the current session (terminal, editor, CI job).
	// Falls back to the parent process ID if not set.
	SessionID string `env:"INFRACOST_SESSION_ID"`
}

func (c *Config) ApplyDefaults() {
	if len(c.Cache) == 0 {
		c.Cache = defaultCachePath()
	}
	if c.TTL == 0 {
		c.TTL = time.Hour
	}
	if c.SessionID == "" {
		c.SessionID = getSessionID()
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

func getSessionID() string {
	ppid := os.Getppid()
	if ppid > math.MaxInt32 {
		return fmt.Sprintf("%d", ppid)
	}

	process, err := process.NewProcess(int32(ppid)) //nolint:gosec // guarded by MaxInt32 check above
	if err != nil {
		return fmt.Sprintf("%d", ppid)
	}

	createTime, err := process.CreateTime()
	if err != nil {
		return fmt.Sprintf("%d", ppid)
	}

	return fmt.Sprintf("%d-%d", ppid, createTime)
}
