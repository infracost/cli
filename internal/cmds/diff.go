package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// baselineHint is the two-step recipe printed when no baseline is available.
const baselineHint = `To show a diff:
  1. Save a baseline:  infracost scan --json > infracost-base.json
  2. Change your code
  3. Show the diff:    infracost diff --compare-to infracost-base.json`

// readOnlyStore reads through to the wrapped store but drops writes, so a
// diff never replaces the cached baseline it just compared against.
type readOnlyStore struct{ cache.Store }

func (readOnlyStore) Write(string, *format.Output) error { return nil }

// DiffCmd compares the current scan of a path against a saved baseline.
func DiffCmd(cfg *config.Config) *cobra.Command {
	var in ScanInput
	var compareTo string
	var options []string
	var legacy legacyDiffFlags

	cmd := &cobra.Command{
		Use:   "diff [path]",
		Short: "Show the cost change between a saved baseline and your current code",
		Example: `  # Save a baseline, change the code, then diff against it
  $ infracost scan --json > infracost-base.json
  $ infracost diff --compare-to infracost-base.json

  # Diff a specific project path
  $ infracost diff ./terraform --compare-to infracost-base.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			startTime := time.Now()

			cache.Prune(cache.DefaultPruneAge)

			if len(args) > 0 {
				in.Path = args[0]
			}
			legacy.apply(cfg, &in)
			in.Currency = cfg.Currency

			pluginOptionMap, err := parsePluginOptions(options)
			if err != nil {
				return err
			}

			outputFormat := "text"
			switch {
			case cfg.LLM.Value:
				outputFormat = "llm"
			case cfg.JSON.Value:
				outputFormat = "json"
			}

			defer func() { pushRunError(cfg, outputFormat, startTime, runErr) }()

			out := io.Writer(os.Stdout)
			if legacy.outFile != "" {
				f, err := os.Create(legacy.outFile) // #nosec G304 -- user-specified output file
				if err != nil {
					return fmt.Errorf("failed to create --out-file %q: %w", legacy.outFile, err)
				}
				defer func() { _ = f.Close() }()
				out = f
			}

			// Resolved before scanning: a missing baseline is the common
			// mistake, and failing after a full scan wastes the user's time.
			baseline, err := resolveBaseline(cfg, compareTo, in.Path)
			if err != nil {
				return err
			}

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

			if err := ui.RunWithSpinnerErr(cmd.Context(), "Checking plugins are up to date...", "Plugins up to date", func(ctx context.Context) error {
				_, ensureErr := cfg.Plugins.EnsurePlugins(ctx)
				return ensureErr
			}); err != nil {
				return err
			}

			// The scan's store drops writes: overwriting the cache here would
			// replace the baseline, so the next diff would compare the code
			// against itself.
			store := readOnlyStore{Store: &cfg.Cache}

			var current ScanResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Scanning...", "Scan complete", func(ctx context.Context) error {
				var scanErr error
				current, scanErr = Scan(ctx, cfg, source, store, in, outputFormat, pluginOptionMap)
				return scanErr
			}); err != nil {
				return err
			}

			if err := sameCurrency(baseline, current); err != nil {
				return err
			}

			if err := writeStructured(cfg, out, format.Diff(baseline, current), diffRenderers()); err != nil {
				return err
			}
			// A project that failed to parse has no resources, which would
			// otherwise read as every resource being removed.
			return criticalDiagnosticsErr(current)
		},
	}

	cmd.Flags().StringVar(&compareTo, "compare-to", "", "Path to an Infracost JSON file to compare against (defaults to the last cached scan of this path)")
	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")
	cmd.Flags().StringVar(&cfg.SSHKeyFile, "ssh-key-file", "", "Comma-separated SSH private key file(s) to use for fetching private modules over SSH (defaults to the standard ~/.ssh keys)")
	cmd.Flags().StringArrayVarP(&options, "option", "o", options, "Specify a plugin-specific option e.g. -o infracost/terraform.option=yes")

	legacy.register(cmd)

	return cmd
}

// legacyDiffFlags are the v0.10 `infracost diff` flags. They are kept so
// existing CI scripts get a deprecation notice rather than "unknown flag".
type legacyDiffFlags struct {
	path    string
	format  string
	outFile string
}

func (l *legacyDiffFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()

	f.StringVar(&l.path, "path", "", "")
	f.StringVar(&l.format, "format", "", "")
	f.StringVar(&l.outFile, "out-file", "", "")
	f.Bool("show-skipped", false, "")
	f.StringArray("terraform-var-file", nil, "")

	deprecations := map[string]string{
		"path":               "pass the path as an argument instead: `infracost diff <path>`",
		"format":             "use --json or --llm instead",
		"out-file":           "redirect stdout instead",
		"show-skipped":       "all resources are now shown",
		"terraform-var-file": "this flag is no longer supported",
	}
	for name, msg := range deprecations {
		_ = f.MarkDeprecated(name, msg)
	}
}

// apply maps the legacy flags that still have an equivalent onto the current
// ones; the rest are accepted and ignored.
func (l *legacyDiffFlags) apply(cfg *config.Config, in *ScanInput) {
	if in.Path == "" {
		in.Path = l.path
	}
	if l.format == "json" {
		cfg.JSON.Value = true
	}
}

// sameCurrency rejects a baseline priced in another currency: the diff
// subtracts the two sides, so an FX gap would read as a cost change.
func sameCurrency(past, current *format.Output) error {
	if past.Currency == "" || current.Currency == "" || past.Currency == current.Currency {
		return nil
	}
	return fmt.Errorf("baseline is priced in %s but this scan is priced in %s: re-run with `--currency %s`, or save a new baseline",
		past.Currency, current.Currency, past.Currency)
}

// resolveBaseline loads --compare-to, or falls back to the cached result for
// this path. Age and source-file changes are ignored: the point is to compare
// against the run before the edit. `infracost cache prune` and the automatic
// 24h prune bound how old that run can be.
func resolveBaseline(cfg *config.Config, compareTo, path string) (*format.Output, error) {
	if compareTo != "" {
		return loadBaselineFile(compareTo)
	}

	target := path
	if target == "" {
		target = "."
	}
	absolutePath, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path to target: %w", err)
	}

	cached, err := cfg.Cache.ForPathAny(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("no baseline to compare against (%v): pass --compare-to, or run `infracost scan` first.\n\n%s", err, baselineHint)
	}
	return cached, nil
}

func loadBaselineFile(path string) (*format.Output, error) {
	f, err := os.Open(path) // #nosec G304 -- user-specified baseline file
	if err != nil {
		return nil, fmt.Errorf("failed to open --compare-to file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out format.Output
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to parse --compare-to file %q as Infracost JSON: %w", path, err)
	}
	// Unknown fields decode silently, so an unrelated JSON file (a v0.10
	// breakdown, say) would otherwise read as an empty baseline.
	if len(out.Projects) == 0 {
		return nil, fmt.Errorf("--compare-to file %q has no projects: it is not an `infracost scan --json` output", path)
	}
	return &out, nil
}

func diffRenderers() Renderers[*format.DiffOutput] {
	return Renderers[*format.DiffOutput]{
		Human: func(w io.Writer, d *format.DiffOutput) error {
			return inspect.WriteDiff(w, d)
		},
		JSON: func(w io.Writer, d *format.DiffOutput) error {
			if err := d.ToJSON(w); err != nil {
				return fmt.Errorf("failed to write JSON output: %w", err)
			}
			_, err := fmt.Fprintln(w)
			return err
		},
		LLM: func(w io.Writer, d *format.DiffOutput) error {
			if err := d.ToTOON(w); err != nil {
				return fmt.Errorf("failed to write LLM output: %w", err)
			}
			_, err := fmt.Fprintln(w)
			return err
		},
	}
}
