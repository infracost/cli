package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/spf13/cobra"
)

func Inspect(cfg *config.Config) *cobra.Command {
	opts := inspect.Options{}
	var file string

	cmd := &cobra.Command{
		Use:   "inspect [path]",
		Short: "Inspect cached analysis results with filtering and grouping",
		Example: `  # Show a summary of the latest scan
  $ infracost inspect --summary

  # Show the 10 most expensive resources
  $ infracost inspect --top 10

  # Group results by provider
  $ infracost inspect --group-by provider

  # Show only failing policies
  $ infracost inspect --failing

  # Output the results as JSON
  $ infracost inspect --json`,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			count := 0
			if opts.Policy != "" {
				count++
			}
			if opts.Budget != "" {
				count++
			}
			if opts.Guardrail != "" {
				count++
			}
			if count > 1 {
				return fmt.Errorf("--policy, --budget, and --guardrail are mutually exclusive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			startTime := time.Now()
			var data *format.Output
			var err error

			switch {
			case file != "":
				data, err = cache.ReadFile(file)
				if err != nil {
					return err
				}
			case len(args) > 0:
				absPath, err := filepath.Abs(filepath.Clean(args[0]))
				if err != nil {
					return fmt.Errorf("failed to resolve path: %w", err)
				}
				data, err = cfg.Cache.ForPath(absPath)
				if err != nil {
					return fmt.Errorf("no cached results found, run 'infracost scan %s' first", args[0])
				}
			default:
				data, err = cfg.Cache.Latest(false)
				if err != nil {
					return fmt.Errorf("no cached results found, run 'infracost scan <path>' first")
				}
			}

			if err := inspect.Run(os.Stdout, data, opts); err != nil {
				return err
			}

			eventsClient := cfg.Events.Client(api.Client(cmd.Context(), cfg.Auth.TokenFromCache(cmd.Context()), cfg.OrgID))
			data.TrackRun(cmd.Context(), eventsClient, time.Since(startTime).Seconds(), "inspect", nil)

			return nil
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "Path to JSON file (skips cache)")
	cmd.Flags().BoolVar(&opts.Summary, "summary", false, "Show a summary view")
	cmd.Flags().StringSliceVar(&opts.GroupBy, "group-by", nil, "Group by: type, provider, project, policy (comma-separated or repeated)")
	cmd.Flags().StringVar(&opts.Policy, "policy", "", "Filter by policy name or slug")
	cmd.Flags().StringVar(&opts.Budget, "budget", "", "Show budget detail by name or ID")
	cmd.Flags().StringVar(&opts.Guardrail, "guardrail", "", "Show guardrail detail by name or ID")
	cmd.Flags().StringVar(&opts.Resource, "resource", "", "Filter by resource address")
	cmd.Flags().StringVar(&opts.Provider, "provider", "", "Filter by provider (aws, google, azurerm)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Filter by project name")
	cmd.Flags().BoolVar(&opts.CostsOnly, "costs-only", false, "Hide free resources")
	cmd.Flags().BoolVar(&opts.Failing, "failing", false, "Only show failing policies")
	cmd.Flags().IntVar(&opts.Top, "top", 0, "Show only the top N resources by cost")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Output as JSON")

	return cmd
}
