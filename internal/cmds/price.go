package cmds

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
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

// PriceInput is the parsed input for `price`. PriceCmd reads IaC from
// stdin and populates IaC + Currency from the cobra command; the MCP
// `price` tool populates them from the request payload.
type PriceInput struct {
	// IaC is the raw Terraform source to price. Required — Price returns
	// an error when it's empty so the agent gets immediate feedback
	// instead of a successful zero-resource scan.
	IaC string `json:"iac" jsonschema:"Raw Terraform / IaC source to price. Must contain at least one resource block; sending an empty string is an error."`
	// Currency is the ISO 4217 code prices are rendered in. Empty falls
	// through to the scanner's default ("USD"). PriceCmd populates this
	// from cfg.Currency (bound to --currency); the MCP tool handler
	// takes it from its input args, falling back to cfg.Currency.
	Currency string `json:"currency,omitempty" jsonschema:"ISO 4217 currency code (e.g. USD, EUR, GBP). Optional; defaults to the org-configured currency."`
}

// PriceResult is the typed output of `price`. Aliased to *format.Output so
// `price --json` / `price --llm` stay byte-identical with previous
// releases. The MCP `price` tool projects this to MCPPriceOutput.
type PriceResult = *format.Output

// Price writes the IaC to a temporary directory, scans it, records run
// telemetry, and returns the typed result.
//
// Authentication and org resolution are the caller's responsibility —
// `source` and `cfg.OrgID` must be populated before calling Price.
// `store` is the cache backend used to record this run and look up the
// previous one (PriceCmd passes &cfg.Cache; the MCP tool passes the
// session's MemoryStore). `outputFormat` is the label recorded on the
// infracost-run telemetry event; it is a separate parameter, not a
// PriceInput field, so MCP cannot impersonate the CLI's text/json/llm
// labels.
func Price(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, store cache.Store, in PriceInput, outputFormat string) (PriceResult, error) {
	if in.IaC == "" {
		return nil, fmt.Errorf("no IaC content provided")
	}

	dir, err := os.MkdirTemp("", "infracost-price-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	tmpFile := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(tmpFile, []byte(in.IaC), 0o600); err != nil {
		return nil, fmt.Errorf("failed to write IaC to temporary file: %w", err)
	}

	repositoryURL := vcs.GetRemoteURL(dir)
	branchName := vcs.GetCurrentBranch(dir)

	// Self-hosted pricing mode prices without Infracost Cloud: run parameters
	// stay zero-valued, so no policies or usage defaults apply.
	var runParameters dashboard.RunParameters
	if cfg.SelfHostedPricing() {
		if err := cfg.ValidateSelfHostedPricing(); err != nil {
			return nil, err
		}
		logging.Infof("INFRACOST_CLI_PRICING_API_KEY is set: pricing against the self-hosted pricing API only; Infracost Cloud features are disabled")
	} else {
		client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))
		var rpErr error
		runParameters, rpErr = client.RunParameters(ctx, repositoryURL, branchName)
		if rpErr != nil {
			return nil, fmt.Errorf("failed to retrieve run parameters: %w", rpErr)
		}
		if cfg.Org == "" {
			cfg.OrgID = runParameters.OrganizationID
		}
	}

	events.RegisterMetadata("orgId", cfg.OrgID)
	events.RegisterMetadata("repoId", repositoryURL)
	events.RegisterMetadata("branchId", branchName)

	s := &scanner.Scanner{
		Plugins:         &cfg.Plugins,
		Logging:         cfg.Logging,
		Dashboard:       cfg.Dashboard,
		Currency:        in.Currency,
		PricingEndpoint: cfg.PricingEndpoint,
		PricingAPIKey:   cfg.PricingAPIKey,
		FetchAuth:       scanner.SSHFetchAuthFromValue(cfg.SSHKeyFile),
	}
	startTime := time.Now()
	result, err := s.Scan(ctx, runParameters, dir, branchName, source, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to scan target: %w", err)
	}
	runSeconds := time.Since(startTime).Seconds()

	output := format.ToOutput(result)

	eventsClient := cfg.Events.Client(api.Client(ctx, source, cfg.OrgID))

	// Diff against the previous cached result to detect fixed policy violations.
	if prev, err := store.Latest(true); err != nil {
		logging.Infof("could not load previous run data: %v", err)
	} else {
		logging.Infof("found previous run data in cache")
		output.TrackDiff(ctx, eventsClient, prev)
	}

	if err := store.Write(dir, &output); err != nil {
		logging.Warn("failed to cache results: " + err.Error())
	}

	output.TrackRun(ctx, eventsClient, runSeconds, outputFormat, nil)

	return &output, nil
}

// PriceCmd builds the cobra command. Reads IaC from stdin into PriceInput,
// authenticates, resolves the active org, then calls the pure Price
// function and dispatches the result to one of the renderers based on
// --json / --llm.
func PriceCmd(cfg *config.Config) *cobra.Command {
	var in PriceInput
	var includeWarnings bool
	cmd := &cobra.Command{
		Use:   "price",
		Short: "Read IaC from stdin, scan it, and print the cost estimate",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			iac, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("failed to read IaC from stdin: %w", err)
			}
			in.IaC = string(iac)
			in.Currency = cfg.Currency

			outputFormat := "text"
			switch {
			case cfg.LLM.Value:
				outputFormat = "llm"
			case cfg.JSON.Value:
				outputFormat = "json"
			}

			// Self-hosted pricing mode needs no login or org: the static
			// pricing API key is the only credential used, and no Infracost
			// Cloud API is contacted.
			var source oauth2.TokenSource
			if !cfg.SelfHostedPricing() {
				source, err = cfg.Auth.Token(cmd.Context())
				if err != nil {
					return fmt.Errorf("failed to log in: %w", err)
				}
				if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
					return err
				}
			}

			var result PriceResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Pricing...", "Pricing complete", func(ctx context.Context) error {
				var priceErr error
				result, priceErr = Price(ctx, cfg, source, &cfg.Cache, in, outputFormat)
				return priceErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, priceRenderers(includeWarnings))
		},
	}
	cmd.Hidden = true
	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")
	cmd.Flags().StringVar(&cfg.SSHKeyFile, "ssh-key-file", "", "Comma-separated SSH private key file(s) to use for fetching private modules over SSH (defaults to the standard ~/.ssh keys)")
	cmd.Flags().BoolVar(&includeWarnings, "include-warnings", false, "Also show warning-severity diagnostics in the summary")
	return cmd
}

func priceRenderers(includeWarnings bool) Renderers[PriceResult] {
	return Renderers[PriceResult]{
		Human: func(w io.Writer, r PriceResult) error {
			return renderPriceHuman(w, r, includeWarnings)
		},
		JSON: renderPriceJSON,
		LLM:  renderPriceLLM,
	}
}

func renderPriceHuman(w io.Writer, r PriceResult, includeWarnings bool) error {
	if err := inspect.Run(w, r, inspect.Options{}); err != nil {
		return err
	}
	printInspectHints(r, includeWarnings)
	inspect.WriteSummaryDiagnostics(w, r, includeWarnings)
	return nil
}

func renderPriceJSON(w io.Writer, r PriceResult) error {
	if err := r.ToJSON(w); err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	_, err := fmt.Fprintln(w)
	return err
}

func renderPriceLLM(w io.Writer, r PriceResult) error {
	if err := r.ToTOON(w); err != nil {
		return fmt.Errorf("failed to write LLM output: %w", err)
	}
	_, err := fmt.Fprintln(w)
	return err
}
