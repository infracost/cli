package views

import "github.com/infracost/cli/internal/ui"

// RenderHeader returns the banner string at the top of the TUI, matching
// what `infracost setup` shows. Wrapping ui.Banner here keeps the
// dependency on internal/ui contained to one place so the rest of the
// view code stays presentation-tier.
func RenderHeader(version string) string {
	return ui.Banner(version)
}
