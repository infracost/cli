package ui

import (
	"fmt"

	"github.com/liamg/tml"
)

// Success prints a green checkmark followed by the message.
func Success(msg string) {
	tml.Printf("<lightgreen>✔</lightgreen>  %s\n", msg)
}

// Successf prints a green checkmark followed by a formatted message.
func Successf(format string, args ...any) {
	Success(fmt.Sprintf(format, args...))
}

// Warn prints a yellow warning symbol followed by the message.
func Warn(msg string) {
	tml.Printf("<lightyellow>!</lightyellow>  %s\n", msg)
}

// Warnf prints a yellow warning symbol followed by a formatted message.
func Warnf(format string, args ...any) {
	Warn(fmt.Sprintf(format, args...))
}

// Fail prints a red cross followed by the message.
func Fail(msg string) {
	tml.Printf("<lightred>✗</lightred>  %s\n", msg)
}

// Failf prints a red cross followed by a formatted message.
func Failf(format string, args ...any) {
	Fail(fmt.Sprintf(format, args...))
}

// Step prints a blue arrow followed by the message.
func Step(msg string) {
	tml.Printf("<lightblue>→</lightblue>  %s\n", msg)
}

// Stepf prints a blue arrow followed by a formatted message.
func Stepf(format string, args ...any) {
	Step(fmt.Sprintf(format, args...))
}
