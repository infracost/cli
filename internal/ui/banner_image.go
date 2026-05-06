package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"

	"github.com/BourgeoisBear/rasterm"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Image-banner layout:
//
//	┌─────────────── full terminal width ───────────────┐
//	│  gradient ░░░░░░░░  [INFRACOST LOGO]  ░░░░░░░░ ▓▓ │ ← bannerRows tall
//	└────────────────────────────────────────────────────┘
//	v0.0.x                                                ← version line
//
// Composed at runtime: a horizontal brand→pink gradient fills the
// banner; the embedded logo PNG is scaled to fit a comfortable height
// and composited centered. The whole thing is encoded as a single PNG
// and emitted via the active image protocol with `c=W r=bannerRows`
// sizing so the terminal renders it edge-to-edge.
const (
	// bannerRows is the banner's display height in terminal cells.
	bannerRows = 4
	// bannerPxHeight is the height of the source PNG before terminal
	// scaling. Sized so the logo (logoPxHeight) sits inside with room
	// to spare above and below, giving the banner some breathing space.
	bannerPxHeight = 64
	// bannerPxPerCol approximates a terminal cell's pixel width. Used to
	// size the source PNG proportionally to the terminal so the gradient
	// and logo render at the right relative scale after the protocol
	// scales us to fit.
	bannerPxPerCol = 8
	// logoPxHeight is the rendered logo height inside the banner.
	// Smaller than bannerPxHeight so there's vertical padding above and
	// below.
	logoPxHeight = 42
	// bannerLeftMarginPx pads the logo away from the banner's left edge
	// so the gradient still reads on the left side. Equivalent to ~3
	// terminal cells.
	bannerLeftMarginPx = 24
	// bannerVersionRightPx / bannerVersionBottomPx give the version
	// label its inset from the banner's bottom-right corner.
	bannerVersionRightPx  = 12
	bannerVersionBottomPx = 8
)

// imageBanner returns a printable banner string that places the embedded
// Infracost wordmark inside a brand→pink horizontal gradient spanning
// the full terminal width. Falls back to "" if anything goes wrong;
// callers should treat that as "no image banner" and fall through to
// the ASCII variant.
func imageBanner(version string) string {
	w := terminalWidth()
	if w <= 0 {
		w = 80
	}
	// Floor at 320px so the gradient stays legible on tiny terminals.
	bannerPxWidth := max(w*bannerPxPerCol, 320)

	// On dark terminals (the only background we currently target) the
	// dark-coloured wordmark sits on top of the saturated brand gradient
	// the way the marketing site uses it — high contrast against the
	// purple/pink without the washed-out look the white wordmark gives.
	logoData, err := iconsFS.ReadFile("icons/banner-dark.png")
	if err != nil {
		return ""
	}
	src, _, err := image.Decode(bytes.NewReader(logoData))
	if err != nil {
		return ""
	}

	// Scale the logo to logoPxHeight while preserving aspect ratio.
	srcB := src.Bounds()
	logoW := logoPxHeight * srcB.Dx() / srcB.Dy()
	logo := image.NewRGBA(image.Rect(0, 0, logoW, logoPxHeight))
	xdraw.CatmullRom.Scale(logo, logo.Bounds(), src, srcB, xdraw.Over, nil)

	// Build the banner canvas, fill with a horizontal gradient.
	canvas := image.NewRGBA(image.Rect(0, 0, bannerPxWidth, bannerPxHeight))
	denom := float64(bannerPxWidth - 1)
	if denom <= 0 {
		denom = 1
	}
	for x := range bannerPxWidth {
		t := float64(x) / denom
		r := uint8(lerpChannel(gradientStart[0], gradientEnd[0], t))
		g := uint8(lerpChannel(gradientStart[1], gradientEnd[1], t))
		b := uint8(lerpChannel(gradientStart[2], gradientEnd[2], t))
		col := color.RGBA{R: r, G: g, B: b, A: 0xff}
		for y := range bannerPxHeight {
			canvas.SetRGBA(x, y, col)
		}
	}

	// Composite the logo left-aligned with a small horizontal margin,
	// vertically centred so the padding reads evenly above and below.
	logoX := bannerLeftMarginPx
	logoY := (bannerPxHeight - logoPxHeight) / 2
	xdraw.Draw(canvas, image.Rect(logoX, logoY, logoX+logoW, logoY+logoPxHeight), logo, image.Point{}, xdraw.Over)

	// Stamp the version label into the bottom-right corner using the
	// same dark colour as the wordmark so the two read as one piece.
	// basicfont.Face7x13 is a no-deps bitmap font shipped with x/image;
	// version strings are ASCII, so the limited glyph set is fine.
	// The font has no bold variant — fake-bolding by drawing twice with
	// a 1px horizontal offset thickens each glyph stroke and gives the
	// label enough weight to stay legible after the protocol scales the
	// whole banner down to terminal-cell dimensions.
	versionLabel := "v" + strings.TrimPrefix(version, "v")
	labelDrawer := &font.Drawer{
		Dst:  canvas,
		Src:  image.NewUniform(color.RGBA{R: 0x0D, G: 0x13, B: 0x2C, A: 0xff}),
		Face: basicfont.Face7x13,
	}
	labelW := labelDrawer.MeasureString(versionLabel).Round()
	baseX := bannerPxWidth - labelW - bannerVersionRightPx
	baseY := bannerPxHeight - bannerVersionBottomPx
	labelDrawer.Dot = fixed.P(baseX, baseY)
	labelDrawer.DrawString(versionLabel)
	labelDrawer.Dot = fixed.P(baseX+1, baseY)
	labelDrawer.DrawString(versionLabel)

	// Encode as PNG and emit through the active image protocol.
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, canvas); err != nil {
		return ""
	}

	var out bytes.Buffer
	switch detectIconProtocol() {
	case iconITerm:
		err = rasterm.ItermCopyFileInlineWithOptions(&out, &pngBuf, rasterm.ItermImgOpts{
			DisplayInline:     true,
			Width:             fmt.Sprintf("%d", w),
			Height:            fmt.Sprintf("%d", bannerRows),
			IgnoreAspectRatio: true,
			Size:              int64(pngBuf.Len()),
		})
	case iconKitty:
		err = rasterm.KittyWriteImage(&out, canvas, rasterm.KittyImgOpts{
			DstCols: uint32(w),
			DstRows: uint32(bannerRows),
		})
	default:
		return ""
	}
	if err != nil {
		return ""
	}

	// Newline after the image so the cursor lands cleanly below the
	// banner. The version label is baked into the banner itself.
	out.WriteString("\n")
	return out.String()
}
