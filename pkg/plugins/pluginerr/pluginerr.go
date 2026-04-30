// Package pluginerr defines typed errors for go-plugin connect failures.
package pluginerr

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrPluginNotFound is returned when the plugin binary is missing or inaccessible.
var ErrPluginNotFound = errors.New("plugin binary not found")

// ErrPluginNotExecutable is returned when the plugin binary lacks the executable bit (POSIX only).
var ErrPluginNotExecutable = errors.New("plugin binary not executable")

// ErrPluginExecFailed is returned when the plugin process could not be spawned or exited before handshake.
var ErrPluginExecFailed = errors.New("plugin process failed to start")

// ErrPluginHandshakeTimeout is returned when the plugin handshake exceeds StartTimeout.
var ErrPluginHandshakeTimeout = errors.New("plugin handshake timed out")

// ErrPluginHandshake is returned for handshake failures other than exec failure or timeout.
var ErrPluginHandshake = errors.New("plugin handshake failed")

// ClassifyConnect maps a raw go-plugin connect error to a typed sentinel, preserving the original via wrapping.
func ClassifyConnect(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		ErrPluginNotFound,
		ErrPluginNotExecutable,
		ErrPluginExecFailed,
		ErrPluginHandshakeTimeout,
		ErrPluginHandshake,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}

	// go-plugin does not export sentinels for these conditions, so we match
	// on the stable error strings emitted by hashicorp/go-plugin.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "timeout while waiting"):
		return fmt.Errorf("%w: %v", ErrPluginHandshakeTimeout, err)
	case strings.Contains(msg, "plugin exited before"),
		strings.Contains(msg, "exec format error"),
		strings.Contains(msg, "no such file or directory"),
		strings.Contains(msg, "permission denied"):
		return fmt.Errorf("%w: %v", ErrPluginExecFailed, err)
	default:
		return fmt.Errorf("%w: %v", ErrPluginHandshake, err)
	}
}

// WindowsHint adds Windows-specific AV/EDR/firewall guidance for exec or timeout failures; no-op elsewhere.
func WindowsHint(err error, path string, timeout time.Duration) error {
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	switch {
	case errors.Is(err, ErrPluginExecFailed):
		return fmt.Errorf("%w\n\nThe plugin process did not start. On Windows, this is typically caused by antivirus or EDR software blocking the plugin executable.\nTry adding %s to your antivirus exclusions and running again.", err, filepath.Dir(path)) //nolint:revive,staticcheck // multi-line user-facing hint
	case errors.Is(err, ErrPluginHandshakeTimeout):
		return fmt.Errorf("%w\n\nThe plugin process started but did not complete its handshake within %s. On Windows, this is typically caused by host firewall or EDR software blocking the loopback TCP connection between the CLI and the plugin process.\nTry adding %s to your antivirus and firewall exclusions and running again.", err, timeout, filepath.Dir(path)) //nolint:revive,staticcheck // multi-line user-facing hint
	}
	return err
}
