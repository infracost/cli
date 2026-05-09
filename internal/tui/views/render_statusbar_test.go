package views_test

import (
	"testing"
	"time"

	"github.com/infracost/cli/internal/tui/views"
)

func TestRenderStatusBar_DefaultMain(t *testing.T) {
	// Project label + cache age + sort + default keymap. Captures
	// the canonical layout users see most of the time.
	got := views.RenderStatusBar(views.StatusBarData{
		Project:   "demo-project",
		CacheAge:  4 * time.Minute,
		FromCache: true,
		SortLabel: "cost ↓",
	}, 100)
	assertGolden(t, got)
}

func TestRenderStatusBar_FilterPrefixesEscClear(t *testing.T) {
	// Active filter swaps in the "esc clear ·" hint at the front of
	// the keymap so the user sees the way out of filtered state
	// regardless of which view they're in.
	got := views.RenderStatusBar(views.StatusBarData{
		Project: "demo-project",
		Filter:  "rds",
	}, 100)
	assertGolden(t, got)
}

func TestRenderStatusBar_PickerShortcuts(t *testing.T) {
	// Picker view passes its own shortcut string through the
	// Shortcuts override — the default "r refresh" / "s sort" don't
	// apply there.
	got := views.RenderStatusBar(views.StatusBarData{
		Shortcuts: "/ filter · ↑↓ nav · enter select · ? help · q quit",
	}, 100)
	assertGolden(t, got)
}

func TestRenderStatusBar_TruncatesLeftWhenCrowded(t *testing.T) {
	// Right-side hints take priority over left-side status entries —
	// when total width is tight the left should truncate (with `…`)
	// instead of pushing the keymap off the right edge.
	got := views.RenderStatusBar(views.StatusBarData{
		Project:   "an-extremely-long-project-name-that-eats-into-the-row",
		CacheAge:  2 * time.Hour,
		FromCache: true,
		SortLabel: "cost ↓",
	}, 60)
	assertGolden(t, got)
}
