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
		Example: `  # Headline numbers (totals + per-policy counts):
  $ infracost inspect --summary

  # Single scalar from the summary (e.g. "how many failing FinOps policies?"):
  $ infracost inspect --summary --fields failing_policies

  # Top 10 most expensive resources:
  $ infracost inspect --top 10

  # Total potential monthly savings if every FinOps issue were fixed:
  $ infracost inspect --total-savings

  # Top 5 FinOps issues, just the addresses (one per line):
  $ infracost inspect --top-savings 5 --fields address

  # Top 5 FinOps issues with custom column projection (TSV with header):
  $ infracost inspect --top-savings 5 --fields address,monthly_savings,policy

  # Every resource missing the 'team' tag, one address per line:
  $ infracost inspect --missing-tag team

  # Same, with extra columns for context (TSV-friendly for awk / cut):
  $ infracost inspect --missing-tag team --fields address,type,monthly_cost

  # Resources whose 'environment' tag is set but uses a disallowed value:
  $ infracost inspect --invalid-tag environment

  # Resources with monthly cost > $100:
  $ infracost inspect --min-cost 100

  # Composable filter: missing 'team' tag AND on AWS:
  $ infracost inspect --filter "tag.team=missing,provider=aws"

  # All addresses failing a specific policy (full list, no truncation):
  $ infracost inspect --policy "Required Tags" --addresses-only

  # Machine-readable, token-efficient output for LLM pipelines:
  $ infracost inspect --summary --llm`,
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
			return inspect.ValidateGroupBy(opts.GroupBy)
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

			if cfg.JSON.Value && cfg.LLM.Value {
				return fmt.Errorf("--json and --llm cannot be used together")
			}
			opts.JSON = cfg.JSON.Value
			opts.LLM = cfg.LLM.Value
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
	cmd.Flags().StringSliceVar(&opts.GroupBy, "group-by", nil, "Group by: "+inspect.GroupByOptionsHelp()+" (comma-separated or repeated)")
	cmd.Flags().StringVar(&opts.Policy, "policy", "", "Filter by policy name or slug")
	cmd.Flags().StringVar(&opts.Budget, "budget", "", "Show budget detail by name or ID")
	cmd.Flags().StringVar(&opts.Guardrail, "guardrail", "", "Show guardrail detail by name or ID")
	cmd.Flags().StringVar(&opts.Resource, "resource", "", "Filter by resource address")
	cmd.Flags().StringVar(&opts.Provider, "provider", "", "Filter by provider (aws, google, azurerm)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Filter by project name")
	cmd.Flags().BoolVar(&opts.CostsOnly, "costs-only", false, "Hide free resources")
	cmd.Flags().BoolVar(&opts.Failing, "failing", false, "Only show failing policies")
	cmd.Flags().IntVar(&opts.Top, "top", 0, "Show only the top N resources by cost")

	// Aggregation views.
	cmd.Flags().BoolVar(&opts.TotalSavings, "total-savings", false, "Print the scalar sum of monthly_savings across every FinOps issue")
	cmd.Flags().IntVar(&opts.TopSavings, "top-savings", 0, "Print the top N FinOps issues sorted by monthly_savings")

	// Output modifiers.
	cmd.Flags().BoolVar(&opts.AddressesOnly, "addresses-only", false, "Strip everything except resource addresses (one per line). Composes with the other selection flags.")

	// Targeted filters (replace common jq/python patterns).
	cmd.Flags().StringVar(&opts.MissingTag, "missing-tag", "", "Limit to resources missing the given tag key entirely")
	cmd.Flags().StringVar(&opts.InvalidTag, "invalid-tag", "", "Limit to resources where the given tag is set but its value is outside the policy's allowed list")
	cmd.Flags().Float64Var(&opts.MinCost, "min-cost", 0, "Limit to resources with monthly cost ≥ N")
	cmd.Flags().Float64Var(&opts.MaxCost, "max-cost", 0, "Limit to resources with monthly cost ≤ N")

	// Generic filter expression. Comma-separated AND'd equality predicates.
	cmd.Flags().StringVar(&opts.Filter, "filter", "", `Filter expression (AND'd, comma-separated). Supported keys: policy, project, provider, tag.<key>=missing. Example: --filter "tag.team=missing,provider=aws"`)

	// Column projection. Per-view canonical field set; unknown names error.
	cmd.Flags().StringSliceVar(&opts.Fields, "fields", nil, `Tabular column projection (comma-separated). Available fields depend on the view (--top-savings, --policy, --missing-tag/--invalid-tag, etc.). Example: --top-savings 10 --fields address,monthly_savings. --addresses-only is an alias for --fields=address.`)

	return cmd
}
