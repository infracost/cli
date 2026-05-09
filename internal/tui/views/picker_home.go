package views

import "os"

// userHomeDir is a tiny indirection over os.UserHomeDir so picker.go's
// homeDir func value can override it in tests. Real callers go
// straight through.
func userHomeDir() (string, error) { return os.UserHomeDir() }
