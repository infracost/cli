package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/orgresolve"
)

// authResolvedMsg is posted after the user has successfully run
// `infracost auth login` from the error-overlay's recovery prompt.
// The model refreshes its cached token source and retries the scan
// that triggered the recovery in the first place.
type authResolvedMsg struct{ err error }

// isAuthError heuristically classifies an error as an auth/credential
// failure rather than a "couldn't reach the API" or "scan parse"
// failure. The dashboard surfaces a few different shapes; we match on
// substrings rather than typed errors because the error chain crosses
// several packages.
//
// False positives degrade UX (we'd offer auth retry for an unrelated
// failure) but are recoverable — running auth login is harmless. False
// negatives mean the user has to retry manually, also recoverable.
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unauthor",
		"unauthen",
		"401",
		"403",
		"token expired",
		"invalid token",
		"failed to log in",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// runAuthLoginCmd suspends the bubbletea program, execs the running
// binary's `auth login` subcommand, then resumes. We re-exec the
// running binary (via os.Executable) rather than depending on
// `infracost` being on PATH so this also works for `make build`
// outputs and one-off binaries.
func runAuthLoginCmd() tea.Cmd {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "infracost"
	}
	// gosec G204 false positive: bin is the path to our own running
	// binary (or the literal "infracost" fallback); arguments are
	// hard-coded. No user input flows into either, so this isn't a
	// command-injection risk.
	c := exec.Command(bin, "auth", "login") //nolint:gosec
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return authResolvedMsg{err: err}
	})
}

// refreshCredentials re-resolves the token source and active org in
// place after a successful `auth login`. Returns the fresh token
// source so the model can swap it into m.source for follow-up scans.
//
// orgresolve.Resolve here is safe even though we're back inside the
// bubbletea event loop: by the time it runs, the user has just
// completed an interactive login (which selected an org), so the
// resolution chain hits SelectedOrgID in the user cache and never
// needs the multi-org huh picker.
func refreshCredentials(ctx context.Context, cfg *config.Config) (any, error) {
	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("authenticating after login: %w", err)
	}
	if err := orgresolve.Resolve(ctx, cfg, source); err != nil {
		// Multi-org users who skipped org selection during login will
		// still hit this; the error message is actionable enough to
		// surface unmodified.
		return nil, err
	}
	if source == nil {
		return nil, errors.New("auth login completed but no token is cached — try again")
	}
	return source, nil
}
