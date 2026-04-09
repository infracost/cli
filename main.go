package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/stacktrace"
	"github.com/infracost/cli/version"
	"github.com/infracost/go-proto/pkg/diagnostic"
	parserpb "github.com/infracost/proto/gen/go/infracost/parser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	var diags *diagnostic.Diagnostics
	cfg := new(config.Config)
	defer func() {
		if r := recover(); r != nil {
			client := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
			client.Push(context.Background(), "infracost-error", "error", r, "stacktrace", stacktrace.Sanitize(debug.Stack(), "github.com/infracost/cli/"))
			_, _ = fmt.Fprintf(os.Stderr, "An unexpected error occurred. This is a bug in Infracost, please report it at https://github.com/infracost/infracost/issues\n\n")
			_, _ = fmt.Fprintf(os.Stderr, "panic: %v\n\n%s\n", r, debug.Stack())
			os.Exit(1)
		}
	}()

	cmd := &cobra.Command{
		Use:           "infracost",
		Version:       version.Version,
		Short:         "Cloud cost estimates for IaC in your CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			events.RegisterMetadata("command", cmd.Name())
			events.RegisterMetadata("flags", func() []string {
				var flags []string
				cmd.Flags().Visit(func(flag *pflag.Flag) {
					flags = append(flags, flag.Name)
				})
				return flags
			}())

			process.Process(cfg) // set defaults and validate values etc
		},
	}

	cmd.AddCommand(cmds.Scan(cfg))
	cmd.AddCommand(cmds.Policies(cfg))
	cmd.AddCommand(cmds.CI(cfg))
	cmd.AddCommand(cmds.Agent(cfg))
	cmd.AddCommand(cmds.IDE(cfg))
	cmd.AddCommand(cmds.Inspect(cfg))
	cmd.AddCommand(cmds.Login(cfg))
	cmd.AddCommand(cmds.Logout(cfg))
	cmd.AddCommand(cmds.Price(cfg))
	cmd.AddCommand(cmds.WhoAmI(cfg))
	cmd.AddCommand(cmds.Update(cfg))
	cmd.AddCommand(cmds.Version(cfg))

	diags.Merge(process.PreProcess(cfg, cmd.PersistentFlags()))
	if diags.Critical().Len() > 0 {
		format.Diagnostics(diags)
		client := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
		for _, diag := range diags.Critical().Unwrap() {
			client.Push(context.Background(), "infracost-error", "error", diag.String())
		}
		return 1
	}

	if err := cmd.Execute(); err != nil {
		diags = diags.Add(diagnostic.FromError(parserpb.DiagnosticType_DIAGNOSTIC_TYPE_FAILED_OPERATION, err))
	}
	format.Diagnostics(diags)
	if diags.Critical().Len() > 0 {
		client := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
		for _, diag := range diags.Critical().Unwrap() {
			client.Push(context.Background(), "infracost-error", "error", diag.String())
		}
		return 1
	}

	return 0
}
