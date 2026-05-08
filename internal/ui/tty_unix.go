//go:build !windows

package ui

import "os"

// canOpenControllingTTY reports whether /dev/tty can be opened. This is
// what huh/bubbletea ultimately need — a stdin char-device check is not
// sufficient because the prompt library reads/writes /dev/tty directly.
func canOpenControllingTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
