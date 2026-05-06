package ui

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
	"github.com/liamg/tml"
)

// Brand palette. Two variants are kept — one tuned for dark terminals
// (where text needs to be light/saturated to pop against a near-black
// background) and one for light terminals (where text needs to be
// darker/desaturated to retain contrast against a near-white background).
// The active palette is picked at init time based on terminal background
// detection. Truecolor (24-bit) is emitted directly. Any callsite that
// wants color goes through the helpers in this file so `--no-color` /
// NO_COLOR can short-circuit uniformly.
const (
	// Dark-terminal palette (default).
	darkText    = "234;233;242" // #EAE9F2
	darkFail    = "239;68;68"   // #EF4444
	darkWarn    = "255;186;8"   // #FFBA08
	darkSuccess = "16;217;160"  // #10D9A0
	darkBrand   = "108;112;242" // #6C70F2
	darkAccent  = "177;184;248" // #B1B8F8 — command names, identifiers
	darkInfo    = "34;211;238"  // #22D3EE — also used for inline code
	darkMuted   = "116;124;162" // #747CA2

	// Light-terminal palette: darker, less saturated variants picked for
	// AA contrast on white. Mirrors Tailwind's 600–700 range.
	lightText    = "31;41;55"    // #1F2937 — gray-800
	lightFail    = "185;28;28"   // #B91C1C — red-700
	lightWarn    = "180;83;9"    // #B45309 — amber-700
	lightSuccess = "4;120;87"    // #047857 — emerald-700
	lightBrand   = "79;70;229"   // #4F46E5 — indigo-600
	lightAccent  = "67;56;202"   // #4338CA — indigo-700
	lightInfo    = "14;116;144"  // #0E7490 — cyan-700
	lightMuted   = "75;85;99"    // #4B5563 — gray-600
)

// Hex variants of the same palette, surfaced so adaptive lipgloss colors
// (used by huh themes, the spinner, etc.) stay in sync with the truecolor
// codes above.
const (
	hexBrandDark    = "#6C70F2"
	hexAccentDark   = "#B1B8F8"
	hexInfoDark     = "#22D3EE"
	hexSuccessDark  = "#10D9A0"
	hexWarnDark     = "#FFBA08"
	hexFailDark     = "#EF4444"
	hexMutedDark    = "#747CA2"
	hexTextDark     = "#EAE9F2"

	hexBrandLight   = "#4F46E5"
	hexAccentLight  = "#4338CA"
	hexInfoLight    = "#0E7490"
	hexSuccessLight = "#047857"
	hexWarnLight    = "#B45309"
	hexFailLight    = "#B91C1C"
	hexMutedLight   = "#4B5563"
	hexTextLight    = "#1F2937"
)

const (
	reset     = "\x1b[0m"
	boldOn    = "\x1b[1m"
	dimOn     = "\x1b[2m"
	italicOn = "\x1b[3m"
	underlineOn = "\x1b[4m"
)

func fg(triplet string) string { return "\x1b[38;2;" + triplet + "m" }

// Active escape codes. Populated in init() once the background is known.
var (
	textCode    string
	failCode    string
	warnCode    string
	successCode string
	brandCode   string
	accentCode  string
	infoCode    string
	mutedCode   string
)

var (
	colorDisabled  atomic.Bool
	darkBackground = true // assume dark unless detection says otherwise
)

// HasDarkBackground reports whether the active palette is the dark-terminal
// variant. Useful for callers that emit truecolor escapes directly (banner,
// gradient) and need to pick matching shades.
func HasDarkBackground() bool { return darkBackground }

func init() {
	// NO_COLOR (https://no-color.org): any non-empty value disables.
	if os.Getenv("NO_COLOR") != "" {
		DisableColor()
		applyPalette()
		return
	}
	// Pre-scan --no-color so the toggle is set before any callsite (including
	// the help template) renders. Cobra's flag parsing happens too late.
	for _, arg := range os.Args[1:] {
		if arg == "--no-color" {
			DisableColor()
			applyPalette()
			return
		}
		if v, ok := strings.CutPrefix(arg, "--no-color="); ok && v != "false" && v != "0" {
			DisableColor()
			applyPalette()
			return
		}
	}
	// Plain stdout (pipe/file) gets no color either.
	if info, err := os.Stdout.Stat(); err != nil || (info.Mode()&os.ModeCharDevice) == 0 {
		DisableColor()
		applyPalette()
		return
	}
	// Honor an explicit override (useful when terminal detection is flaky,
	// e.g. inside tmux/screen, or over SSH). Accepts light/dark only.
	switch strings.ToLower(strings.TrimSpace(os.Getenv("INFRACOST_TERMINAL_BACKGROUND"))) {
	case "light":
		darkBackground = false
	case "dark":
		darkBackground = true
	default:
		// Ask the terminal. Defaults to dark on detection failure.
		darkBackground = lipgloss.HasDarkBackground()
	}
	applyPalette()
}

func applyPalette() {
	if darkBackground {
		textCode = fg(darkText)
		failCode = fg(darkFail)
		warnCode = fg(darkWarn)
		successCode = fg(darkSuccess)
		brandCode = fg(darkBrand)
		accentCode = fg(darkAccent)
		infoCode = fg(darkInfo)
		mutedCode = fg(darkMuted)
		return
	}
	textCode = fg(lightText)
	failCode = fg(lightFail)
	warnCode = fg(lightWarn)
	successCode = fg(lightSuccess)
	brandCode = fg(lightBrand)
	accentCode = fg(lightAccent)
	infoCode = fg(lightInfo)
	mutedCode = fg(lightMuted)
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
