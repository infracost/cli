package ui

import (
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"golang.org/x/term"
)

// Two camps of terminal emulators disagree about the rendered width of
// ambiguous-width emoji (e.g. ⚠ U+26A0) when followed by U+FE0F (the
// variation selector that requests emoji presentation):
//
//   - Modern, Unicode TR51-aware terminals (Ghostty, Kitty, iTerm2,
//     WezTerm, recent Windows Terminal): widen the base codepoint to a
//     2-cell colored emoji when it carries VS-16. Matches what
//     uniseg/ansi.StringWidth measures.
//   - Traditional wcwidth(3) terminals (Apple Terminal, cmd.exe and
//     older Windows Terminal versions, the Linux console, GNU screen,
//     some tmux configurations): ignore VS-16. The base codepoint stays
//     1 cell.
//
// We can't tell from environment variables alone — TERM_PROGRAM /
// runtime.GOOS only catch a subset, and tmux can flip the behavior of
// any wrapper terminal. The reliable signal is to ask the terminal
// itself: write a probe emoji, send a Cursor Position Report request,
// read back the column the cursor advanced to. The two results are
// 2 (modern) or 1 (wcwidth-style).
//
// The probe is run once at startup via DetectEmojiWidth. Callers that
// need the result use EmojiWidth or NarrowEmojis. Both default to the
// modern interpretation (2 cells) so unprobed environments — non-TTY,
// CI, tests — match what the CLI text output already assumes.

const probeEmoji = "⚠️"

var (
	emojiWidthOnce sync.Once
	emojiWidth     = 2 // safe default: assume the terminal honors VS-16
)

// DetectEmojiWidth probes the active terminal once and caches the
// result. Idempotent and safe to call from multiple sites — the probe
// runs at most once per process. Should be called early, before any
// code that depends on emoji width renders output.
//
// Falls back to the default (2 cells) when:
//   - /dev/tty can't be opened (non-TTY, restricted environment),
//   - the terminal can't be put in raw mode,
//   - the cursor-position query times out (terminal doesn't reply
//     within 150ms), or
//   - the response can't be parsed.
//
// On Windows, this returns silently because /dev/tty isn't available;
// the default applies.
func DetectEmojiWidth() {
	emojiWidthOnce.Do(func() {
		if w := probeEmojiWidth(); w > 0 {
			emojiWidth = w
		}
	})
}

// EmojiWidth returns the cached emoji-width measurement (1 or 2). 2 is
// the default before DetectEmojiWidth has run or when probing failed.
func EmojiWidth() int { return emojiWidth }

// NarrowEmojis reports whether the active terminal renders VS-16-style
// ambiguous emoji as 1 cell (true) or 2 cells (false). Sites that
// substitute glyphs based on terminal capability use this rather than
// re-deriving the comparison.
func NarrowEmojis() bool { return EmojiWidth() <= 1 }

// probeEmojiWidth performs the actual cursor-position probe. Returns
// the measured width on success or 0 if anything went wrong (the
// caller treats 0 as "use the default").
func probeEmojiWidth() int {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0
	}
	defer func() { _ = tty.Close() }()

	rawFd := tty.Fd()
	// gosec G115 guard: tty.Fd() is a uintptr; on platforms where it
	// could exceed math.MaxInt the int() cast below would wrap. In
	// practice file descriptors are tiny non-negative integers, but
	// the explicit check makes the intent obvious to the linter and
	// any reader.
	if rawFd > uintptr(int(^uint(0)>>1)) {
		return 0
	}
	fd := int(rawFd) //nolint:gosec // bounds-checked above
	if !term.IsTerminal(fd) {
		return 0
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return 0
	}
	defer func() { _ = term.Restore(fd, state) }()

	// Save cursor (DECSC), home (CUP), write the probe emoji, query
	// cursor position (DSR-CPR). Using DECSC/DECRC instead of CSI s/u
	// because they're the most widely supported across terminal types.
	if _, err := tty.Write([]byte("\x1b7\x1b[H" + probeEmoji + "\x1b[6n")); err != nil {
		return 0
	}

	type readResult struct {
		n   int
		err error
	}
	ch := make(chan readResult, 1)
	var buf [64]byte
	go func() {
		n, err := tty.Read(buf[:])
		ch <- readResult{n, err}
	}()

	width := 0
	select {
	case res := <-ch:
		if res.err == nil && res.n > 0 {
			width = parseCursorCol(buf[:res.n])
		}
	case <-time.After(150 * time.Millisecond):
		// Terminal didn't reply — fall through with width = 0 so the
		// caller keeps its default. The blocked goroutine resolves
		// when the next read returns or the TTY is closed by the
		// deferred close.
	}

	// Restore the cursor and clear the line we wrote the probe on so
	// nothing leaks onto the user's screen.
	_, _ = tty.Write([]byte("\x1b8\x1b[2K"))

	return width
}

// cursorPosRE matches the Cursor Position Report response shape:
//
//	ESC [ <row> ; <col> R
var cursorPosRE = regexp.MustCompile(`\x1b\[(\d+);(\d+)R`)

// parseCursorCol returns the cell width inferred from a CPR response.
// The probe wrote the emoji at column 1, so the cursor's column on
// reply is `width + 1`. Returns 0 on parse failure.
func parseCursorCol(b []byte) int {
	m := cursorPosRE.FindSubmatch(b)
	if m == nil {
		return 0
	}
	col, err := strconv.Atoi(string(m[2]))
	if err != nil || col < 2 {
		return 0
	}
	return col - 1
}
