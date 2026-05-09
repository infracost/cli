package views

import (
	"strings"

	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
)

// RenderError formats a TUI-level error with a recovery hint. Used by
// the root model when a scan or auth attempt fails non-recoverably.
// Rendered as a centered bordered card so it visually echoes the help
// overlay — the user expects modal-feeling content to look the same.
//
// authError surfaces the in-place "press a to re-authenticate" affordance
// for auth-shaped failures; non-auth failures show the static recovery
// list instead.
//
// width is the terminal column count; the card caps at a comfortable
// reading width and is left-padded to center within the body.
func RenderError(err error, authError bool, width int) string {
	if err == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}

	lines := []string{
		styles.Bold().Render("Something went wrong."),
		"",
		styles.Danger().Render(err.Error()),
		"",
		styles.Muted().Render("Try one of:"),
	}
	if authError {
		// Lead with the in-TUI recovery so the user doesn't have to
		// quit and run `infracost auth login` themselves.
		lines = append(lines,
			styles.Muted().Render("  • ")+styles.Bold().Render("a")+styles.Muted().Render(" — re-authenticate and retry"),
		)
	}
	lines = append(lines,
		styles.Muted().Render("  • r — retry the scan"),
		styles.Muted().Render("  • run ")+styles.Bold().Render("infracost auth login")+styles.Muted().Render(" to refresh credentials"),
		styles.Muted().Render("  • run ")+styles.Bold().Render("infracost setup")+styles.Muted().Render(" to walk through configuration"),
		styles.Muted().Render("  • q — quit"),
	)
	body := strings.Join(lines, "\n")

	cardW := 70
	if cardW > width-4 {
		cardW = width - 4
	}
	if cardW < 30 {
		cardW = 30
	}
	card := styles.PaneBorderAccent().Width(cardW).Padding(1, 2).Render(body)

	leftPad := (width - ui.PrintableWidth(card)) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	prefix := strings.Repeat(" ", leftPad)

	cardLines := strings.Split(card, "\n")
	for i, l := range cardLines {
		cardLines[i] = prefix + l
	}
	return strings.Join(cardLines, "\n")
}
