package logging

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/rs/zerolog"
)

var (
	_ process.Processor = (*Config)(nil)

	loggerConfigured bool
	logger           zerolog.Logger

	// output is a swappable destination so callers (e.g. the spinner)
	// can redirect log writes through a TUI that owns stderr.
	output = &outputRouter{target: os.Stderr}
)

type outputRouter struct {
	mu     sync.Mutex
	target io.Writer
}

func (w *outputRouter) Write(p []byte) (int, error) {
	w.mu.Lock()
	target := w.target
	w.mu.Unlock()
	return target.Write(p)
}

// SetOutput swaps the writer used for log output and returns a function
// that restores the previous writer. Use this when something else owns
// stderr (e.g. a bubbletea spinner) so log lines aren't clobbered by
// concurrent redraws.
func SetOutput(w io.Writer) func() {
	output.mu.Lock()
	prev := output.target
	output.target = w
	output.mu.Unlock()
	return func() {
		output.mu.Lock()
		output.target = prev
		output.mu.Unlock()
	}
}

// Output returns the shared status writer. Use this for any status or
// decoration output (UI checkmarks, hints, headings) so it coordinates
// with whatever owns stderr — currently a bubbletea spinner via
// SetOutput.
func Output() io.Writer { return output }

type Config struct {
	WriteLevel string `env:"INFRACOST_CLI_LOG_LEVEL" default:"warn"`
	JSON       bool   `flagvalue:"json"`
}

// ToHCLogLevel converts the WriteLevel to an hclog.Level for use in logging outputs from the
// plugins.
//
// TODO: We probably should make the plugins load separate log levels for each plugin.
func (config *Config) ToHCLogLevel() hclog.Level {
	switch strings.ToLower(config.WriteLevel) {
	case "trace":
		return hclog.Trace
	case "debug":
		return hclog.Debug
	case "info":
		return hclog.Info
	case "warn":
		return hclog.Warn
	case "panic", "fatal", "error":
		return hclog.Error
	case "disabled":
		return hclog.Off
	default:
		return hclog.NoLevel
	}
}

func (config *Config) Process() {
	if loggerConfigured {
		return
	}
	loggerConfigured = true

	level, err := zerolog.ParseLevel(config.WriteLevel)
	if err != nil {
		level = zerolog.WarnLevel
	}

	logger = zerolog.New(output).Level(level).With().Timestamp().Logger()
	if !config.JSON {
		logger = logger.Output(zerolog.ConsoleWriter{Out: output})
	}

	if err != nil {
		Errorf("failed to parse log level (%q), defaulting to WARN", config.WriteLevel)
	}
}

func (config *Config) ForTest(t *testing.T) {
	t.Helper()
	level, err := zerolog.ParseLevel(config.WriteLevel)
	if err != nil {
		t.Fatal(err)
	}

	writer := zerolog.NewTestWriter(t)
	logger = zerolog.New(writer).Level(level).With().Timestamp().Logger()
	if !config.JSON {
		logger = logger.Output(zerolog.ConsoleWriter{Out: writer})
	}
}

func WithError(v error) *zerolog.Event {
	l := logger.With().Err(v).Logger()
	return l.Error()
}

func Trace(msg string) {
	logger.Trace().Msg(msg)
}

func Tracef(format string, v ...interface{}) {
	logger.Trace().Msgf(format, v...)
}

func Debug(msg string) {
	logger.Debug().Msg(msg)
}

func Debugf(format string, v ...interface{}) {
	logger.Debug().Msgf(format, v...)
}

func Info(msg string) {
	logger.Info().Msg(msg)
}

func Infof(format string, v ...interface{}) {
	logger.Info().Msgf(format, v...)
}

func Warn(msg string) {
	logger.Warn().Msg(msg)
}

func Warnf(format string, v ...interface{}) {
	logger.Warn().Msgf(format, v...)
}

func Error(msg string) {
	logger.Error().Msg(msg)
}

func Errorf(format string, v ...interface{}) {
	logger.Error().Msgf(format, v...)
}

func Fatal(msg string) {
	logger.Fatal().Msg(msg)
}

func Fatalf(format string, v ...interface{}) {
	logger.Fatal().Msgf(format, v...)
}

func Panic(msg string) {
	logger.Panic().Msg(msg)
}

func Panicf(format string, v ...interface{}) {
	logger.Panic().Msgf(format, v...)
}
