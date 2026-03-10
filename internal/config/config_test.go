package config

import (
	"testing"

	"github.com/infracost/cli/internal/config/process"
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
