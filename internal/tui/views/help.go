package views

import (
	"strings"

	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
)

// helpEntry is one row in the help overlay: a key combo and what it does.
type helpEntry struct {
	keys string
	desc string
}

// helpEntries is the canonical list of keybindings shown by the help
// overlay. Keep in sync with the actual handlers in app.Model.Update.
var helpEntries = []helpEntry{
	{"↑ / k", "Move selection up"},
	{"↓ / j", "Move selection down"},
	{"home / g", "Jump to first resource"},
	{"end / G", "Jump to last resource"},
	{"pgup / pgdown", "Page up / page down"},
	{"", ""},
	{"/", "Filter resources (substring or key=value)"},
	{"esc", "Clear filter / dismiss this help"},
	{"tab", "Switch project (multi-project scans)"},
	{"s", "Cycle sort: cost ↓ → address ↑ → type ↑"},
	{"r", "Refresh — re-run the scan, bypassing the cache"},
	{"", ""},
	{"?", "Toggle this help"},
	{"q / ctrl+c", "Quit"},
}

// RenderHelp returns the help overlay block: a bordered card centered
// horizontally within width, listing every keybinding in the TUI.
// The caller is responsible for vertical placement (the parent layout
// pads above/below to position the card on screen).
func RenderHelp(width int) string {
	if width <= 0 {
		width = 80
	}

	// Compute the longest "keys" column so descriptions line up.
	maxKey := 0
	for _, e := range helpEntries {
		if w := ui.PrintableWidth(e.keys); w > maxKey {
			maxKey = w
		}
	}

	var lines []string
	lines = append(lines,
		styles.Bold().Render("Keyboard shortcuts"),
		"",
	)
	for _, e := range helpEntries {
		if e.keys == "" && e.desc == "" {
			lines = append(lines, "")
			continue
		}
		key := styles.PaneTitle().Render(padRight(e.keys, maxKey))
		lines = append(lines, "  "+key+"   "+e.desc)
	}
	lines = append(lines,
		"",
		styles.Muted().Italic(true).Render("Press ? or esc to close."),
	)

	body := strings.Join(lines, "\n")

	// Cap card width so the help block doesn't span the whole terminal
	// on wide windows — easier to scan as a column.
	cardW := 64
	if cardW > width-4 {
		cardW = width - 4
	}
	card := styles.PaneBorderAccent().Width(cardW).Padding(1, 2).Render(body)

	// Center horizontally by padding either side. Vertical placement is
	// handled by the parent (which positions the help block within the
	// available body height).
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
