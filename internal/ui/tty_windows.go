//go:build windows

package ui

// canOpenControllingTTY reports whether the controlling terminal is
// usable for huh/bubbletea prompts. On Windows we trust the stdin
// char-device check that ran before this — the prompt library does not
// rely on /dev/tty, so there is no extra accessibility test to perform.
func canOpenControllingTTY() bool {
	return true
}
