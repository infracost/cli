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

// Sides of the comparison, used in the diagnostics error.
const (
	diffSideBaseline = "baseline"
	diffSideCurrent  = "current scan"
)

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

			// Distinct from `scan`'s labels so diff adoption is visible in
			// telemetry instead of inflating scan counts.
			outputFormat := "diff"
			switch {
			case cfg.LLM.Value:
				outputFormat = "diff-llm"
			case cfg.JSON.Value:
				outputFormat = "diff-json"
			}

			defer func() { pushRunError(cfg, outputFormat, startTime, runErr) }()

			// Before scanning: a missing baseline should fail fast.
			baseline, err := resolveBaseline(cfg, compareTo, in.Path)
			if err != nil {
				return err
			}
			if err := diffDiagnosticsErr(cmd.ErrOrStderr(), diffSideBaseline, baseline); err != nil {
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

			// Drops writes: a cache write here would replace the baseline,
			// so the next diff would compare the code against itself.
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

			// Before rendering, unlike `scan`: a project that failed to parse
			// has no resources, so the diff would read as a mass deletion.
			if err := diffDiagnosticsErr(cmd.ErrOrStderr(), diffSideCurrent, current); err != nil {
				return err
			}

			// Opened last: a failed login, plugin check or scan must not leave
			// an empty file behind, which for `--out-file base.json --compare-to
			// base.json` would destroy the baseline.
			out := io.Writer(os.Stdout)
			if legacy.outFile != "" {
				f, err := os.Create(legacy.outFile) // #nosec G304 -- user-specified output file
				if err != nil {
					return fmt.Errorf("failed to create --out-file %q: %w", legacy.outFile, err)
				}
				defer func() { _ = f.Close() }()
				out = f
			}

			return writeStructured(cfg, out, format.Diff(baseline, current), diffRenderers())
		},
	}

	cmd.Flags().StringVar(&compareTo, "compare-to", "", "Path to an Infracost JSON file to compare against (defaults to the last cached scan of this path)")
	cmd.Flags().StringVar(&cfg.Currency, "currency", "", "ISO 4217 currency code to use for prices (e.g. USD, EUR, GBP)")
	cmd.Flags().StringVar(&cfg.SSHKeyFile, "ssh-key-file", "", "Comma-separated SSH private key file(s) to use for fetching private modules over SSH (defaults to the standard ~/.ssh keys)")
	cmd.Flags().StringArrayVarP(&options, "option", "o", options, "Specify a plugin-specific option e.g. -o infracost/terraform.option=yes")

	legacy.register(cmd)

	return cmd
}

// legacyDiffFlags are the v0.10 `infracost diff` flags, kept so existing CI
// scripts get a deprecation notice rather than "unknown flag".
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

// diffDiagnosticsErr stops the diff when either side has a critical
// diagnostic, printing the messages the diff model would otherwise drop.
func diffDiagnosticsErr(stderr io.Writer, side string, r *format.Output) error {
	entries := inspect.CollectDiagnostics(r, false)
	if len(entries) == 0 {
		return nil
	}
	inspect.WriteSummaryDiagnostics(stderr, r, false)
	word := "diagnostics"
	if len(entries) == 1 {
		word = "diagnostic"
	}

	// Only the baseline is worth re-saving. Saving one from broken current
	// code would fail the same way, or hide the change the user came for.
	advice := "fix the errors above and re-run."
	if side == diffSideBaseline {
		advice = "fix the errors above, then save a new baseline.\n\n" + baselineHint
	}
	return fmt.Errorf("the %s has %d critical %s, so the comparison would be wrong: %s",
		side, len(entries), word, advice)
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

// resolveBaseline loads --compare-to, else the cached result for this path.
// Age and source changes are ignored: compare against the run before the edit.
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
	b, err := os.ReadFile(path) // #nosec G304 -- user-specified baseline file
	if err != nil {
		return nil, fmt.Errorf("failed to open --compare-to file %q: %w", path, err)
	}

	var out format.Output
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("failed to parse --compare-to file %q as Infracost JSON: %w", path, err)
	}
	// Unknown fields decode silently, so an unrelated JSON file would
	// otherwise read as an empty baseline.
	if len(out.Projects) == 0 {
		return nil, fmt.Errorf("--compare-to file %q has no projects: it is not an `infracost scan --json` output", path)
	}
	if isDiffJSON(b) {
		return nil, fmt.Errorf("--compare-to file %q is `infracost diff` output, not `infracost scan --json` output: its costs would read as zero.\n\n%s", path, baselineHint)
	}
	return &out, nil
}

// isDiffJSON reports whether a baseline file is this command's own output.
// It shares the `projects` key with a scan, but names costs differently, so
// decoding it as a scan silently zeroes every cost.
func isDiffJSON(b []byte) bool {
	var probe struct {
		DiffTotal json.RawMessage `json:"diff_total_monthly_cost"`
		Projects  []struct {
			Op json.RawMessage `json:"op"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	if len(probe.DiffTotal) > 0 {
		return true
	}
	return len(probe.Projects) > 0 && len(probe.Projects[0].Op) > 0
}

func diffRenderers() Renderers[*format.DiffOutput] {
	return Renderers[*format.DiffOutput]{
		Human: inspect.WriteDiff,
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
