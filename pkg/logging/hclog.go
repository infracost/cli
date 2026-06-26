package logging

import (
	"io"
	stdlog "log"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/rs/zerolog"
)

// pluginHCLogger adapts hclog.Logger calls into the CLI's zerolog logger.
// go-plugin reads each plugin subprocess's stderr line, recovers its level
// (from a "[LEVEL]" prefix or "@level" JSON), and re-emits it through this
// logger — so plugin log lines surface at the correct level and coordinate
// with the shared output writer. go-plugin calls Named() with the plugin
// binary name, which we attach as a "plugin" field.
type pluginHCLogger struct {
	name  string
	level hclog.Level
}

// PluginHCLogger returns an hclog.Logger that forwards into the CLI logger at
// the CLI's effective level.
func PluginHCLogger() hclog.Logger {
	return &pluginHCLogger{level: PluginLogLevel()}
}

func (l *pluginHCLogger) event(level hclog.Level) *zerolog.Event {
	switch level {
	case hclog.Trace:
		return logger.Trace()
	case hclog.Debug:
		return logger.Debug()
	case hclog.Info:
		return logger.Info()
	case hclog.Warn:
		return logger.Warn()
	case hclog.Error:
		return logger.Error()
	default:
		return logger.Debug()
	}
}

func (l *pluginHCLogger) write(level hclog.Level, msg string, args ...any) {
	e := l.event(level)
	if l.name != "" {
		e = e.Str("plugin", l.name)
	}
	if len(args) > 0 {
		e = e.Fields(args)
	}
	e.Msg(stripLevelPrefix(msg))
}

// stripLevelPrefix removes a redundant leading "[LEVEL]" tag. go-plugin passes
// the plugin's full stderr line (including its own level prefix) as the message
// after parsing the level from it; we log at that level, so the prefix would
// otherwise be duplicated in the output.
func stripLevelPrefix(msg string) string {
	for _, tag := range [...]string{"[TRACE]", "[DEBUG]", "[INFO]", "[WARN]", "[ERROR]"} {
		if strings.HasPrefix(msg, tag) {
			return strings.TrimLeft(msg[len(tag):], " ")
		}
	}
	return msg
}

func (l *pluginHCLogger) Log(level hclog.Level, msg string, args ...any) {
	l.write(level, msg, args...)
}
func (l *pluginHCLogger) Trace(msg string, args ...any) { l.write(hclog.Trace, msg, args...) }
func (l *pluginHCLogger) Debug(msg string, args ...any) { l.write(hclog.Debug, msg, args...) }
func (l *pluginHCLogger) Info(msg string, args ...any)  { l.write(hclog.Info, msg, args...) }
func (l *pluginHCLogger) Warn(msg string, args ...any)  { l.write(hclog.Warn, msg, args...) }
func (l *pluginHCLogger) Error(msg string, args ...any) { l.write(hclog.Error, msg, args...) }

func (l *pluginHCLogger) IsTrace() bool { return l.level <= hclog.Trace }
func (l *pluginHCLogger) IsDebug() bool { return l.level <= hclog.Debug }
func (l *pluginHCLogger) IsInfo() bool  { return l.level <= hclog.Info }
func (l *pluginHCLogger) IsWarn() bool  { return l.level <= hclog.Warn }
func (l *pluginHCLogger) IsError() bool { return l.level <= hclog.Error }

func (l *pluginHCLogger) ImpliedArgs() []any         { return nil }
func (l *pluginHCLogger) With(_ ...any) hclog.Logger { return l }
func (l *pluginHCLogger) Name() string               { return l.name }
func (l *pluginHCLogger) GetLevel() hclog.Level      { return l.level }
func (l *pluginHCLogger) SetLevel(level hclog.Level) { l.level = level }

func (l *pluginHCLogger) Named(name string) hclog.Logger {
	if l.name != "" {
		name = l.name + "." + name
	}
	return &pluginHCLogger{name: name, level: l.level}
}

func (l *pluginHCLogger) ResetNamed(name string) hclog.Logger {
	return &pluginHCLogger{name: name, level: l.level}
}

func (l *pluginHCLogger) StandardLogger(opts *hclog.StandardLoggerOptions) *stdlog.Logger {
	return stdlog.New(l.StandardWriter(opts), "", 0)
}

func (l *pluginHCLogger) StandardWriter(_ *hclog.StandardLoggerOptions) io.Writer {
	return output
}
