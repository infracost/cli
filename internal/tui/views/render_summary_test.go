package views_test

import (
	"testing"

	"github.com/infracost/cli/internal/tui/views"
)

func TestRenderSummary_Aggregate(t *testing.T) {
	got := views.RenderSummary(goldenFixture(), "", 100)
	assertGolden(t, got)
}

func TestRenderSummary_ScopedToWeb(t *testing.T) {
	// Per-project view excludes scan-level guardrails/budgets and
	// reports only the project's own resource and policy stats.
	got := views.RenderSummary(goldenFixture(), "web", 100)
	assertGolden(t, got)
}

func TestRenderSummary_Narrow(t *testing.T) {
	// At 50 columns the inline pairs wrap to multiple lines —
	// captures the wrap behavior so a future tweak to the inline
	// renderer doesn't quietly stop wrapping.
	got := views.RenderSummary(goldenFixture(), "", 50)
	assertGolden(t, got)
}

func TestRenderSummary_NilOutputReturnsEmpty(t *testing.T) {
	// Guards against an early-render race: when the model invokes
	// RenderSummary before its scan output arrives, the empty string
	// is what tells the caller to skip the surrounding bordered box
	// rather than draw an empty card.
	got := views.RenderSummary(nil, "", 100)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}
