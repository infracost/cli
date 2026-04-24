package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/liamg/tml"
)

// Success prints a green checkmark followed by the message.
func Success(msg string) {
	tml.Printf("  <lightgreen>✔</lightgreen>  %s\n", msg)
}

// Successf prints a green checkmark followed by a formatted message.
func Successf(format string, args ...any) {
	Success(fmt.Sprintf(format, args...))
}

// Warn prints a yellow warning symbol followed by the message.
func Warn(msg string) {
	tml.Printf("  <lightyellow>!</lightyellow>  %s\n", msg)
}

// Warnf prints a yellow warning symbol followed by a formatted message.
func Warnf(format string, args ...any) {
	Warn(fmt.Sprintf(format, args...))
}

// Fail prints a red cross followed by the message.
func Fail(msg string) {
	tml.Printf("  <lightred>✗</lightred>  %s\n", msg)
}

// Failf prints a red cross followed by a formatted message.
func Failf(format string, args ...any) {
	Fail(fmt.Sprintf(format, args...))
}

// Step prints a blue arrow followed by the message.
func Step(msg string) {
	tml.Printf("  <lightblue>→</lightblue>  %s\n", msg)
}

// Stepf prints a blue arrow followed by a formatted message.
func Stepf(format string, args ...any) {
	Step(fmt.Sprintf(format, args...))
}

// Heading prints a bold section heading.
func Heading(msg string) {
	tml.Printf("<bold>%s</bold>\n", msg)
}

// Headingf prints a bold formatted section heading.
func Headingf(format string, args ...any) {
	Heading(fmt.Sprintf(format, args...))
}

// Hint prints an indented hint line with a blue arrow.
// indent is the number of leading spaces before the arrow.
func Hint(indent int, msg string) {
	tml.Printf("%s<lightblue>→</lightblue>  %s\n", strings.Repeat(" ", indent), msg)
}

// Hintf prints a formatted indented hint.
func Hintf(indent int, format string, args ...any) {
	Hint(indent, fmt.Sprintf(format, args...))
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
	fmt.Printf("  %s\n", url)
	if !PressEnter("\nPress Enter to open in your browser...") {
		return
	}
	if err := browser.Open(url); err != nil {
		Failf("Failed to open browser. Visit the URL manually:\n   %s", url)
	} else {
		Successf("Opened %s in your browser.", url)
	}
}
