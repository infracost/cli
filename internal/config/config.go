package config

import (
	"fmt"
	"os"

	"github.com/infracost/cli/internal/api/agents"
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

// defaultPricingEndpoint is Infracost's SaaS pricing API — must match the
// `default` tag on Config.PricingEndpoint.
const defaultPricingEndpoint = "https://pricing.api.infracost.io"

// Config contains the configuration for the CLI.
type Config struct {
	// Environment is the environment to target for operations / authentication (development or production). Defaults to
	// production.
	Environment environment.Environment `flag:"environment;hidden" usage:"The environment to use for authentication" default:"prod"`

	// Currency is the currency to use for prices. Defaults to USD.
	Currency string `env:"INFRACOST_CLI_CURRENCY" default:""`

	// SSHKeyFile is a comma-separated list of SSH private key file(s) to attach
	// for fetching private modules over git-over-ssh. Empty scans the standard
	// ~/.ssh default key files. The registered --ssh-key-file flag overrides it.
	SSHKeyFile string `env:"INFRACOST_CLI_SSH_KEY_FILE" default:""`

	// PricingEndpoint is the endpoint to use for prices. Defaults to https://pricing.api.infracost.io.
	PricingEndpoint string `env:"INFRACOST_CLI_PRICING_ENDPOINT" flag:"pricing-endpoint;hidden" usage:"The pricing endpoint to use for prices" default:"https://pricing.api.infracost.io"`

	// PricingAPIKey is a static API key for a self-hosted Cloud Pricing API (the
	// value of its SELF_HOSTED_INFRACOST_API_KEY). Setting it switches the CLI
	// into self-hosted pricing mode: the key is sent to the pricing API instead
	// of an OAuth access token, and all other communication with Infracost Cloud
	// (login, dashboard, telemetry) is disabled — policies, guardrails, budgets,
	// usage defaults and config templates are skipped. Pair with
	// PricingEndpoint to point at the self-hosted instance. Deliberately
	// env-only (like Auth.AuthenticationToken): a flag would put the credential
	// in argv, visible to `ps` and shell history.
	PricingAPIKey string `env:"INFRACOST_CLI_PRICING_API_KEY"`

	// Org is the organization slug or ID to use. Resolved to an ID before API calls.
	Org string `env:"INFRACOST_CLI_ORG" flag:"org" usage:"The organization slug or ID to use"`

	// OrgID is the resolved organization ID, set after resolving --org or from RunParameters.
	OrgID string

	// OrgSlug is the resolved active organization's slug, set alongside OrgID
	// when the org is resolved from the user cache. Used for building org-
	// scoped dashboard links (e.g. the Agents waitlist).
	OrgSlug string

	// AgentsEnabled is the resolved Agents entitlement the Agents commands and
	// MCP tools gate on (see ensureAgentsEnabled). It is true when the coast-
	// access entitlement is set on either the user directly or the active org
	AgentsEnabled bool

	// ClaudePath is the path to the Claude CLI binary. Defaults to "claude" (looked up on PATH).
	ClaudePath string `env:"INFRACOST_CLI_CLAUDE_PATH" flag:"claude-path;hidden" usage:"Path to the Claude CLI binary"`

	// NoColor disables all colored output. NO_COLOR env var (any non-empty value)
	// is honored separately at startup; see internal/ui/colors.go.
	NoColor bool `flag:"no-color" usage:"Disable colored output"`

	// JSON toggles JSON output for both logs and command results. Shared with
	// any sub-config that registers `flagvalue:"json"` (e.g. logging).
	JSON process.BoolFlag `env:"INFRACOST_CLI_LOG_JSON" flag:"json" usage:"Output logs and command results as JSON"`

	// Debug enables debug logging. Shared with logging.
	Debug process.BoolFlag `flag:"debug" usage:"Enable debug logging"`

	// LLM toggles a compact, token-efficient output format intended for piping
	// into LLM prompts. Carries the same data as --json in fewer tokens.
	LLM process.BoolFlag `env:"INFRACOST_CLI_LLM" flag:"llm" usage:"Output command results in a compact, token-efficient format intended for LLM prompts"`

	// Logging contains the configuration for logging.
	// keep logging above other structs, so it gets processed first and others can log in their process functions.
	Logging logging.Config

	// Dashboard contains the configuration for the dashboard API.
	Dashboard dashboard.Config

	// Agents contains the configuration for the Agents API (findings).
	Agents agents.Config

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
	// The pricing endpoint env var was renamed from the legacy CLI's
	// INFRACOST_PRICING_API_ENDPOINT to INFRACOST_CLI_PRICING_ENDPOINT.
	// Honor the legacy name only when the new var wasn't set at all (checking
	// the env directly, not the resolved value: someone explicitly exporting
	// the new var as the default endpoint must not be repointed) and no flag
	// changed the endpoint, so migrating self-hosted users don't silently
	// price against pricing.api.infracost.io.
	if legacy := os.Getenv("INFRACOST_PRICING_API_ENDPOINT"); legacy != "" &&
		os.Getenv("INFRACOST_CLI_PRICING_ENDPOINT") == "" &&
		config.PricingEndpoint == defaultPricingEndpoint {
		logging.Warnf("INFRACOST_PRICING_API_ENDPOINT was renamed to INFRACOST_CLI_PRICING_ENDPOINT; using the legacy value %s — please update your configuration", legacy)
		config.PricingEndpoint = legacy
	}

	// The legacy CLI used a single INFRACOST_API_KEY for everything, including
	// self-hosted pricing. Adopt it as the pricing API key only when the
	// legacy pricing endpoint var is also set — that combination is
	// unambiguously a legacy self-hosted setup. INFRACOST_API_KEY on its own
	// is too ambiguous to act on: it still exists in many CI environments for
	// the runner flow, and switching those into self-hosted mode would
	// silently disable Infracost Cloud.
	if legacyKey := os.Getenv("INFRACOST_API_KEY"); legacyKey != "" &&
		config.PricingAPIKey == "" &&
		os.Getenv("INFRACOST_PRICING_API_ENDPOINT") != "" {
		logging.Warnf("using INFRACOST_API_KEY as the self-hosted pricing API key because INFRACOST_PRICING_API_ENDPOINT is also set; please rename these to INFRACOST_CLI_PRICING_API_KEY and INFRACOST_CLI_PRICING_ENDPOINT")
		config.PricingAPIKey = legacyKey
	}

	events.RegisterMetadata("cloudEnabled", os.Getenv("INFRACOST_ENABLE_CLOUD") == "true")
	events.RegisterMetadata("dashboardEnabled", os.Getenv("INFRACOST_ENABLE_DASHBOARD") == "true")
	events.RegisterMetadata("environment", config.Environment.String())
	events.RegisterMetadata("isDefaultPricingApiEndpoint", config.PricingEndpoint == defaultPricingEndpoint)

	// Self-hosted pricing mode: a static pricing API key means the user runs
	// their own Cloud Pricing API and typically has no route to Infracost
	// Cloud at all, so disable login (which would otherwise trigger an OAuth
	// flow / JWKS fetch) and telemetry. This runs after the sub-configs'
	// Process methods (process.Process is depth-first), so nothing below
	// overwrites it.
	config.Auth.Disabled = config.SelfHostedPricing()
	config.Events.Disabled = config.SelfHostedPricing()
}

// SelfHostedPricing reports whether the CLI is in self-hosted pricing mode:
// pricing calls authenticate with the static PricingAPIKey and every other
// Infracost Cloud integration is disabled.
func (config *Config) SelfHostedPricing() bool {
	return config.PricingAPIKey != ""
}

// ValidateSelfHostedPricing errors when the self-hosted pricing key is set
// but the endpoint still points at Infracost's SaaS pricing API — the SaaS
// only accepts OAuth tokens, so every price lookup with a static key would
// 403. Failing fast with the fix beats a confusing provider-plugin error.
func (config *Config) ValidateSelfHostedPricing() error {
	if config.SelfHostedPricing() && config.PricingEndpoint == defaultPricingEndpoint {
		return fmt.Errorf("INFRACOST_CLI_PRICING_API_KEY is set but the pricing endpoint still points at %s, which does not accept static API keys — set INFRACOST_CLI_PRICING_ENDPOINT to your self-hosted Cloud Pricing API", defaultPricingEndpoint)
	}
	return nil
}
