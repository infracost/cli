//go:build windows

package ui

import "os"

// canOpenControllingTTY reports whether the controlling console is
// usable for huh/bubbletea prompts on Windows.
//
// Mirrors the /dev/tty preflight on Unix. A char-device stdin is not
// enough on its own — agent harnesses (e.g. Claude Code on Windows) can
// hand the process a char-device pty for stdin while the process has no
// real attached console, and huh/bubbletea would then render escape
// sequences into a transcript that nobody is reading and never receive
// a selection. Bubbletea opens CONIN$/CONOUT$ when it actually starts
// the prompt; we attempt the same up-front and bail with a useful flag
// hint when it fails.
func canOpenControllingTTY() bool {
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = in.Close()
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = out.Close()
	return true
}
