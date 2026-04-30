package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/stacktrace"
	"github.com/infracost/cli/version"
	"github.com/infracost/go-proto/pkg/diagnostic"
	parserpb "github.com/infracost/proto/gen/go/infracost/parser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// runTrackedCommands lists commands that fire their own infracost-run event
// and should therefore be excluded from the generic infracost-command event.
var runTrackedCommands = map[string]bool{
	"scan":    true,
	"price":   true,
	"inspect": true,
}

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	startTime := time.Now()
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
		Use:     "infracost",
		Version: version.Version,
		Short:   "Cloud cost estimates for IaC in your CLI",
		Example: `  # First-time setup (auth, agents, IDE, CI)
  $ infracost setup

  # Scan the current directory for costs and policy violations
  $ infracost scan

  # View a summary of the latest scan results
  $ infracost inspect --summary`,
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

			if cfg.NoColor {
				ui.DisableColor()
			}
		},
	}

	cmd.AddGroup(
		&cobra.Group{ID: "setup", Title: "Setup & integrations:"},
		&cobra.Group{ID: "analyze", Title: "Analyze infrastructure:"},
		&cobra.Group{ID: "workspace", Title: "Organization settings:"},
		&cobra.Group{ID: "maintain", Title: "CLI maintenance:"},
	)

	addCmd := func(c *cobra.Command, groupID string) {
		c.GroupID = groupID
		cmd.AddCommand(c)
	}

	addCmd(cmds.Setup(cfg), "setup")
	addCmd(cmds.Auth(cfg), "setup")
	addCmd(cmds.Org(cfg), "setup")
	addCmd(cmds.Agent(cfg), "setup")
	addCmd(cmds.IDE(cfg), "setup")
	addCmd(cmds.CI(cfg), "setup")

	addCmd(cmds.Scan(cfg), "analyze")
	addCmd(cmds.Inspect(cfg), "analyze")
	addCmd(cmds.Price(cfg), "analyze")

	addCmd(cmds.Policies(cfg), "workspace")
	addCmd(cmds.Budgets(cfg), "workspace")
	addCmd(cmds.Guardrails(cfg), "workspace")

	addCmd(cmds.Doctor(cfg), "maintain")
	addCmd(cmds.Update(cfg), "maintain")

	cmd.AddCommand(cmds.Version(cfg))

	for _, c := range cmds.Deprecated(cfg) {
		cmd.AddCommand(c)
	}

	cmds.ApplyHelpStyles(cmd)

	diags.Merge(process.PreProcess(cfg, cmd.PersistentFlags()))
	if diags.Critical().Len() > 0 {
		format.Diagnostics(diags)
		client := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
		for _, diag := range diags.Critical().Unwrap() {
			client.Push(context.Background(), "infracost-error", "error", diag.String())
		}
		return 1
	}

	err := cmd.Execute()
	if err != nil {
		diags = diags.Add(diagnostic.FromError(parserpb.DiagnosticType_DIAGNOSTIC_TYPE_FAILED_OPERATION, err))
	}

	// Fire a lightweight infracost-command event for commands that don't
	// already emit their own infracost-run event.
	if command, ok := events.GetMetadata[string]("command"); ok && !runTrackedCommands[command] {
		client := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
		extra := []interface{}{
			"success", err == nil,
			"durationSeconds", time.Since(startTime).Seconds(),
		}
		if err != nil {
			msg := err.Error()
			if len(msg) > 200 {
				msg = msg[:200]
			}
			extra = append(extra, "errorMessage", msg)
		}
		client.Push(context.Background(), "infracost-command", extra...)
	}

	format.Diagnostics(diags)
	if diags.Critical().Len() > 0 {
		client := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
		for _, diag := range diags.Critical().Unwrap() {
			client.Push(context.Background(), "infracost-error", "error", diag.String())
		}
		return 1
	}
	if err != nil {
		return 1
	}

	return 0
}
