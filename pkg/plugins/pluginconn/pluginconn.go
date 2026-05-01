// Package pluginconn provides shared go-plugin connect plumbing.
package pluginconn

import (
	"os"
	"runtime"
	"time"

	"github.com/hashicorp/go-hclog"
)

// ConnectOptions configures a plugin connect call.
type ConnectOptions struct {
	// Level is the verbosity for the default logger. Ignored when Logger is set.
	Level hclog.Level
	// Logger receives plugin handshake logs. If nil, a logger is created at Level that writes to os.Stderr.
	Logger hclog.Logger
}

// ResolveLogger returns o.Logger when set, otherwise builds a default stderr logger at o.Level.
func (o ConnectOptions) ResolveLogger() hclog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return hclog.New(&hclog.LoggerOptions{
		Level:  o.Level,
		Output: os.Stderr,
	})
}

// StartTimeout returns the go-plugin StartTimeout, extended on Windows to tolerate AV scans on first run.
func StartTimeout() time.Duration {
	if runtime.GOOS == "windows" {
		return 3 * time.Minute
	}
	return time.Minute // go-plugin default
}
