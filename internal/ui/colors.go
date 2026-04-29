package ui

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/liamg/tml"
)

// Brand palette. Optimized for dark terminals; truecolor (24-bit) is
// emitted directly. Any callsite that wants color goes through the
// helpers in this file so `--no-color` / NO_COLOR can short-circuit
// uniformly.
const (
	hexText    = "234;233;242"  // #EAE9F2
	hexFail    = "239;68;68"    // #EF4444
	hexWarn    = "255;186;8"    // #FFBA08
	hexSuccess = "16;217;160"   // #10D9A0
	hexBrand   = "108;112;242"  // #6C70F2
	hexAccent  = "177;184;248"  // #B1B8F8 — command names, identifiers
	hexInfo    = "34;211;238"   // #22D3EE — also used for inline code
	hexMuted   = "116;124;162"  // #747CA2
)

const (
	reset     = "\x1b[0m"
	boldOn    = "\x1b[1m"
	dimOn     = "\x1b[2m"
	italicOn = "\x1b[3m"
	underlineOn = "\x1b[4m"
)

func fg(triplet string) string { return "\x1b[38;2;" + triplet + "m" }

var (
	textCode    = fg(hexText)
	failCode    = fg(hexFail)
	warnCode    = fg(hexWarn)
	successCode = fg(hexSuccess)
	brandCode   = fg(hexBrand)
	accentCode  = fg(hexAccent)
	infoCode    = fg(hexInfo)
	mutedCode   = fg(hexMuted)
)

var colorDisabled atomic.Bool

func init() {
	// NO_COLOR (https://no-color.org): any non-empty value disables.
	if os.Getenv("NO_COLOR") != "" {
		DisableColor()
		return
	}
	// Pre-scan --no-color so the toggle is set before any callsite (including
	// the help template) renders. Cobra's flag parsing happens too late.
	for _, arg := range os.Args[1:] {
		if arg == "--no-color" {
			DisableColor()
			return
		}
		if v, ok := strings.CutPrefix(arg, "--no-color="); ok && v != "false" && v != "0" {
			DisableColor()
			return
		}
	}
	// Plain stdout (pipe/file) gets no color either.
	if info, err := os.Stdout.Stat(); err != nil || (info.Mode()&os.ModeCharDevice) == 0 {
		DisableColor()
	}
}

// DisableColor turns off ALL color from this CLI — the truecolor
// helpers below, direct `tml.*` usage, and any lipgloss-based rendering
// (huh prompts, spinners) via the NO_COLOR convention. Safe to call
// multiple times.
func DisableColor() {
	colorDisabled.Store(true)
	tml.DisableFormatting()
	_ = os.Setenv("NO_COLOR", "1")
}

// ColorEnabled reports whether color helpers will emit escape codes.
func ColorEnabled() bool { return !colorDisabled.Load() }

func wrap(code, s string) string {
	if colorDisabled.Load() {
		return s
	}
	return code + s + reset
}

// Text returns the input wrapped in the primary text color. Most output
// can rely on the terminal's default foreground; use this only when you
// need to "lift" text that follows colored content.
func Text(s string) string { return wrap(textCode, s) }

// Brand returns text in the Infracost brand-primary color (used for
// headings and titles).
func Brand(s string) string { return wrap(brandCode, s) }

// Accent returns text in the accent color (used to highlight command
// names, identifiers, and similar named items in lists).
func Accent(s string) string { return wrap(accentCode, s) }

// Info returns text in the info color (used for hint arrows, links,
// and "→" action markers).
func Info(s string) string { return wrap(infoCode, s) }

// Code returns text in the code/literal color (regexes, inline code
// references). Visually identical to Info; named separately for intent.
func Code(s string) string { return wrap(infoCode, s) }

// Muted returns text in the muted color (IDs, secondary info, brackets).
func Muted(s string) string { return wrap(mutedCode, s) }

// Danger returns text in the failure/error color.
func Danger(s string) string { return wrap(failCode, s) }

// Caution returns text in the warning color (also used for money).
func Caution(s string) string { return wrap(warnCode, s) }

// Positive returns text in the success color (passing checks, savings).
func Positive(s string) string { return wrap(successCode, s) }

// Bold returns text in bold.
func Bold(s string) string { return wrap(boldOn, s) }

// Dim returns text dimmed.
func Dim(s string) string { return wrap(dimOn, s) }

// Italic returns text in italics.
func Italic(s string) string { return wrap(italicOn, s) }

// Underline returns text underlined.
func Underline(s string) string { return wrap(underlineOn, s) }

// Brandf is a Sprintf wrapper that applies the brand color to the
// formatted string.
func Brandf(format string, args ...any) string { return Brand(fmt.Sprintf(format, args...)) }

// Accentf, Infof, Codef, Mutedf, Dangerf, Cautionf, Positivef are Sprintf
// wrappers for their corresponding colors.
func Accentf(format string, args ...any) string   { return Accent(fmt.Sprintf(format, args...)) }
func Infof(format string, args ...any) string     { return Info(fmt.Sprintf(format, args...)) }
func Codef(format string, args ...any) string     { return Code(fmt.Sprintf(format, args...)) }
func Mutedf(format string, args ...any) string    { return Muted(fmt.Sprintf(format, args...)) }
func Dangerf(format string, args ...any) string   { return Danger(fmt.Sprintf(format, args...)) }
func Cautionf(format string, args ...any) string  { return Caution(fmt.Sprintf(format, args...)) }
func Positivef(format string, args ...any) string { return Positive(fmt.Sprintf(format, args...)) }
