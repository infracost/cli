package config

import (
	"os"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/environment"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins"
)

var (
	_ process.Processor = (*Config)(nil)
)

// Config contains the configuration for the CLI.
type Config struct {
	// Environment is the environment to target for operations / authentication (development or production). Defaults to
	// production.
	Environment environment.Environment `flag:"environment;hidden" usage:"The environment to use for authentication" default:"prod"`

	// Currency is the currency to use for prices. Defaults to USD.
	Currency string `env:"INFRACOST_CLI_CURRENCY" flag:"currency" usage:"The currency to use for prices" default:""`

	// PricingEndpoint is the endpoint to use for prices. Defaults to https://pricing.api.infracost.io.
	PricingEndpoint string `env:"INFRACOST_CLI_PRICING_ENDPOINT" flag:"pricing-endpoint;hidden" usage:"The pricing endpoint to use for prices" default:"https://pricing.api.infracost.io"`

	// Org is the organization slug or ID to use. Resolved to an ID before API calls.
	Org string `env:"INFRACOST_ORG" flag:"org" usage:"The organization slug or ID to use"`

	// OrgID is the resolved organization ID, set after resolving --org or from RunParameters.
	OrgID string

	// ClaudePath is the path to the Claude CLI binary. Defaults to "claude" (looked up on PATH).
	ClaudePath string `env:"INFRACOST_CLI_CLAUDE_PATH" flag:"claude-path;hidden" usage:"Path to the Claude CLI binary"`

	// Logging contains the configuration for logging.
	// keep logging above other structs, so it gets processed first and others can log in their process functions.
	Logging logging.Config

	// Dashboard contains the configuration for the dashboard API.
	Dashboard dashboard.Config

	// Events contains the configuration for the events API.
	Events events.Config

	// Auth contains the configuration for authenticating with Infracost.
	Auth auth.Config

	// Plugins contains the configuration for plugins.
	Plugins plugins.Config

	// Cache contains the configuration for the cache.
	Cache cache.Config
}

func (config *Config) Process() {
	events.RegisterMetadata("cloudEnabled", os.Getenv("INFRACOST_ENABLE_CLOUD") == "true")
	events.RegisterMetadata("dashboardEnabled", os.Getenv("INFRACOST_ENABLE_DASHBOARD") == "true")
	events.RegisterMetadata("environment", config.Environment.String())
	events.RegisterMetadata("isDefaultPricingApiEndpoint", config.PricingEndpoint == "https://pricing.api.infracost.io")
}
