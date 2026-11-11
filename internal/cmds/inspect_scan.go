package cmds

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
	"golang.org/x/oauth2"
)

// scanForInspect handles an `infracost inspect` with nothing cached to inspect.
//
// It is the most common error people and agents hit after a successful login:
// inspect reads what scan wrote, and nothing said so. An interactive terminal
// is offered the scan and carries on with its results. A non-interactive run
// is given the exact command for this path instead of running it: a scan
// contacts the pricing API and uploads a run, and an agent loop should not
// start that on a command that only reads.
func scanForInspect(ctx context.Context, cfg *config.Config, absPath, displayPath string) (*format.Output, error) {
	if !ui.IsInteractive() {
		return nil, noCachedResultsError(displayPath)
	}

	var confirm bool
	if err := huh.NewConfirm().
		Title(fmt.Sprintf("Nothing has been scanned yet. Scan %s now?", displayPath)).
		Affirmative("Yes").
		Negative("No").
		Value(&confirm).
		WithTheme(ui.BrandTheme()).
		Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, noCachedResultsError(displayPath)
		}
		return nil, err
	}
	if !confirm {
		return nil, noCachedResultsError(displayPath)
	}

	return scanNow(ctx, cfg, absPath, "inspect")
}

// scanNow runs a scan the way `scan` would, caching the result, for a command
// that found nothing cached and was told to go and get it.
func scanNow(ctx context.Context, cfg *config.Config, absPath, outputFormat string) (*format.Output, error) {
	// Self-hosted pricing mode needs no login or org: the static pricing API
	// key is the only credential used. Same condition as ScanCmd.
	var source oauth2.TokenSource
	if !cfg.SelfHostedPricing() {
		var err error
		source, err = cfg.Auth.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to log in: %w", err)
		}
		if err := resolveOrg(ctx, cfg, source); err != nil {
			return nil, err
		}
	}

	if err := ui.RunWithSpinnerErr(ctx, "Checking plugins are up to date...", "Plugins up to date", func(ctx context.Context) error {
		_, ensureErr := cfg.Plugins.EnsurePlugins(ctx)
		return ensureErr
	}); err != nil {
		return nil, err
	}

	in := ScanInput{Path: absPath, Currency: cfg.Currency}
	var result ScanResult
	if err := ui.RunWithSpinnerErr(ctx, "Scanning...", "Scan complete", func(ctx context.Context) error {
		var scanErr error
		result, scanErr = Scan(ctx, cfg, source, &cfg.Cache, in, outputFormat, nil)
		return scanErr
	}); err != nil {
		return nil, err
	}

	return result, nil
}

// noCachedResultsError names the command that fixes it, for this path rather
// than a placeholder.
func noCachedResultsError(displayPath string) error {
	return fmt.Errorf("no cached results found, run 'infracost scan %s' first", displayPath)
}
