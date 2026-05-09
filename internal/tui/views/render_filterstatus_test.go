package views_test

import (
	"testing"

	"github.com/infracost/cli/internal/tui/views"
)

func TestRenderFilterStatus_Standard(t *testing.T) {
	// Wide enough to fit "/ aws_rds" + "esc to clear". Captures
	// the in-list-pane filter banner shown after the user commits
	// a filter (`/` typing → enter).
	got := views.RenderFilterStatus("aws_rds", 60)
	assertGolden(t, got)
}

func TestRenderFilterStatus_Narrow(t *testing.T) {
	// Narrow widths drop the "esc to clear" hint to keep the
	// filter expression itself visible — users still have the
	// status-bar hint as a fallback.
	got := views.RenderFilterStatus("aws_rds", 14)
	assertGolden(t, got)
}
