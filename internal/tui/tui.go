// Package tui implements the interactive Bubble Tea interface that runs when
// `infracost` is invoked with no subcommand on a real terminal. The TUI loads
// or runs a scan of the current directory and lets the user navigate the
// resulting resources, drill into per-component cost breakdowns, filter, and
// switch between projects.
//
// The package's public surface is intentionally tiny — Run is the entry point
// from main.go. Implementation details live in subpackages (app, views,
// styles, discovery) that the rest of the codebase shouldn't import directly.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/orgresolve"
	"github.com/infracost/cli/internal/tui/app"
	"github.com/infracost/cli/internal/ui"
)

// Run starts the TUI and blocks until the user quits or an unrecoverable
// error occurs.
//
// Auth + org resolution happens BEFORE we hand control to bubbletea: both
// can prompt interactively (browser auth, multi-org huh picker), and huh
// opens /dev/tty in raw mode — which would deadlock if we tried to do it
// from inside a tea.Program also holding /dev/tty. Step 9 will introduce
// tea.Exec for runtime re-auth; for now the prompts run on the regular
// terminal before alt-screen takes over.
//
// We also probe the terminal's emoji-width behavior here, while we still
// own the TTY exclusively — bubbletea would otherwise consume the
// cursor-position-report response we need to read back. The result is
// cached globally and consulted later when picking which warning glyph
// (⚠️ vs bare ⚠) to render in bordered panes.
func Run(ctx context.Context, cfg *config.Config, version string) error {
	ui.DetectEmojiWidth()

	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}
	if err := orgresolve.Resolve(ctx, cfg, source); err != nil {
		return err
	}

	model := app.NewModel(ctx, cfg, version, app.WithSource(source))
	p := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithAltScreen(),
	)
	_, err = p.Run()
	return err
}

