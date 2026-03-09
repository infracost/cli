package cmds

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/inspect"
	"github.com/spf13/cobra"
)

func Inspect(cfg *config.Config) *cobra.Command {
	opts := inspect.Options{}
	var file string

	cmd := &cobra.Command{
		Use:   "inspect [path]",
		Short: "Inspect cached analysis results with filtering and grouping",
		RunE: func(_ *cobra.Command, args []string) error {
			var data *cache.Entry
			var err error

			if file != "" {
				output, ferr := cache.ReadFile(file)
				if ferr != nil {
					return ferr
				}
				return inspect.Run(os.Stdout, output, opts)
			}

			if len(args) > 0 {
				absPath, err := filepath.Abs(filepath.Clean(args[0]))
				if err != nil {
					return fmt.Errorf("failed to resolve path: %w", err)
				}
				data, err = cfg.Cache.Read(absPath, false)
				if err != nil {
					return fmt.Errorf("no cached results found, run 'infracost scan %s' first", args[0])
				}
			} else {
				data, err = cfg.Cache.ReadLatest()
				if err != nil {
					return fmt.Errorf("no cached results found, run 'infracost scan <path>' first")
				}
			}

			return inspect.Run(os.Stdout, &data.Data, opts)
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "Path to JSON file (skips cache)")
	cmd.Flags().BoolVar(&opts.Summary, "summary", false, "Show summary overview")
	cmd.Flags().StringSliceVar(&opts.GroupBy, "group-by", nil, "Group by: type, provider, project, policy (comma-separated or repeated)")
	cmd.Flags().StringVar(&opts.Policy, "policy", "", "Filter by policy name or slug")
	cmd.Flags().StringVar(&opts.Resource, "resource", "", "Filter by resource address")
	cmd.Flags().StringVar(&opts.Provider, "provider", "", "Filter by provider (aws, google, azurerm)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Filter by project name")
	cmd.Flags().BoolVar(&opts.CostsOnly, "costs-only", false, "Hide free resources")
	cmd.Flags().BoolVar(&opts.Failing, "failing", false, "Only show failing policies")
	cmd.Flags().IntVar(&opts.Top, "top", 0, "Top N by cost")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "JSON output")

	return cmd
}
