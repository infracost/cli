package cmds

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Price(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "price",
		Short: "Read IaC from stdin, scan it, and print the cost estimate",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.MkdirTemp("", "infracost-price-*")
			if err != nil {
				return fmt.Errorf("failed to create temporary directory: %w", err)
			}
			defer func() { _ = os.RemoveAll(dir) }()

			tmpFile := filepath.Join(dir, "main.tf")
			f, err := os.Create(tmpFile) //nolint:gosec // path is constructed from our own temp dir
			if err != nil {
				return fmt.Errorf("failed to create temporary file: %w", err)
			}

			if _, err := io.Copy(f, cmd.InOrStdin()); err != nil {
				_ = f.Close()
				return fmt.Errorf("failed to write stdin to temporary file: %w", err)
			}

			if err := f.Close(); err != nil {
				return fmt.Errorf("failed to close temporary file: %w", err)
			}

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to log in: %w", err)
			}

			repositoryURL := vcs.GetRemoteURL(dir)
			branchName := vcs.GetCurrentBranch(dir)

			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))
			runParameters, err := client.RunParameters(cmd.Context(), repositoryURL, branchName)
			if err != nil {
				return fmt.Errorf("failed to retrieve run parameters: %w", err)
			}
			if cfg.Org == "" {
				cfg.OrgID = runParameters.OrganizationID
			}

			events.RegisterMetadata("orgId", cfg.OrgID)
			events.RegisterMetadata("repoId", repositoryURL)
			events.RegisterMetadata("branchId", branchName)

			scanner := scanner.NewScanner(cfg)
			startTime := time.Now()
			result, err := scanner.Scan(cmd.Context(), runParameters, dir, branchName, source)
			if err != nil {
				return fmt.Errorf("failed to scan target: %w", err)
			}
			runSeconds := time.Since(startTime).Seconds()

			output := format.ToOutput(result)

			eventsClient := cfg.Events.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			// Diff against the previous cached result to detect fixed policy violations.
			if prev, err := cfg.Cache.Latest(true); err != nil {
				logging.Infof("could not load previous run data: %v", err)
			} else {
				logging.Infof("found previous run data in cache")
				output.TrackDiff(cmd.Context(), eventsClient, prev)
			}

			if err := cfg.Cache.Write(dir, &output); err != nil {
				logging.Warn("failed to cache results: " + err.Error())
			}

			if cfg.JSON.Value && cfg.LLM.Value {
				return fmt.Errorf("--json and --llm cannot be used together")
			}

			outputFormat := "text"
			switch {
			case cfg.JSON.Value:
				outputFormat = "json"
			case cfg.LLM.Value:
				outputFormat = "llm"
			}
			output.TrackRun(cmd.Context(), eventsClient, runSeconds, outputFormat, nil)

			if cfg.JSON.Value {
				if err := output.ToJSON(os.Stdout); err != nil {
					return fmt.Errorf("failed to write JSON output: %w", err)
				}
				fmt.Println()
				return nil
			}

			if cfg.LLM.Value {
				if err := output.ToTOON(os.Stdout); err != nil {
					return fmt.Errorf("failed to write LLM output: %w", err)
				}
				fmt.Println()
				return nil
			}

			if err := inspect.Run(os.Stdout, &output, inspect.Options{}); err != nil {
				return err
			}
			printInspectHints(&output)
			return nil
		},
	}
	cmd.Hidden = true
	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")
	return cmd
}
