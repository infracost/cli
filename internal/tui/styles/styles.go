// Package styles centralizes lipgloss styles for the TUI. Color sources
// import from internal/ui — never duplicate hex values, since the brand
// palette and dark/light handling already live there.
package styles

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/infracost/cli/internal/ui"
)

// brand returns the active brand color as a lipgloss.Color.
func brand() lipgloss.Color {
	if ui.HasDarkBackground() {
		return lipgloss.Color("#6C70F2")
	}
	return lipgloss.Color("#4F46E5")
}

// muted returns the active muted color.
func muted() lipgloss.Color {
	if ui.HasDarkBackground() {
		return lipgloss.Color("#737487")
	}
	return lipgloss.Color("#6B7280")
}

// accent returns the active accent color.
func accent() lipgloss.Color {
	return lipgloss.Color("#0EA5E9")
}

// danger returns the active danger color.
func danger() lipgloss.Color {
	return lipgloss.Color("#F87171")
}

// Common styles. Allocated lazily so tests that flip ui.ColorEnabled don't
// see stale instances.
func ListSelected() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(brand()).Bold(true)
}

func ListRow() lipgloss.Style {
	return lipgloss.NewStyle()
}

func StatusBar() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(muted())
}

func StatusActive() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(brand()).Bold(true)
}

func Cost() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(accent())
}

func Muted() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(muted())
}

func Danger() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(danger())
}

func Bold() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true)
}

func PaneBorder() lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(muted())
}

// PaneBorderAccent is a rounded border in the brand color, used to make a
// pane visually distinct from the muted-border list/detail panes (e.g. the
// summary block at the top of the main view).
func PaneBorderAccent() lipgloss.Style {
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(brand())
}

// PaneBorderFocused renders the active split pane's border in a thick
// white line. Pairs with PaneBorderDimmed (used on the inactive pane
// when the detail side has focus) so the contrast is in line weight
// + brightness rather than relying on the brand color, which the
// summary box above already owns.
func PaneBorderFocused() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.ThickBorder()).
		BorderForeground(lipgloss.Color("#FFFFFF"))
}

// PaneBorderDimmed renders the inactive pane in a deliberately faded
// rounded border. Used to "demote" the resource list when the detail
// pane is focused — combined with the Faint render below it pushes
// the list visually into the background so the eye lands on the
// detail content the user is actively reading.
func PaneBorderDimmed() lipgloss.Style {
	color := lipgloss.Color("#3a3a4a")
	if !ui.HasDarkBackground() {
		color = lipgloss.Color("#cfd1d6")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(color)
}

// Dimmed wraps content in the ANSI faint attribute so the inactive
// pane's content reads as "background context" while the focused
// pane's content stays full-strength.
func Dimmed() lipgloss.Style { return lipgloss.NewStyle().Faint(true) }


// PaneTitle styles a one-word label inserted into a pane's top border —
// used by the list / detail panes so the user can tell at a glance which
// pane has focus.
func PaneTitle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(brand()).Bold(true)
}
