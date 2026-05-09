package views_test

import (
	"testing"

	"github.com/infracost/cli/internal/tui/views"
)

func TestRenderHelp_Standard(t *testing.T) {
	got := views.RenderHelp(100)
	assertGolden(t, got)
}

func TestRenderHelp_Narrow(t *testing.T) {
	// Card width is capped relative to terminal width — at 50 cols
	// the help block should still render inside the available
	// space without wrapping into the borders.
	got := views.RenderHelp(50)
	assertGolden(t, got)
}
