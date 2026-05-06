package ui

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	_ "image/png" // register PNG decoder for image.Decode
	"io"
	"os"
	"strings"
	"sync"

	"github.com/BourgeoisBear/rasterm"
)

//go:embed icons/*.png
var iconsFS embed.FS

type iconProtocol int

const (
	iconNone iconProtocol = iota
	iconKitty
	iconITerm
)

// Inline icon dimensions in terminal cells. One row keeps the icon on
// the same line as the option label so huh's selection list stays
// single-line per option. Two columns is wide enough for brand marks
// to remain recognisable next to text.
const (
	iconCols = 2
	iconRows = 1
)

var (
	iconOnce sync.Once
	iconCap  iconProtocol

	// iconCache holds the pre-rendered escape string per slug so we don't
	// re-encode the PNG on every huh redraw. Bubbletea repaints on every
	// keystroke, and image escapes carry the full base64 payload — caching
	// matters for both speed and bytes-on-the-wire (especially over SSH).
	iconCache   = make(map[string]string)
	iconCacheMu sync.Mutex
)

// detectIconProtocol picks the active image protocol once based on env
// vars only. Sixel is intentionally skipped: capability detection
// requires a synchronous Device Attributes probe that can hang or
// race with stdin in non-tty-like environments. The piped-output gate
// lives at the call site (HasIcons) so this function stays pure and
// testable.
func detectIconProtocol() iconProtocol {
	iconOnce.Do(func() {
		override := strings.ToLower(strings.TrimSpace(os.Getenv("INFRACOST_ICONS")))
		switch override {
		case "off", "0", "false", "no":
			iconCap = iconNone
			return
		}

		// Multiplexers swallow or mangle image protocols and even when
		// they pass through the image survives only until the next
		// redraw. Default to off; users can opt back in once they've
		// configured passthrough.
		insideMux := rasterm.IsTmuxScreen() ||
			os.Getenv("TMUX") != "" ||
			os.Getenv("STY") != ""
		if insideMux && override != "on" {
			iconCap = iconNone
			return
		}

		switch {
		case rasterm.IsKittyCapable():
			iconCap = iconKitty
		case rasterm.IsItermCapable():
			iconCap = iconITerm
		default:
			iconCap = iconNone
		}
	})
	return iconCap
}

// HasIcons reports whether the active terminal can render embedded icons
// AND whether the current output destination is reasonable to write
// image escapes to (colour disabled / stdout piped both rule it out).
func HasIcons() bool {
	if !ColorEnabled() {
		return false
	}
	return detectIconProtocol() != iconNone
}

// renderIcon writes the named PNG to w using the active image protocol.
// Returns silently (nil, no bytes) if icons are unsupported or the slug
// is unknown — callers should treat the absence of an icon as the
// expected fallback, not an error.
func renderIcon(w io.Writer, slug string) error {
	proto := detectIconProtocol()
	if proto == iconNone {
		return nil
	}
	data, err := iconsFS.ReadFile("icons/" + slug + ".png")
	if err != nil {
		return nil
	}
	switch proto {
	case iconITerm:
		return rasterm.ItermCopyFileInlineWithOptions(w, bytes.NewReader(data), rasterm.ItermImgOpts{
			DisplayInline:     true,
			Width:             fmt.Sprintf("%d", iconCols),
			Height:            fmt.Sprintf("%d", iconRows),
			IgnoreAspectRatio: true,
			Size:              int64(len(data)),
		})
	case iconKitty:
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		return rasterm.KittyWriteImage(w, img, rasterm.KittyImgOpts{
			DstCols: iconCols,
			DstRows: iconRows,
		})
	}
	return nil
}

// brandStyle records how a service's name should be coloured. Either
// rgb (a single solid colour as the "R;G;B" payload for a 24-bit ANSI
// escape) or gradient (a sequence of ≥2 RGB stops interpolated per
// rune) drives the colouring; gradient takes precedence when set.
// highlight optionally restricts the colouring to a substring of the
// display name (e.g. for "OpenAI Codex" only "Codex" gets the brand
// tint; "OpenAI" stays the default foreground). Empty highlight means
// "colour the whole name".
type brandStyle struct {
	rgb       string
	gradient  []rgbStop
	highlight string
}

// rgbStop is a single colour stop in a multi-stop gradient.
type rgbStop [3]int

// brandColors maps an icon slug to its service's primary brand colour,
// hand-picked from each vendor's marketing material and tuned for
// legibility on a dark terminal.
var brandColors = map[string]brandStyle{
	"claude":    {rgb: "201;100;66"},                       // #C96442 Anthropic coral
	"copilot":   {rgb: "137;87;229"},                       // #8957E5 GitHub purple
	"codex":     {rgb: "16;163;127", highlight: "Codex"},   // #10A37F applied only to "Codex"
	// Cursor's identity is monochrome (black/white); intentionally omitted
	// so Service() falls through to the default foreground. The icon next
	// to the name carries the brand cue.
	"vscode":    {rgb: "0;122;204"},                        // #007ACC VS Code blue
	"jetbrains": {gradient: []rgbStop{ // pink → purple → orange, the JetBrains umbrella mark
		{254, 49, 93},   // #FE315D pink
		{160, 57, 163},  // #A039A3 purple
		{255, 144, 0},   // #FF9000 orange
	}},
	"zed":       {rgb: "59;130;246"},                       // #3B82F6 Zed accent blue (logo + buttons on zed.dev)
	"neovim":    {rgb: "122;166;79"},                       // #7AA64F Neovim green
	"gemini":    {rgb: "66;133;244"},                       // #4285F4 Google blue
}

// Service wraps text in the brand colour registered for slug. When the
// brand has a highlight substring, only that substring is coloured; the
// rest of text passes through unchanged. Multi-stop gradients are
// interpolated per rune across the coloured span. Returns text unchanged
// when colour is disabled or the slug is unknown so callers can use
// Service unconditionally.
func Service(slug, text string) string {
	if !ColorEnabled() {
		return text
	}
	style, ok := brandColors[slug]
	if !ok {
		return text
	}
	target := text
	prefix, suffix := "", ""
	if style.highlight != "" {
		idx := strings.Index(text, style.highlight)
		if idx >= 0 {
			prefix = text[:idx]
			target = style.highlight
			suffix = text[idx+len(style.highlight):]
		}
	}
	var coloured string
	if len(style.gradient) >= 2 {
		coloured = applyGradient(target, style.gradient)
	} else {
		coloured = fg(style.rgb) + target + reset
	}
	return prefix + coloured + suffix
}

// applyGradient colours each visible rune of text by interpolating
// linearly across the supplied stops. Control characters (< 0x20) and
// existing ANSI escapes pass through unchanged and don't advance the
// gradient position.
func applyGradient(text string, stops []rgbStop) string {
	if len(stops) == 0 {
		return text
	}
	if len(stops) == 1 {
		s := stops[0]
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", s[0], s[1], s[2], text)
	}
	runes := []rune(text)
	visible := 0
	for _, r := range runes {
		if r >= 0x20 {
			visible++
		}
	}
	if visible <= 1 {
		s := stops[0]
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", s[0], s[1], s[2], text)
	}
	denom := float64(visible - 1)
	var b strings.Builder
	idx := 0
	for _, r := range runes {
		if r >= 0x20 {
			t := float64(idx) / denom
			rgb := interpolateStops(stops, t)
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
			idx++
		}
		b.WriteRune(r)
	}
	b.WriteString(reset)
	return b.String()
}

// interpolateStops returns the RGB triple at position t∈[0,1] across the
// supplied stops, treating them as evenly-spaced segments.
func interpolateStops(stops []rgbStop, t float64) rgbStop {
	if t <= 0 {
		return stops[0]
	}
	if t >= 1 {
		return stops[len(stops)-1]
	}
	segs := len(stops) - 1
	pos := t * float64(segs)
	seg := int(pos)
	if seg >= segs {
		seg = segs - 1
	}
	local := pos - float64(seg)
	a, b := stops[seg], stops[seg+1]
	return rgbStop{
		lerpChannel(a[0], b[0], local),
		lerpChannel(a[1], b[1], local),
		lerpChannel(a[2], b[2], local),
	}
}

// Icon returns an inline image escape sized for use next to a single
// line of text (one row, two columns wide). When the active terminal
// can't render images or the slug is unknown, returns "" so callers can
// concatenate unconditionally:
//
//	label := ui.Icon("claude") + " Claude Code"
//
// Cached per slug — repeat calls (e.g. once per Bubbletea redraw) reuse
// the encoded payload.
func Icon(slug string) string {
	if !HasIcons() || slug == "" {
		return ""
	}
	iconCacheMu.Lock()
	defer iconCacheMu.Unlock()
	if s, ok := iconCache[slug]; ok {
		return s
	}
	var buf bytes.Buffer
	if err := renderIcon(&buf, slug); err != nil || buf.Len() == 0 {
		iconCache[slug] = ""
		return ""
	}
	s := buf.String()
	iconCache[slug] = s
	return s
}
