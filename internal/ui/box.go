package ui

import (
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// MaxBoxWidth caps how wide Box will draw on ultrawide terminals — beyond
// this it's mostly empty space to the right of the content.
const MaxBoxWidth = 120

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// variationSelector16 (U+FE0F) forces emoji-presentation of the preceding
// base codepoint. It's used in PrintableWidth to bump the cell count, since
// runewidth doesn't credit it but terminals widen the rendered glyph to 2.
const variationSelector16 = "️"

// PrintableWidth returns the visible column count of s, ignoring ANSI escape
// codes. Uses runewidth for base width then bumps by 1 per VS-16 selector:
// runewidth (v0.0.16) doesn't credit U+FE0F for promoting an ambiguous-width
// base (e.g. ⚠ U+26A0) to emoji presentation, but every modern terminal
// renders the resulting glyph at 2 cells. Without this bump, ⚠️ measures as
// 1 and box/table borders drift right by one cell on each emoji-bearing line.
func PrintableWidth(s string) int {
	stripped := ansiRE.ReplaceAllString(s, "")
	return runewidth.StringWidth(stripped) + strings.Count(stripped, variationSelector16)
}

// terminalWidth returns the column width of stdout, or 0 if stdout is not a
// terminal or the size can't be read. Callers fall back to content width when
// this is 0 so non-TTY output (tests, pipes) stays deterministic.
func terminalWidth() int {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil {
		return 0
	}
	return w
}

// Box wraps content in a rounded muted border with internal padding. Content
// lines may include ANSI color codes; those don't count toward width.
//
// Width: terminal width (capped at MaxBoxWidth) when stdout is a TTY, otherwise
// just wide enough to fit the content. The box never shrinks below content
// width — long lines push the box wider rather than wrapping.
func Box(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	contentMax := 0
	for _, line := range lines {
		contentMax = max(contentMax, PrintableWidth(line))
	}

	const leftPad, rightPad = 3, 3
	innerW := leftPad + contentMax + rightPad
	if tw := terminalWidth(); tw > 0 {
		// Leave 2 cols for the border chars themselves.
		target := min(tw, MaxBoxWidth) - 2
		innerW = max(innerW, target)
	}

	horiz := strings.Repeat("─", innerW)
	blank := strings.Repeat(" ", innerW)
	leftBorder := Muted("│")
	rightBorder := Muted("│")

	var b strings.Builder
	b.WriteString(Muted("╭" + horiz + "╮"))
	b.WriteByte('\n')

	writePadRow := func() {
		b.WriteString(leftBorder)
		b.WriteString(blank)
		b.WriteString(rightBorder)
		b.WriteByte('\n')
	}

	writePadRow()
	for _, line := range lines {
		gap := max(0, innerW-leftPad-PrintableWidth(line))
		b.WriteString(leftBorder)
		b.WriteString(strings.Repeat(" ", leftPad))
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(rightBorder)
		b.WriteByte('\n')
	}
	writePadRow()

	b.WriteString(Muted("╰" + horiz + "╯"))
	b.WriteByte('\n')

	return b.String()
}
