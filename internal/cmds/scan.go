package cmds

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	pkgscanner "github.com/infracost/cli/pkg/scanner"
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
func Scan(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, store cache.Store, in ScanInput, outputFormat string, pluginOpts pkgscanner.PluginOpts) (ScanResult, error) {
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

	// Self-hosted pricing mode runs the scan without Infracost Cloud: run
	// parameters stay zero-valued, so no policies, guardrails, budgets, usage
	// defaults or config templates apply.
	var runParameters dashboard.RunParameters
	if cfg.SelfHostedPricing() {
		if err := cfg.ValidateSelfHostedPricing(); err != nil {
			return nil, err
		}
		logging.Infof("INFRACOST_CLI_PRICING_API_KEY is set: scanning against the self-hosted pricing API only; Infracost Cloud features (policies, guardrails, budgets, usage defaults) are disabled")
	} else {
		client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))

		var err error
		runParameters, err = client.RunParameters(ctx, repositoryURL, branchName)
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
	result, err := s.Scan(ctx, runParameters, absolutePath, branchName, source, pluginOpts)
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
	var diff bool
	var options []string
	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Scan your IaC and derive FinOps costs and policy violations",
		Example: `  # Scan the current directory
  $ infracost scan

  # Scan a specific project path
  $ infracost scan ./terraform

  # Scan against a different organization's policies & prices
  $ infracost scan --org acme

  # Show the cost diff embedded in a Terraform plan JSON file
  $ terraform show -json plan.tfplan > plan.json
  $ infracost scan plan.json --diff --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			startTime := time.Now()

			cache.Prune(cache.DefaultPruneAge)

			if len(args) > 0 {
				in.Path = args[0]
			}

			pluginOptionMap, err := parsePluginOptions(options)
			if err != nil {
				return err
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

			if err := validateDiffFlags(diff, cfg); err != nil {
				return err
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

			// Ensure plugins are present/updated as a distinct phase so the
			// spinner reflects what's actually happening (plugin downloads emit
			// their own "Downloaded <plugin> <version>" lines above this one).
			// Scan() ensures plugins lazily too, but that call is cached so this
			// is a no-op by the time we scan.
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Checking plugins are up to date...", "Plugins up to date", func(ctx context.Context) error {
				_, ensureErr := cfg.Plugins.EnsurePlugins(ctx)
				return ensureErr
			}); err != nil {
				return err
			}

			if diff {
				var diffResult *format.ScanDiffOutput
				var result ScanResult
				if err := ui.RunWithSpinnerErr(cmd.Context(), "Scanning...", "Scan complete", func(ctx context.Context) error {
					var scanErr error
					diffResult, result, scanErr = ScanDiff(ctx, cfg, source, &cfg.Cache, in, outputFormat, pluginOptionMap)
					return scanErr
				}); err != nil {
					return err
				}
				if err := diffResult.ToJSON(os.Stdout); err != nil {
					return fmt.Errorf("failed to write JSON output: %w", err)
				}
				fmt.Println()
				return criticalDiagnosticsErr(result)
			}

			var result ScanResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Scanning...", "Scan complete", func(ctx context.Context) error {
				var scanErr error
				result, scanErr = Scan(ctx, cfg, source, &cfg.Cache, in, outputFormat, pluginOptionMap)
				return scanErr
			}); err != nil {
				return err
			}
			if err := writeStructured(cfg, os.Stdout, result, scanRenderers(includeWarnings)); err != nil {
				return err
			}
			return criticalDiagnosticsErr(result)
		},
	}

	cmd.Flags().BoolVar(&diff, "diff", false, "Show the cost difference between the plan's prior state and planned state (Terraform plan JSON files only, requires --json and self-hosted pricing mode)")
	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")
	cmd.Flags().StringVar(&cfg.SSHKeyFile, "ssh-key-file", "", "Comma-separated SSH private key file(s) to use for fetching private modules over SSH (defaults to the standard ~/.ssh keys)")
	cmd.Flags().BoolVar(&includeWarnings, "include-warnings", false, "Also show warning-severity diagnostics in the summary")
	cmd.Flags().StringArrayVarP(&options, "option", "o", options, "Specify a plugin-specific option e.g. -o infracost/terraform.option=yes")

	return cmd
}

// parsePluginOptions turns repeated --option/-o flags into a nested blob keyed
// by plugin name, e.g. "terraform.sourceMap.http=https" becomes
// {"terraform": {"sourceMap": {"http": "https"}}}. Dots delimit nesting and an
// option without "=" is treated as the boolean true (e.g. "-o terraform.foo").
// The exact keys accepted are plugin-specific; see the relevant plugin's docs.
func parsePluginOptions(options []string) (pkgscanner.PluginOpts, error) {
	pluginOptionMap := make(pkgscanner.PluginOpts)
	for _, option := range options {
		var rawVal any = true
		key, value, ok := strings.Cut(option, "=")
		if ok {
			rawVal = value
		}

		keyParts := strings.Split(key, ".")
		plugin := keyParts[0]
		keyParts = keyParts[1:]

		if len(keyParts) == 0 {
			return nil, fmt.Errorf("option %s is missing a key", option)
		}

		if pluginOptionMap[plugin] == nil {
			pluginOptionMap[plugin] = make(map[string]any)
		}

		target := pluginOptionMap[plugin]
		for i, part := range keyParts {
			if i == len(keyParts)-1 {
				// Last part, set the value
				target[part] = rawVal
				break
			}
			if existing, ok := target[part]; !ok {
				target[part] = make(map[string]any)
			} else if _, ok := existing.(map[string]any); !ok {
				return nil, fmt.Errorf("option %s is not a map, cannot set %s", strings.Join(keyParts[:i+1], "."), strings.Join(keyParts[i+1:], "."))
			}

			target = target[part].(map[string]any)
		}
	}
	return pluginOptionMap, nil
}

// validateDiffFlags rejects --diff without --json. The diff has no human or
// LLM rendering yet, so rather than silently falling back to the regular scan
// output, require the caller to pick the only supported format.
//
// It also rejects --diff outside self-hosted pricing mode: an Infracost Cloud
// run scans the current state with real RunParameters (usage defaults, config
// template) while the prior state is scanned with zero values, so an
// unchanged usage-based resource would show a phony cost change. Until the
// prior scan shares the current scan's RunParameters, fail clearly instead of
// quietly returning wrong numbers.
func validateDiffFlags(diff bool, cfg *config.Config) error {
	if !diff {
		return nil
	}
	if cfg.LLM.Value {
		return fmt.Errorf("--diff does not support --llm output yet, use --json")
	}
	if !cfg.JSON.Value {
		return fmt.Errorf("--diff currently requires --json output")
	}
	if !cfg.SelfHostedPricing() {
		return fmt.Errorf("--diff currently requires self-hosted pricing mode (set INFRACOST_CLI_PRICING_API_KEY): Infracost Cloud usage defaults are not yet applied to the plan's prior state, which would skew the diff")
	}
	return nil
}

// criticalDiagnosticsErr returns an error when any project in the result
// carries a critical-severity diagnostic. A project that failed to parse has
// no tree, so its costs and policy results are absent — without this the run
// would exit 0 looking like a successful $0 scan. Returned after rendering so
// the full output (including --json/--llm) is still written before the
// non-zero exit.
func criticalDiagnosticsErr(r ScanResult) error {
	n := len(inspect.CollectDiagnostics(r, false))
	if n == 0 {
		return nil
	}
	word := "diagnostics"
	if n == 1 {
		word = "diagnostic"
	}
	return fmt.Errorf("scan completed with %d critical %s", n, word)
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
