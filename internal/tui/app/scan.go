package app

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/scanrun"
	"golang.org/x/oauth2"
)

// scanDoneMsg signals a successful scan; the model swaps in Output and
// transitions to ViewMain.
type scanDoneMsg struct {
	output    *format.Output
	cwd       string
	fromCache bool
}

// scanErrMsg signals a failed scan. The model transitions to ViewError
// rendering the message + a hint about how to recover.
type scanErrMsg struct {
	err error
}

// scanStartedMsg flips the status bar into a "Scanning..." indicator.
// Posted by scanCmd before it begins the heavy work so the user sees
// activity even if the first stages take a moment.
type scanStartedMsg struct{}

// scanCmd runs scanrun.Run for absDir and returns scanDoneMsg or
// scanErrMsg. Bypass forces a fresh scan even if the directory's cache
// entry is still fresh.
//
// Auth + org resolution happens BEFORE the TUI starts (in tui.Run) so
// scanCmd can run safely from a goroutine without prompting huh and
// deadlocking on /dev/tty. The token source is captured into the model
// and threaded in here.
func scanCmd(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, absDir string, bypass bool) tea.Cmd {
	return func() tea.Msg {
		if source == nil {
			return scanErrMsg{err: fmt.Errorf("internal error: scan invoked without a token source — this is a bug")}
		}
		result, err := scanrun.Run(ctx, cfg, scanrun.Options{
			AbsoluteDir: absDir,
			Source:      source,
			Bypass:      bypass,
		})
		if err != nil {
			return scanErrMsg{err: err}
		}
		return scanDoneMsg{
			output:    result.Output,
			cwd:       absDir,
			fromCache: result.FromCache,
		}
	}
}
