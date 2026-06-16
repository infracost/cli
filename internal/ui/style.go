package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/infracost/cli/pkg/logging"
)

// Success prints a green checkmark followed by the message.
func Success(msg string) {
	_, _ = fmt.Fprintf(logging.Output(), "  %s  %s\n", Positive("✔"), msg)
}

// Successf prints a green checkmark followed by a formatted message.
func Successf(format string, args ...any) {
	Success(fmt.Sprintf(format, args...))
}

// Warn prints a yellow warning symbol followed by the message.
func Warn(msg string) {
	_, _ = fmt.Fprintf(logging.Output(), "  %s  %s\n", Caution("!"), msg)
}

// Warnf prints a yellow warning symbol followed by a formatted message.
func Warnf(format string, args ...any) {
	Warn(fmt.Sprintf(format, args...))
}

// Fail prints a red cross followed by the message.
func Fail(msg string) {
	_, _ = fmt.Fprintf(logging.Output(), "  %s  %s\n", Danger("✗"), msg)
}

// Failf prints a red cross followed by a formatted message.
func Failf(format string, args ...any) {
	Fail(fmt.Sprintf(format, args...))
}

// Step prints an info-colored arrow followed by the message.
func Step(msg string) {
	_, _ = fmt.Fprintf(logging.Output(), "  %s  %s\n", Info("→"), msg)
}

// Stepf prints an info-colored arrow followed by a formatted message.
func Stepf(format string, args ...any) {
	Step(fmt.Sprintf(format, args...))
}

// Heading prints a bold brand-colored section heading.
func Heading(msg string) {
	_, _ = fmt.Fprintf(logging.Output(), "%s\n", Bold(Brand(msg)))
}

// Headingf prints a bold brand-colored formatted section heading.
func Headingf(format string, args ...any) {
	Heading(fmt.Sprintf(format, args...))
}

// Hint prints an indented hint line with an info-colored arrow.
// indent is the number of leading spaces before the arrow.
func Hint(indent int, msg string) {
	_, _ = fmt.Fprintf(logging.Output(), "%s%s  %s\n", strings.Repeat(" ", indent), Info("→"), msg)
}

// Hintf prints a formatted indented hint.
func Hintf(indent int, format string, args ...any) {
	Hint(indent, fmt.Sprintf(format, args...))
}

// IsInteractive reports whether the process can run interactive prompts
// (huh selects, confirms, etc.). This is true only when stdin is a
// terminal AND the controlling terminal is openable.
//
// Both checks matter: huh/bubbletea opens /dev/tty directly on Unix,
// independent of stdin, so a stdin-only check returns true in
// environments where the prompt will then immediately fail with
// "could not open a new TTY: open /dev/tty: device not configured"
// (e.g. when invoked from agent harnesses like Claude Code that pipe a
// pty for stdin but don't expose a controlling terminal). Returning
// false here lets callers skip the prompt and surface a useful flag
// hint instead.
func IsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	if (info.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	return canOpenControllingTTY()
}

// EraseLastLines moves the cursor up n lines and erases from there to
// the bottom of the screen. Useful for replacing transient TUI fragments
// (an instructions card the user has acknowledged, a spinner that's
// finished) with a more compact final state. No-op when stdout isn't a
// TTY — writing the raw escape codes there would dump as garbage in
// piped output.
func EraseLastLines(n int) {
	if n <= 0 || !ColorEnabled() {
		return
	}
	// CSI n F: move cursor n lines up to column 1.
	// CSI J:   erase from cursor to end of screen.
	fmt.Printf("\x1b[%dF\x1b[J", n)
}

// PressEnter prints a message and waits for the user to press Enter.
// Returns true if the user pressed Enter, false on EOF or error (e.g.
// non-interactive stdin).
func PressEnter(msg string) bool {
	_, _ = fmt.Fprint(logging.Output(), msg)
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return err == nil
}

// OpenOrContinue displays a URL and prompts the user to press Enter to open
// it in their browser. The user can press Ctrl+C to skip. If stdin is
// non-interactive (e.g. in tests), the browser is not opened.
func OpenOrContinue(url string) {
	_, _ = fmt.Fprintf(logging.Output(), "  %s\n", Code(url))
	if !PressEnter("\nPress Enter to open in your browser...") {
		return
	}
	if err := browser.Open(url); err != nil {
		Failf("Failed to open browser. Visit the URL manually:\n   %s", Code(url))
	} else {
		Successf("Opened %s in your browser.", Code(url))
	}
}
