package config

import (
	"os"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/logging"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/environment"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Config contains the configuration for the CLI.
type Config struct {
	// Environment is the environment to target for operations / authentication (development or production). Defaults to
	// production.
	Environment environment.Environment `flag:"environment;hidden" usage:"The environment to use for authentication" default:"prod"`

	// Currency is the currency to use for prices. Defaults to USD.
	Currency string `env:"INFRACOST_CLI_CURRENCY" flag:"currency" usage:"The currency to use for prices" default:"USD"`

	// PricingEndpoint is the endpoint to use for prices. Defaults to https://pricing.api.infracost.io.
	PricingEndpoint string `env:"INFRACOST_CLI_PRICING_ENDPOINT" flag:"pricing-endpoint;hidden" usage:"The pricing endpoint to use for prices" default:"https://pricing.api.infracost.io"`

	// OrgID is the organization ID to use for authentication. Defaults to the value of the INFRACOST_ORG_ID environment variable.
	OrgID string `env:"INFRACOST_CLI_ORG_ID" flag:"org-id;hidden" usage:"The organization ID to use for authentication"`

	// ClaudePath is the path to the Claude CLI binary. Defaults to "claude" (looked up on PATH).
	ClaudePath string `env:"INFRACOST_CLI_CLAUDE_PATH" flag:"claude-path;hidden" usage:"Path to the Claude CLI binary"`

	// Dashboard contains the configuration for the dashboard API.
	Dashboard dashboard.Config

	// Events contains the configuration for the events API.
	Events events.Config

	// Auth contains the configuration for authenticating with Infracost.
	Auth auth.Config

	// Logging contains the configuration for logging.
	Logging logging.Config

	// Plugins contains the configuration for plugins.
	Plugins plugins.Config

	// Cache contains the configuration for the cache.
	Cache cache.Config
}

func (config *Config) RegisterEventMetadata(cmd *cobra.Command) {
	events.RegisterMetadata("command", cmd.Name())
	events.RegisterMetadata("flags", func() []string {
		var flags []string
		cmd.Flags().Visit(func(flag *pflag.Flag) {
			flags = append(flags, flag.Name)
		})
		return flags
	}())
	events.RegisterMetadata("session", config.Cache.SessionID)
	events.RegisterMetadata("cloudEnabled", os.Getenv("INFRACOST_ENABLE_CLOUD") == "true")
	events.RegisterMetadata("dashboardEnabled", os.Getenv("INFRACOST_ENABLE_DASHBOARD") == "true")
	events.RegisterMetadata("environment", string(config.Environment))
	events.RegisterMetadata("isDefaultPricingApiEndpoint", config.PricingEndpoint == "https://pricing.api.infracost.io")
}
