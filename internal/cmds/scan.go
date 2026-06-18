package cmds

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// ScanInput is the parsed input for `scan`. Per-invocation knobs that
// vary across callers go here; callers (ScanCmd, MCP tool handler) are
// responsible for populating each field. The pure Scan function only
// reads from in — it never reaches into cfg for these values.
type ScanInput struct {
	// Path is the directory to scan. Empty means current working
	// directory.
	Path string
	// Currency is the ISO 4217 code prices are rendered in. Empty
	// falls through to the scanner's default ("USD"). ScanCmd
	// populates this from cfg.Currency (which is bound to the
	// --currency CLI flag); the MCP tool handler takes it from its
	// input args, falling back to cfg.Currency.
	Currency string
	// KubernetesClusterFrom is an optional path to the Terraform that
	// defines the cluster Kubernetes manifests deploy onto (e.g. an EKS
	// module). When set, the scanner derives the cluster's node pools from
	// it and prices K8s workloads against them.
	KubernetesClusterFrom string
}

// ScanResult is the typed output of `scan`. Aliased to *format.Output so
// `scan --json` / `scan --llm` stay byte-identical with previous releases.
// A projected MCP-only summary shape is tracked in a follow-up ticket.
type ScanResult = *format.Output

// Scan validates the target path, runs the scanner, records run
// telemetry, and returns the typed result.
//
// Authentication and org resolution are the caller's responsibility
// — `source` and `cfg.OrgID` must be populated before calling Scan.
// `store` is the cache backend used to record this run and look up
// the previous one (ScanCmd passes &cfg.Cache; the MCP tool passes
// the session's MemoryStore). `outputFormat` is the label recorded
// on the infracost-run telemetry event so analytics can distinguish
// CLI text output from --json / --llm scrapes and from MCP tool
// calls. It is deliberately a separate parameter, not a ScanInput
// field, so MCP tool callers cannot impersonate the CLI.
func Scan(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, store cache.Store, in ScanInput, outputFormat string) (ScanResult, error) {
	target := in.Path
	if target == "" {
		target = "."
	}

	absolutePath, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path to target: %w", err)
	}
	absoluteDirectory := absolutePath

	if info, err := os.Stat(absolutePath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("target directory does not exist")
		}
		return nil, fmt.Errorf("failed to get info for target directory: %w", err)
	} else if !info.IsDir() {
		absoluteDirectory = filepath.Dir(absolutePath)
	}

	repositoryURL := vcs.GetRemoteURL(absoluteDirectory)
	branchName := vcs.GetCurrentBranch(absoluteDirectory)

	client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))

	runParameters, err := client.RunParameters(ctx, repositoryURL, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve run parameters: %w", err)
	}

	// If --org was not provided, use the org from RunParameters.
	// If --org was provided, log when it overrides what the API reports
	// as the repo's default org.
	if cfg.Org == "" {
		cfg.OrgID = runParameters.OrganizationID
	} else if runParameters.OrganizationID != "" && cfg.OrgID != runParameters.OrganizationID {
		if uc, ucErr := cfg.Auth.LoadUserCache(); ucErr != nil {
			logging.WithError(ucErr).Msg("failed to load user cache for override message")
		} else if uc != nil {
			for _, org := range uc.Organizations {
				if org.ID == cfg.OrgID {
					logging.Infof("using --org %s; overriding repository default", org.Slug)
					break
				}
			}
		}
	}

	events.RegisterMetadata("orgId", cfg.OrgID)
	events.RegisterMetadata("repoId", repositoryURL)
	events.RegisterMetadata("branchId", branchName)

	s := &scanner.Scanner{
		Plugins:               &cfg.Plugins,
		Logging:               cfg.Logging,
		Dashboard:             cfg.Dashboard,
		Currency:              in.Currency,
		PricingEndpoint:       cfg.PricingEndpoint,
		KubernetesClusterFrom: in.KubernetesClusterFrom,
	}
	startTime := time.Now()
	result, err := s.Scan(ctx, runParameters, absolutePath, branchName, source)
	if err != nil {
		return nil, fmt.Errorf("failed to scan target: %w", err)
	}
	runSeconds := time.Since(startTime).Seconds()

	output := format.ToOutput(result)

	eventsClient := cfg.Events.Client(api.Client(ctx, source, cfg.OrgID))

	// Load previous result for this directory (stale allowed) for run diff counts.
	var prevForDir *format.Output
	if p, err := store.ForPathAllowStale(absolutePath); err != nil {
		logging.Infof("could not load previous run data for path: %v", err)
	} else {
		logging.Infof("found previous run data for path in cache")
		prevForDir = p
	}

	// Diff against the previous cached result to detect fixed policy violations.
	if prev, err := store.Latest(true); err != nil {
		logging.Infof("could not load previous run data: %v", err)
	} else {
		logging.Infof("found previous run data in cache")
		output.TrackDiff(ctx, eventsClient, prev)
	}

	if err := store.Write(absolutePath, &output); err != nil {
		logging.Warn("failed to cache results: " + err.Error())
	}

	output.TrackRun(ctx, eventsClient, runSeconds, outputFormat, prevForDir)

	return &output, nil
}

// ScanCmd builds the cobra command. Parses flags into ScanInput,
// authenticates, resolves the active org, then calls the pure Scan
// function and dispatches the result to one of the renderers based on
// --json / --llm.
func ScanCmd(cfg *config.Config) *cobra.Command {
	var in ScanInput
	var includeWarnings bool
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
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			startTime := time.Now()

			cache.Prune(cache.DefaultPruneAge)

			if len(args) > 0 {
				in.Path = args[0]
			}
			// Currency on the CLI comes from --currency (env-bound on
			// cfg). Threading it through ScanInput keeps the pure
			// function agnostic of cfg.Currency so the MCP can supply
			// a per-call override.
			in.Currency = cfg.Currency

			outputFormat := "text"
			switch {
			case cfg.LLM.Value:
				outputFormat = "llm"
			case cfg.JSON.Value:
				outputFormat = "json"
			}

			defer func() {
				if runErr != nil {
					msg := runErr.Error()
					if len(msg) > 200 {
						msg = msg[:200]
					}
					eventsClient := cfg.Events.Client(api.Client(context.Background(), cfg.Auth.TokenFromCache(context.Background()), cfg.OrgID))
					eventsClient.Push(context.Background(), "infracost-error",
						"error", msg,
						"runSeconds", time.Since(startTime).Seconds(),
						"outputFormat", outputFormat,
					)
				}
			}()

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to log in: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result ScanResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Scanning...", "Scan complete", func(ctx context.Context) error {
				var scanErr error
				result, scanErr = Scan(ctx, cfg, source, &cfg.Cache, in, outputFormat)
				return scanErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, scanRenderers(includeWarnings))
		},
	}

	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")
	cmd.Flags().StringVar(&in.KubernetesClusterFrom, "kubernetes-cluster-from", "", "Path to the Terraform that defines the cluster (e.g. an EKS module) that Kubernetes manifests deploy onto, used to price K8s workloads")
	cmd.Flags().BoolVar(&includeWarnings, "include-warnings", false, "Also show warning-severity diagnostics in the summary")

	return cmd
}

func scanRenderers(includeWarnings bool) Renderers[ScanResult] {
	return Renderers[ScanResult]{
		Human: func(w io.Writer, r ScanResult) error {
			return renderScanHuman(w, r, includeWarnings)
		},
		JSON: renderScanJSON,
		LLM:  renderScanLLM,
	}
}

func renderScanHuman(w io.Writer, r ScanResult, includeWarnings bool) error {
	if err := inspect.Run(w, r, inspect.Options{}); err != nil {
		return err
	}
	printInspectHints(r, includeWarnings)
	inspect.WriteSummaryDiagnostics(w, r, includeWarnings)
	return nil
}

func renderScanJSON(w io.Writer, r ScanResult) error {
	if err := r.ToJSON(w); err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	_, err := fmt.Fprintln(w)
	return err
}

func renderScanLLM(w io.Writer, r ScanResult) error {
	if err := r.ToTOON(w); err != nil {
		return fmt.Errorf("failed to write LLM output: %w", err)
	}
	_, err := fmt.Fprintln(w)
	return err
}
