package views

import "github.com/infracost/cli/internal/ui"

// issueIcon returns the warning glyph used to flag failing-policy
// rows. Defaults to "⚠️ " — the same emoji the CLI's scan summary
// uses (`internal/inspect.warnEmoji`) — so the TUI feels like one
// product on terminals that honor U+FE0F's emoji presentation.
//
// On wcwidth-style terminals (Apple Terminal, cmd.exe, the Linux
// console, some multiplexer configurations — detected at startup
// by ui.DetectEmojiWidth via a cursor-position probe) we substitute
// "🟡 ". The yellow-circle emoji is wide-by-default in Unicode (no
// variation selector required), so every terminal we've tested
// renders it at 2 cells, matching its measured width. Visually it
// reads as "caution" too — a yellow indicator next to the row —
// so the swap preserves the warning semantic.
//
// Both forms measure as 3 cells (2 emoji + 1 space) so the icon
// column reservation in the list and the inline alignment in the
// detail pane stay constant across modes.
func issueIcon() string {
	if ui.NarrowEmojis() {
		return "🟡 "
	}
	return "⚠️ "
}

// issueIconRendered returns the issue glyph for inline use in the
// list. Both ⚠️ and 🟡 have intrinsic colors, so no extra styling
// is needed on top.
func issueIconRendered() string { return issueIcon() }
