package cmds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Scan(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:     "scan [path]",
		Aliases: []string{"analyse"}, // codespell:ignore analyse
		Short:   "Scan your IaC and derive FinOps costs and policy violations",
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

			repositoryURL := vcs.GetRemoteURL(absoluteDirectory)
			branchName := vcs.GetCurrentBranch(absoluteDirectory)

			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			var result *format.Result
			var runSeconds float64

			if err := ui.RunWithSpinnerErr(cmd.Context(), "Scanning...", "Scan complete", func(ctx context.Context) error {
				runParameters, err := client.RunParameters(ctx, repositoryURL, branchName)
				if err != nil {
					return fmt.Errorf("failed to retrieve run parameters: %w", err)
				}

				// If --org was not provided, use the org from RunParameters.
				// If --org was provided, show a message when it overrides the default.
				if cfg.Org == "" {
					cfg.OrgID = runParameters.OrganizationID
				} else if runParameters.OrganizationID != "" && cfg.OrgID != runParameters.OrganizationID {
					if uc, ucErr := cfg.Auth.LoadUserCache(); ucErr != nil {
						logging.WithError(ucErr).Msg("failed to load user cache for override message")
					} else if uc != nil {
						for _, org := range uc.Organizations {
							if org.ID == cfg.OrgID {
								ui.Stepf("%s (overriding default)", org.Slug)
								break
							}
						}
					}
				}

				events.RegisterMetadata("orgId", cfg.OrgID)
				events.RegisterMetadata("repoId", repositoryURL)
				events.RegisterMetadata("branchId", branchName)

				s := scanner.NewScanner(cfg)
				startTime := time.Now()
				result, err = s.Scan(ctx, runParameters, absoluteDirectory, branchName, source)
				if err != nil {
					return fmt.Errorf("failed to scan target: %w", err)
				}
				runSeconds = time.Since(startTime).Seconds()
				return nil
			}); err != nil {
				return err
			}

			output := format.ToOutput(result)

			eventsClient := cfg.Events.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			// Load previous result for this directory (stale allowed) for run diff counts.
			var prevForDir *format.Output
			if p, err := cfg.Cache.ForPathAllowStale(absoluteDirectory); err != nil {
				logging.Infof("could not load previous run data for directory: %v", err)
			} else {
				logging.Infof("found previous run data for directory in cache")
				prevForDir = p
			}

			// Diff against the previous cached result to detect fixed policy violations.
			if prev, err := cfg.Cache.Latest(true); err != nil {
				logging.Infof("could not load previous run data: %v", err)
			} else {
				logging.Infof("found previous run data in cache")
				output.TrackDiff(cmd.Context(), eventsClient, prev)
			}

			if err := cfg.Cache.Write(absoluteDirectory, &output); err != nil {
				logging.Warn("failed to cache results: " + err.Error())
			}

			output.TrackRun(cmd.Context(), eventsClient, runSeconds, "json", prevForDir)

			if err := output.ToJSON(os.Stdout); err != nil {
				return fmt.Errorf("failed to write JSON output: %w", err)
			}
			fmt.Println() // add newline after JSON output
			return nil
		},
	}

}
