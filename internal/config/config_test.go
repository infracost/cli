package config

import (
	"testing"

	"github.com/infracost/cli/pkg/config/process"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestConfig_Process(t *testing.T) {
	var cfg Config

	flags := pflag.NewFlagSet("", pflag.ContinueOnError)

	// first, make sure that preprocess doesn't error or panic when no values provided.
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil)) // we have no required flags yet, so will provide nothing
	process.Process(&cfg)                // make sure doesn't panic

	// environment is a shared flag, so let's make sure that all worked
	require.Equal(t, "prod", cfg.Environment.String())
	require.Equal(t, "prod", cfg.Auth.Environment)
	require.Equal(t, "prod", cfg.Dashboard.Environment)
}

func TestConfig_SelfHostedPricing(t *testing.T) {
	// Env-only on purpose: a flag would put the credential in argv.
	t.Setenv("INFRACOST_CLI_PRICING_API_KEY", "self-hosted-key")

	var cfg Config
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.True(t, cfg.SelfHostedPricing())
	require.True(t, cfg.Auth.Disabled, "self-hosted pricing mode must disable Infracost Cloud auth")
	require.True(t, cfg.Events.Disabled, "self-hosted pricing mode must disable telemetry")
}

func TestConfig_ValidateSelfHostedPricing(t *testing.T) {
	// Key + default endpoint can never work (the SaaS pricing API only
	// accepts OAuth tokens), so it must fail fast with the fix.
	cfg := Config{PricingAPIKey: "key", PricingEndpoint: "https://pricing.api.infracost.io"}
	require.ErrorContains(t, cfg.ValidateSelfHostedPricing(), "INFRACOST_CLI_PRICING_ENDPOINT")

	cfg.PricingEndpoint = "http://pricing.internal:4000"
	require.NoError(t, cfg.ValidateSelfHostedPricing())

	// Not in self-hosted mode: never an error.
	require.NoError(t, (&Config{PricingEndpoint: "https://pricing.api.infracost.io"}).ValidateSelfHostedPricing())
}

func TestConfig_SelfHostedPricingOffByDefault(t *testing.T) {
	var cfg Config

	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.False(t, cfg.SelfHostedPricing())
	require.False(t, cfg.Auth.Disabled)
	require.False(t, cfg.Events.Disabled)
}

func TestConfig_LegacyPricingEndpointEnvVar(t *testing.T) {
	// The legacy CLI read INFRACOST_PRICING_API_ENDPOINT; v2 renamed it to
	// INFRACOST_CLI_PRICING_ENDPOINT. The legacy name must still be honored
	// when nothing else sets an endpoint, so migrating self-hosted users
	// don't silently price against pricing.api.infracost.io.
	t.Setenv("INFRACOST_PRICING_API_ENDPOINT", "http://pricing.internal:4000")

	var cfg Config
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.Equal(t, "http://pricing.internal:4000", cfg.PricingEndpoint)
}

func TestConfig_NewPricingEndpointWinsOverLegacy(t *testing.T) {
	t.Setenv("INFRACOST_PRICING_API_ENDPOINT", "http://legacy.internal:4000")
	t.Setenv("INFRACOST_CLI_PRICING_ENDPOINT", "http://new.internal:4000")

	var cfg Config
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.Equal(t, "http://new.internal:4000", cfg.PricingEndpoint)
}

func TestConfig_ExplicitDefaultEndpointNotRepointedToLegacy(t *testing.T) {
	// Someone explicitly exporting the new var as the default endpoint must
	// not be silently repointed at a stale legacy var in their shell.
	t.Setenv("INFRACOST_PRICING_API_ENDPOINT", "http://legacy.internal:4000")
	t.Setenv("INFRACOST_CLI_PRICING_ENDPOINT", "https://pricing.api.infracost.io")

	var cfg Config
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.Equal(t, "https://pricing.api.infracost.io", cfg.PricingEndpoint)
}

func TestConfig_LegacyAPIKeyAdoptedOnlyWithLegacyEndpoint(t *testing.T) {
	// INFRACOST_API_KEY + INFRACOST_PRICING_API_ENDPOINT is unambiguously a
	// legacy self-hosted setup, so the key is adopted.
	t.Setenv("INFRACOST_API_KEY", "legacy-key")
	t.Setenv("INFRACOST_PRICING_API_ENDPOINT", "http://legacy.internal:4000")

	var cfg Config
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.Equal(t, "legacy-key", cfg.PricingAPIKey)
	require.Equal(t, "http://legacy.internal:4000", cfg.PricingEndpoint)
	require.True(t, cfg.SelfHostedPricing())
}

func TestConfig_LegacyAPIKeyAloneIsIgnored(t *testing.T) {
	// INFRACOST_API_KEY on its own is ambiguous (it lingers in CI envs for
	// the runner flow) — it must not flip the CLI into self-hosted mode.
	t.Setenv("INFRACOST_API_KEY", "legacy-key")

	var cfg Config
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse(nil))
	process.Process(&cfg)

	require.Empty(t, cfg.PricingAPIKey)
	require.False(t, cfg.SelfHostedPricing())
}

func TestConfig_DebugFlag(t *testing.T) {
	var cfg Config

	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse([]string{"--debug"}))
	process.Process(&cfg)

	require.True(t, cfg.Debug.Value)
	require.True(t, cfg.Logging.Debug)
	require.Equal(t, "debug", cfg.Logging.WriteLevel)
}
