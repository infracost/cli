package cmds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/orgresolve"
	"github.com/infracost/cli/internal/scanrun"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Scan(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Scan your IaC and derive FinOps costs and policy violations",
		Example: `  # Scan the current directory
  $ infracost scan

  # Scan a specific project path
  $ infracost scan ./terraform

  # Scan against a different organization's policies & prices
  $ infracost scan --org acme`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to log in: %w", err)
			}

			// default to current working dir
			target := "."
			if len(args) > 0 {
				target = args[0]
			}

			absoluteDirectory, err := filepath.Abs(filepath.Clean(target))
			if err != nil {
				return fmt.Errorf("failed to get absolute path to target: %w", err)
			}

			if info, err := os.Stat(absoluteDirectory); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("target directory does not exist")
				}
				return fmt.Errorf("failed to get info for target directory: %w", err)
			} else if !info.IsDir() {
				// TODO: should probably generate a minimal config for a single project in this case, but for now just require a directory
				return fmt.Errorf("target is not a directory")
			}

			// Resolve org outside the spinner — it may prompt interactively
			// when the user belongs to multiple orgs and has no saved selection.
			if err := orgresolve.Resolve(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result *scanrun.Result
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Scanning...", "Scan complete", func(ctx context.Context) error {
				var runErr error
				result, runErr = scanrun.Run(ctx, cfg, scanrun.Options{
					AbsoluteDir: absoluteDirectory,
					Source:      source,
					Bypass:      true,
					OnOverride: func(slug string) {
						ui.Stepf("%s (overriding default)", slug)
					},
				})
				return runErr
			}); err != nil {
				return err
			}

			output := result.Output
			eventsClient := cfg.Events.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			// Diff against the previous cached result to detect fixed policy violations.
			if prev, err := cfg.Cache.Latest(true); err != nil {
				logging.Infof("could not load previous run data: %v", err)
			} else {
				logging.Infof("found previous run data in cache")
				output.TrackDiff(cmd.Context(), eventsClient, prev)
			}

			outputFormat := "text"
			switch {
			case cfg.LLM.Value:
				outputFormat = "llm"
			case cfg.JSON.Value:
				outputFormat = "json"
			}
			output.TrackRun(cmd.Context(), eventsClient, result.RunSeconds, outputFormat, result.PrevForDir)

			if cfg.LLM.Value {
				if err := output.ToTOON(os.Stdout); err != nil {
					return fmt.Errorf("failed to write LLM output: %w", err)
				}
				fmt.Println()
				return nil
			}

			if cfg.JSON.Value {
				if err := output.ToJSON(os.Stdout); err != nil {
					return fmt.Errorf("failed to write JSON output: %w", err)
				}
				fmt.Println() // add newline after JSON output
				return nil
			}

			if err := inspect.Run(os.Stdout, output, inspect.Options{}); err != nil {
				return err
			}
			printInspectHints(output)
			return nil
		},
	}

	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")

	return cmd
}
