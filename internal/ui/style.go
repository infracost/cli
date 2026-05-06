package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/infracost/cli/pkg/auth/browser"
)

// Success prints a green checkmark followed by the message.
func Success(msg string) {
	fmt.Printf("  %s  %s\n", Positive("✔"), msg)
}

// Successf prints a green checkmark followed by a formatted message.
func Successf(format string, args ...any) {
	Success(fmt.Sprintf(format, args...))
}

// Warn prints a yellow warning symbol followed by the message.
func Warn(msg string) {
	fmt.Printf("  %s  %s\n", Caution("!"), msg)
}

// Warnf prints a yellow warning symbol followed by a formatted message.
func Warnf(format string, args ...any) {
	Warn(fmt.Sprintf(format, args...))
}

// Fail prints a red cross followed by the message.
func Fail(msg string) {
	fmt.Printf("  %s  %s\n", Danger("✗"), msg)
}

// Failf prints a red cross followed by a formatted message.
func Failf(format string, args ...any) {
	Fail(fmt.Sprintf(format, args...))
}

// Step prints an info-colored arrow followed by the message.
func Step(msg string) {
	fmt.Printf("  %s  %s\n", Info("→"), msg)
}

// Stepf prints an info-colored arrow followed by a formatted message.
func Stepf(format string, args ...any) {
	Step(fmt.Sprintf(format, args...))
}

// Heading prints a bold brand-colored section heading.
func Heading(msg string) {
	fmt.Printf("%s\n", Bold(Brand(msg)))
}

// Headingf prints a bold brand-colored formatted section heading.
func Headingf(format string, args ...any) {
	Heading(fmt.Sprintf(format, args...))
}

// Hint prints an indented hint line with an info-colored arrow.
// indent is the number of leading spaces before the arrow.
func Hint(indent int, msg string) {
	fmt.Printf("%s%s  %s\n", strings.Repeat(" ", indent), Info("→"), msg)
}

// Hintf prints a formatted indented hint.
func Hintf(indent int, format string, args ...any) {
	Hint(indent, fmt.Sprintf(format, args...))
}

// IsInteractive reports whether stdin is a terminal. Interactive prompts
// (huh selects, confirms, etc.) should be skipped when this returns false
// to avoid blocking in tests or piped environments.
func IsInteractive() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// PressEnter prints a message and waits for the user to press Enter.
// Returns true if the user pressed Enter, false on EOF or error (e.g.
// non-interactive stdin).
func PressEnter(msg string) bool {
	fmt.Printf("%s", msg)
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return err == nil
}

// OpenOrContinue displays a URL and prompts the user to press Enter to open
// it in their browser. The user can press Ctrl+C to skip. If stdin is
// non-interactive (e.g. in tests), the browser is not opened.
func OpenOrContinue(url string) {
	fmt.Printf("  %s\n", Code(url))
	if !PressEnter("\nPress Enter to open in your browser...") {
		return
	}
	if err := browser.Open(url); err != nil {
		Failf("Failed to open browser. Visit the URL manually:\n   %s", Code(url))
	} else {
		Successf("Opened %s in your browser.", Code(url))
	}
}
