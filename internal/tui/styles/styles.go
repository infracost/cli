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

// PaneTitle styles a one-word label inserted into a pane's top border —
// used by the list / detail panes so the user can tell at a glance which
// pane has focus.
func PaneTitle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(brand()).Bold(true)
}
