package ui

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// MaxBoxWidth caps how wide Box will draw on ultrawide terminals — beyond
// this it's mostly empty space to the right of the content.
const MaxBoxWidth = 120

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// apcRE matches a Kitty graphics APC ("\x1b_G…\x1b\\"). Each one carries
// a `c=N` parameter that declares how many terminal cells the rendered
// image will occupy horizontally; PrintableWidth credits that count so
// layouts containing inline icons (e.g. instruction cards in setup)
// measure correctly. Base64 image data inside the body never contains
// \x1b, so the "anything-not-ESC" inner class terminates cleanly at
// the closing ST. Matching only the `G` command keeps us narrow — other
// APC namespaces aren't used in this codebase.
var apcRE = regexp.MustCompile(`\x1b_G[^\x1b]*\x1b\\`)

// oscRE matches an OSC ("\x1b]…<BEL or ST>") — used for hyperlinks and
// the iTerm2 image protocol. Treated as zero-width: any visible text
// between two OSC sequences (e.g. the label of a hyperlink) survives
// the strip pass and gets counted normally.
var oscRE = regexp.MustCompile(`\x1b\][^\x1b\x07]*(\x07|\x1b\\)`)

// variationSelector16 (U+FE0F) forces emoji-presentation of the preceding
// base codepoint. It's used in PrintableWidth to bump the cell count, since
// runewidth doesn't credit it but terminals widen the rendered glyph to 2.
const variationSelector16 = "️"

// ContentWidth returns the visible width available for content inside a Box
// on the current terminal — i.e. terminal width minus the borders and inner
// padding that Box adds. Use this as the target width when wrapping text or
// constraining tables before passing them to Box. Returns 0 when stdout
// isn't a TTY, in which case content is unconstrained (auto-sized).
func ContentWidth() int {
	tw := terminalWidth()
	if tw == 0 {
		return 0
	}
	// Box uses (border, leftPad, ..., rightPad, border) = 8 cells overhead.
	const boxOverhead = 2 + 3 + 3
	w := min(tw, MaxBoxWidth) - boxOverhead
	if w < 0 {
		return 0
	}
	return w
}

// TerminalContentWidth returns the visible width for content rendered
// outside a Box (e.g. group-by tables). Capped at MaxBoxWidth so a 200-col
// terminal doesn't produce a 200-col table; returns 0 (unconstrained) when
// stdout isn't a TTY.
func TerminalContentWidth() int {
	tw := terminalWidth()
	if tw == 0 {
		return 0
	}
	return min(tw, MaxBoxWidth)
}

// WrapText soft-wraps s to maxWidth visible columns. Splits on whitespace;
// tokens longer than maxWidth get hard-split (so e.g. a long URL doesn't
// overrun a narrow terminal). maxWidth <= 0 returns s unchanged. Existing
// newlines in s are preserved — each pre-existing line is wrapped
// independently.
func WrapText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	var out strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		wrapLine(&out, line, maxWidth)
	}
	return out.String()
}

func wrapLine(out *strings.Builder, line string, maxWidth int) {
	if PrintableWidth(line) <= maxWidth {
		out.WriteString(line)
		return
	}

	// Capture the original line's leading whitespace so wrapped
	// continuation lines preserve the same indent. Without this, an
	// indented bullet like "  →  Open …" loses its 2-space prefix once
	// strings.FieldsSeq drops whitespace during the wrap, and the
	// bullet visually breaks out of the surrounding list.
	indent := ""
	rest := line
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c != ' ' && c != '\t' {
			indent = line[:i]
			rest = line[i:]
			break
		}
	}
	indentW := PrintableWidth(indent)

	out.WriteString(indent)
	cur := indentW
	first := true
	for word := range strings.FieldsSeq(rest) {
		ww := PrintableWidth(word)
		switch {
		case ww > maxWidth:
			// Hard-split tokens that won't fit on any line (long URLs).
			if !first {
				if cur+1 > maxWidth {
					out.WriteByte('\n')
					out.WriteString(indent)
					cur = indentW
				} else {
					out.WriteByte(' ')
					cur++
				}
			}
			runes := []rune(word)
			for len(runes) > 0 {
				take := maxWidth - cur
				if take <= 0 {
					out.WriteByte('\n')
					out.WriteString(indent)
					cur = indentW
					take = maxWidth - indentW
				}
				if take > len(runes) {
					take = len(runes)
				}
				out.WriteString(string(runes[:take]))
				cur += take
				runes = runes[take:]
				if len(runes) > 0 {
					out.WriteByte('\n')
					out.WriteString(indent)
					cur = indentW
				}
			}
			first = false
		case first:
			out.WriteString(word)
			cur = indentW + ww
			first = false
		case cur+1+ww <= maxWidth:
			out.WriteByte(' ')
			out.WriteString(word)
			cur += 1 + ww
		default:
			out.WriteByte('\n')
			out.WriteString(indent)
			out.WriteString(word)
			cur = indentW + ww
		}
	}
}

// PrintableWidth returns the visible column count of s, ignoring ANSI escape
// codes. Three classes of escapes are handled:
//
//   - CSI ("\x1b[…<final>"): styling sequences (colour, bold, etc).
//     Stripped, count as zero width.
//   - OSC ("\x1b]…<BEL/ST>"): hyperlinks and iTerm2 image data.
//     Stripped, count as zero width. Any visible text between two OSC
//     sequences (a hyperlink label) is preserved and counted normally.
//   - APC ("\x1b_…\x1b\\"): Kitty graphics protocol. Each APC declares
//     its display width in cells via a c= parameter; that count is
//     credited so inline icon escapes measure as their reserved cell
//     width rather than the length of their base64 payload.
//
// Uses runewidth for base width then bumps by 1 per VS-16 selector:
// runewidth (v0.0.16) doesn't credit U+FE0F for promoting an ambiguous-width
// base (e.g. ⚠ U+26A0) to emoji presentation, but every modern terminal
// renders the resulting glyph at 2 cells. Without this bump, ⚠️ measures as
// 1 and box/table borders drift right by one cell on each emoji-bearing line.
func PrintableWidth(s string) int {
	width := 0
	// Each APC contributes its declared display width (c= keyword).
	// Body layout after "\x1b_G": "<kv-pairs>;<base64>\x1b\\". Pairs are
	// comma-separated k=v; the base64 (and the leading ";") is optional
	// for delete/query commands that send keys only.
	for _, apc := range apcRE.FindAllString(s, -1) {
		body := apc[3 : len(apc)-2] // trim leading "\x1b_G" and trailing "\x1b\\"
		keys := body
		if i := strings.Index(body, ";"); i >= 0 {
			keys = body[:i]
		}
		for _, kv := range strings.Split(keys, ",") {
			if v, ok := strings.CutPrefix(kv, "c="); ok {
				if n, err := strconv.Atoi(v); err == nil {
					width += n
				}
				break
			}
		}
	}

	stripped := apcRE.ReplaceAllString(s, "")
	stripped = oscRE.ReplaceAllString(stripped, "")
	stripped = ansiRE.ReplaceAllString(stripped, "")
	width += runewidth.StringWidth(stripped) + strings.Count(stripped, variationSelector16)
	return width
}

// terminalWidth returns the column width of stdout, or 0 if stdout is not a
// terminal or the size can't be read. Callers fall back to content width when
// this is 0 so non-TTY output (tests, pipes) stays deterministic.
func terminalWidth() int {
	rawFd := os.Stdout.Fd()
	if rawFd > math.MaxInt {
		return 0
	}
	fd := int(rawFd)
	if !term.IsTerminal(fd) {
		return 0
	}
	w, _, err := term.GetSize(fd)
	if err != nil {
		return 0
	}
	return w
}

// InstructionsCard renders content inside a bordered card titled with
// "title". The card is left-indented to align with the "  ✔  " /
// "  →  " checklist text and right-padded a few cells from the terminal
// edge. The title sits in the top border, e.g.:
//
//	     ╭─ Setup instructions for VS Code ─────╮
//	     │                                       │
//	     │   To install ...                      │
//	     │                                       │
//	     ╰───────────────────────────────────────╯
//
// Designed for the manual-instruction setup flows where the user takes
// action outside the CLI (e.g. clicking through a marketplace page).
func InstructionsCard(title, content string) string {
	const (
		// Aligns the left border with the column where checklist text
		// starts after "  ✔  " / "  →  " (2 spaces + glyph + 2 spaces).
		instructionLeftIndent = 5
		// Inset from the terminal's right edge so the box doesn't kiss it.
		instructionRightInset = 3
		// Horizontal padding between the borders and the wrapped content.
		instructionContentPad = 2
		// Floor on the inner width so very narrow terminals still produce
		// a usable card rather than a vertical sliver.
		instructionMinInner = 30
	)

	tw := terminalWidth()
	if tw <= 0 {
		tw = 80
	}

	outerW := min(tw-instructionLeftIndent-instructionRightInset, MaxBoxWidth)
	innerW := max(outerW-2, instructionMinInner)

	contentW := innerW - 2*instructionContentPad
	wrapped := WrapText(content, contentW)
	lines := strings.Split(strings.TrimRight(wrapped, "\n"), "\n")

	indent := strings.Repeat(" ", instructionLeftIndent)
	pad := strings.Repeat(" ", instructionContentPad)
	leftBorder := Muted("│")
	rightBorder := Muted("│")
	blank := strings.Repeat(" ", innerW)

	// Top border: "╭─ <title> ──…──╮". Title in bold/brand for emphasis,
	// border chars stay muted so the styles read as one piece.
	titleW := PrintableWidth(title)
	fillW := max(innerW-3-titleW, 0) // 3 = "─ " before + " " after the title

	var b strings.Builder
	b.WriteString(indent)
	b.WriteString(Muted("╭─ "))
	b.WriteString(Bold(Brand(title)))
	b.WriteString(Muted(" " + strings.Repeat("─", fillW) + "╮"))
	b.WriteByte('\n')

	writeBlank := func() {
		b.WriteString(indent)
		b.WriteString(leftBorder)
		b.WriteString(blank)
		b.WriteString(rightBorder)
		b.WriteByte('\n')
	}

	writeBlank()
	for _, line := range lines {
		gap := max(0, innerW-2*instructionContentPad-PrintableWidth(line))
		b.WriteString(indent)
		b.WriteString(leftBorder)
		b.WriteString(pad)
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(pad)
		b.WriteString(rightBorder)
		b.WriteByte('\n')
	}
	writeBlank()

	b.WriteString(indent)
	b.WriteString(Muted("╰" + strings.Repeat("─", innerW) + "╯"))
	b.WriteByte('\n')

	return b.String()
}

// GradientCard renders content in a left-indented bordered card with
// the brand→pink gradient applied to the border. Top and bottom edges
// sweep horizontally across the gradient; side borders sit at the
// gradient's start (left) and end (right) stops so the colour reads as
// one continuous shape around the card. Used for celebratory or summary
// moments — e.g. the "setup complete" card at the end of `infracost
// setup`.
func GradientCard(content string) string {
	const (
		gradientLeftIndent = 5
		gradientRightInset = 3
		gradientContentPad = 2
		gradientMinInner   = 30
	)

	tw := terminalWidth()
	if tw <= 0 {
		tw = 80
	}
	outerW := min(tw-gradientLeftIndent-gradientRightInset, MaxBoxWidth)
	innerW := max(outerW-2, gradientMinInner)

	contentW := innerW - 2*gradientContentPad
	wrapped := WrapText(content, contentW)
	lines := strings.Split(strings.TrimRight(wrapped, "\n"), "\n")

	indent := strings.Repeat(" ", gradientLeftIndent)
	pad := strings.Repeat(" ", gradientContentPad)
	blank := strings.Repeat(" ", innerW)

	// Side borders pin to the gradient's start (left) and end (right)
	// stops so each row of the card looks visually consistent with the
	// horizontal sweep on the top/bottom edges.
	leftBorder := "│"
	rightBorder := "│"
	if ColorEnabled() {
		leftBorder = gradientCode(0) + "│" + reset
		rightBorder = gradientCode(1) + "│" + reset
	}

	// Gradient takes a string and interpolates a colour per visible rune
	// across its length, which is exactly what we want for the
	// horizontal border edges.
	top := Gradient("╭" + strings.Repeat("─", innerW) + "╮")
	bottom := Gradient("╰" + strings.Repeat("─", innerW) + "╯")

	var b strings.Builder
	b.WriteString(indent)
	b.WriteString(top)
	b.WriteByte('\n')

	writeBlank := func() {
		b.WriteString(indent)
		b.WriteString(leftBorder)
		b.WriteString(blank)
		b.WriteString(rightBorder)
		b.WriteByte('\n')
	}

	writeBlank()
	for _, line := range lines {
		gap := max(0, innerW-2*gradientContentPad-PrintableWidth(line))
		b.WriteString(indent)
		b.WriteString(leftBorder)
		b.WriteString(pad)
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", gap))
		b.WriteString(pad)
		b.WriteString(rightBorder)
		b.WriteByte('\n')
	}
	writeBlank()

	b.WriteString(indent)
	b.WriteString(bottom)
	b.WriteByte('\n')

	return b.String()
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
