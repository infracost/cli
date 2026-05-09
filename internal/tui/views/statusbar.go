package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
)

// StatusBarData carries everything the bottom status bar renders.
//
// Cost / currency are intentionally absent — the summary box at the
// top of the main view already surfaces total monthly cost (and shows
// per-project totals when the user tabs through projects), so
// duplicating it in the footer would just waste cells.
type StatusBarData struct {
	// Project is the user-facing label for the active scan target. Usually
	// the directory name; multi-project repos can pass a project-specific
	// label.
	Project string

	// CacheAge is how stale the displayed Output is. Zero means "freshly
	// scanned this session".
	CacheAge time.Duration

	// FromCache marks whether the data came from the on-disk cache.
	FromCache bool

	// Filter is the current /-filter expression, or "" when none.
	Filter string

	// Stage, when non-empty, surfaces a progress indicator (e.g.
	// "Scanning..."). Used during a refresh so the user can see
	// something is happening.
	Stage string

	// SortLabel describes the active sort mode (e.g. "cost ↓"). Empty hides
	// the sort indicator.
	SortLabel string

	// SpinnerView, when non-empty, is prepended to the Stage text. Caller
	// passes m.spinner.View() so the statusbar animates without owning a
	// spinner of its own. Ignored when Stage is empty.
	SpinnerView string

	// Shortcuts overrides the right-side keymap text. Empty falls back
	// to the default "main view" keymap. Use this for views where the
	// default shortcuts don't apply — the project picker, for instance,
	// has no meaningful "refresh" or "sort", but does want / and ?.
	Shortcuts string
}

// RenderStatusBar formats the status bar at the bottom of the TUI to fit
// within width columns.
func RenderStatusBar(d StatusBarData, width int) string {
	if width <= 0 {
		width = 80
	}

	left := []string{}
	if d.Project != "" {
		left = append(left, styles.StatusActive().Render(d.Project))
	}
	if d.Stage != "" {
		stage := d.Stage
		if d.SpinnerView != "" {
			stage = d.SpinnerView + " " + stage
		}
		left = append(left, styles.StatusActive().Render(stage))
	}
	if d.FromCache {
		label := "cached"
		if d.CacheAge > 0 {
			label = fmt.Sprintf("cached %s ago", humanizeAge(d.CacheAge))
		}
		left = append(left, styles.Muted().Render(label))
	}
	// Filter status used to display here, but it was too easy to miss in
	// the footer — the inline banner inside the list pane now carries
	// that signal. The right-hand shortcut list still gains an "esc clear
	// filter" hint when a filter is active, as a fallback.
	if d.SortLabel != "" {
		left = append(left, styles.Muted().Render("sort: "+d.SortLabel))
	}

	// Compact hints with bullet separators read more naturally than the
	// triple-space-separated list and pack tighter, which matters when
	// the left side is busy: a full hint list of "/ filter   ↑↓ nav …"
	// blew past 80 columns and the terminal would silently chop "quit"
	// off the right edge. Middle dots also visually group each chord.
	hints := d.Shortcuts
	if hints == "" {
		hints = "/ filter · ↑↓ nav · r refresh · ? help · q quit"
	}
	if d.Filter != "" {
		hints = "esc clear · " + hints
	}
	right := styles.Muted().Render(hints)

	leftStr := strings.Join(left, "  ")

	// Right-side hints are the always-discoverable affordance — when
	// space gets tight, prefer to truncate the left status entries
	// (project name, cost, sort label) rather than chop the keymap.
	totalNeeded := ui.PrintableWidth(leftStr) + ui.PrintableWidth(right) + 1
	if totalNeeded > width {
		avail := width - ui.PrintableWidth(right) - 1
		if avail > 0 {
			leftStr = inspect.TruncateEnd(leftStr, avail)
		} else {
			// Even the hints alone don't fit; keep the rightmost part
			// (which carries the most-used keys) and drop the rest.
			leftStr = ""
			right = inspect.TruncateEnd(right, width)
		}
	}

	gap := width - ui.PrintableWidth(leftStr) - ui.PrintableWidth(right)
	if gap < 1 {
		gap = 1
	}
	return leftStr + strings.Repeat(" ", gap) + right
}

// humanizeAge renders a Duration as "12s", "4m", "2h", "3d" — the same
// idiom commonly used in CLIs. Anything under 1 second rounds to "0s".
func humanizeAge(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
