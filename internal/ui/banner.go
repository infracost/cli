package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Gradient stops: brand (#6C70F2) → magenta (#F72585). Kept private so
// the gradient is consistent everywhere it's used.
var (
	gradientStart = [3]int{108, 112, 242}
	gradientEnd   = [3]int{247, 37, 133}
)

// shadowCode is the ANSI truecolor escape for the iconmark shadow color
// (#393D64). Used in place of the gradient on the masked positions so
// the shadow stays a single solid hue across terminals.
const shadowCode = "\x1b[38;2;57;61;100m"

// iconmark is the Infracost mark rendered in full-block glyphs
// (U+2584/U+2588/U+2599/U+259F). Solid blocks tile edge-to-edge so the
// gradient reads as one continuous shape; previously the shadow zones
// used U+2591 LIGHT SHADE, which rendered with visibly different dot
// density across fonts/terminals.
var iconmark = []string{
	"██▄▟█▙",
	"▄▟  ██",
	"██  ██",
}

// iconmarkShadowMask flags the iconmark positions that render in the
// fixed shadow color instead of the brand→magenta gradient. '#' marks
// a shadow cell; any other char (including space) means "use the
// gradient" (or skip, if the iconmark has a space at that position).
// Same dimensions as iconmark.
var iconmarkShadowMask = []string{
	"##    ",
	"      ",
	"##    ",
}

func lerpChannel(a, b int, t float64) int {
	return a + int(float64(b-a)*t+0.5)
}

func gradientCode(t float64) string {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	r := lerpChannel(gradientStart[0], gradientEnd[0], t)
	g := lerpChannel(gradientStart[1], gradientEnd[1], t)
	b := lerpChannel(gradientStart[2], gradientEnd[2], t)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

// Banner renders the Infracost iconmark with a diagonal brand→info
// gradient, alongside a left-to-right gradient wordmark and the version.
// Falls back to plain text when color is disabled.
func Banner(version string) string {
	wordmark := "Infracost CLI"
	versionLine := "v" + strings.TrimPrefix(version, "v")

	if !ColorEnabled() {
		var b strings.Builder
		for i, row := range iconmark {
			b.WriteString("  ")
			b.WriteString(row)
			if i == 0 {
				b.WriteString("   ")
				b.WriteString(wordmark)
				b.WriteString(" ")
				b.WriteString(versionLine)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}

	rows := len(iconmark)
	maxCols := 0
	for _, r := range iconmark {
		if c := utf8.RuneCountInString(r); c > maxCols {
			maxCols = c
		}
	}
	denom := float64((rows - 1) + (maxCols - 1))
	if denom == 0 {
		denom = 1
	}

	var b strings.Builder
	for row, line := range iconmark {
		b.WriteString("  ")
		col := 0
		mask := []rune(iconmarkShadowMask[row])
		for i, r := range []rune(line) {
			if r == ' ' {
				b.WriteRune(' ')
				col++
				continue
			}
			if i < len(mask) && mask[i] == '#' {
				b.WriteString(shadowCode)
			} else {
				t := float64(row+col) / denom
				b.WriteString(gradientCode(t))
			}
			b.WriteRune(r)
			col++
		}
		b.WriteString(reset)
		if row == 0 {
			b.WriteString("   ")
			b.WriteString(Bold(Gradient(wordmark)))
			b.WriteString(" ")
			b.WriteString(Muted(versionLine))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Gradient applies a left-to-right brand→info gradient across the visible
// runes of s. ANSI escape sequences in the input are passed through but
// don't advance the gradient. Falls back to s when color is disabled.
func Gradient(s string) string {
	if !ColorEnabled() || s == "" {
		return s
	}

	runes := []rune(s)
	visible := 0
	for _, r := range runes {
		if r >= 0x20 {
			visible++
		}
	}
	if visible <= 1 {
		return gradientCode(0) + s + reset
	}

	denom := float64(visible - 1)
	var b strings.Builder
	idx := 0
	for _, r := range runes {
		if r >= 0x20 {
			t := float64(idx) / denom
			b.WriteString(gradientCode(t))
			idx++
		}
		b.WriteRune(r)
	}
	b.WriteString(reset)
	return b.String()
}

// Gradientf is a Sprintf wrapper for Gradient.
func Gradientf(format string, args ...any) string {
	return Gradient(fmt.Sprintf(format, args...))
}
