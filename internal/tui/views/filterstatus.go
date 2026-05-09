package views

import (
	"strings"

	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
)

// RenderFilterStatus formats the inline filter banner displayed at the top
// of the list pane when a filter is committed. It mirrors the shape of the
// filter input (so the user sees the same visual block where they typed)
// and surfaces the clear hint right next to the active expression.
//
// Layout: brand-colored "/expr" on the left, muted "esc to clear" on the
// right, padded out to width columns.
func RenderFilterStatus(expr string, width int) string {
	if width <= 0 {
		width = 80
	}
	left := styles.Bold().Foreground(ui.BrandColor).Render("/ " + expr)
	right := styles.Muted().Italic(true).Render("esc to clear")

	gap := width - ui.PrintableWidth(left) - ui.PrintableWidth(right)
	if gap < 1 {
		// On very narrow panes drop the hint rather than breaking
		// layout; the user can still see the filter expression itself,
		// and the status bar still carries the clear hint.
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}
