package cache

import (
	"time"
)

type Config struct {
	// Cache is where the per-scan result files live (<key>.json plus
	// manifest.json). Defaults to [ResultsDir]; override with
	// INFRACOST_CLI_CACHE_DIRECTORY when you want results written
	// somewhere other than the canonical infracost cache root.
	Cache string `env:"INFRACOST_CLI_CACHE_DIRECTORY"`

	// TTL is how long cached results remain valid.
	TTL time.Duration `env:"INFRACOST_CLI_CACHE_TTL" default:"1h"`

	// manifest is the in-memory manifest, lazily loaded on first access.
	manifest *Manifest
}

func (c *Config) Process() {
	if len(c.Cache) == 0 {
		c.Cache = ResultsDir()
	}
	if c.TTL == 0 {
		c.TTL = time.Hour
	}
}
