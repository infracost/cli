package views

import (
	"fmt"

	"github.com/infracost/cli/internal/tui/styles"
)

// RenderError formats a TUI-level error with a recovery hint. Used by the
// root model when a scan or auth attempt fails non-recoverably.
func RenderError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("\n  %s\n  %s\n\n  %s\n",
		styles.Bold().Render("Something went wrong."),
		styles.Danger().Render(err.Error()),
		styles.Muted().Render("Run `infracost setup` (auth) or `infracost scan` to triage; press q to quit."),
	)
}
