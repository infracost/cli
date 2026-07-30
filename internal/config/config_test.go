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
	var cfg Config

	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	if diags := process.PreProcess(&cfg, flags); diags.Len() != 0 {
		t.Fatal(diags)
	}
	require.NoError(t, flags.Parse([]string{"--pricing-api-key", "self-hosted-key"}))
	process.Process(&cfg)

	require.True(t, cfg.SelfHostedPricing())
	require.True(t, cfg.Auth.Disabled, "self-hosted pricing mode must disable Infracost Cloud auth")
	require.True(t, cfg.Events.Disabled, "self-hosted pricing mode must disable telemetry")
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
