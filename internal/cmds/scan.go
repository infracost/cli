package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/logging"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/vcs"
	"github.com/spf13/cobra"
)

func Scan(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:     "scan",
		Aliases: []string{"analyse"}, // codespell:ignore analyse
		Short:   "Scan your IaC and derive FinOps costs and policy violations",
		Args:    cobra.MaximumNArgs(1),
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

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))
			runParameters, err := client.RunParameters(cmd.Context(), repositoryURL, branchName)
			if err != nil {
				return fmt.Errorf("failed to retrieve run parameters: %w", err)
			}
			cfg.OrgID = runParameters.OrganizationID

			events.RegisterMetadata("orgId", cfg.OrgID)
			events.RegisterMetadata("repoId", repositoryURL)
			events.RegisterMetadata("branchId", branchName)

			scanner := scanner.NewScanner(cfg)
			startTime := time.Now()
			result, err := scanner.Scan(cmd.Context(), runParameters, absoluteDirectory, branchName, source)
			if err != nil {
				return fmt.Errorf("failed to scan target: %w", err)
			}
			runSeconds := time.Since(startTime).Seconds()

			output := format.ToOutput(result)

			eventsClient := cfg.Events.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			// Diff against the previous cached result from the same session to detect
			// fixed policy violations.
			if prev, err := cfg.Cache.Read(absoluteDirectory, true); err == nil && prev.SameSession(&cfg.Cache) {
				output.TrackDiff(cmd.Context(), eventsClient, &prev.Data)
			}

			if err := cfg.Cache.Write(absoluteDirectory, &output); err != nil {
				logging.Warn("failed to cache results: " + err.Error())
			}

			output.TrackRun(cmd.Context(), eventsClient, runSeconds, "json")

			if err := output.ToJSON(os.Stdout); err != nil {
				return fmt.Errorf("failed to write JSON output: %w", err)
			}
			fmt.Println() // add newline after JSON output
			return nil
		},
	}

}
