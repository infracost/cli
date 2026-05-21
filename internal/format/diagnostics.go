package format

import (
	"fmt"
	"io"
	"os"

	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/diagnostic"
)

// Diagnostics prints the diagnostics to stderr.
func Diagnostics(diags *diagnostic.Diagnostics) {
	for _, diag := range diags.Unwrap() {
		Diagnostic(diag)
	}
}

// Diagnostic prints a diagnostic to stderr.
func Diagnostic(diag *diagnostic.Diagnostic) {
	severity := "info"
	switch {
	case diag.Critical:
		severity = "critical"
	case diag.Warning:
		severity = "warning"
	}
	prefix := diagnosticPrefix(diag, severity)
	colorize := severityColorize(severity)
	location := formatSourceRange(diag.SourceRange)
	_, _ = fmt.Fprintln(os.Stderr, formatDiagnosticLine(colorize, prefix, diag.Error, location, ""))
}

// WriteDiagnosticOutput writes a converted diagnostic to w in the same
// colored "<prefix>: <message>" style used for top-level diagnostics. The
// inspect view shares this so per-project diagnostic lines look identical to
// the stderr ones rendered by main.
func WriteDiagnosticOutput(w io.Writer, d DiagnosticOutput) {
	WriteDiagnosticOutputWithSuffix(w, d, "")
}

// WriteDiagnosticOutputWithSuffix is like WriteDiagnosticOutput but appends
// suffix before the trailing newline. Used by the inspect view to tack a
// muted "(project-name)" onto each line when more than one project is in
// the result.
func WriteDiagnosticOutputWithSuffix(w io.Writer, d DiagnosticOutput, suffix string) {
	colorize := severityColorize(d.Severity)
	_, _ = fmt.Fprintln(w, formatDiagnosticLine(colorize, d.Prefix, d.Message, d.Location, suffix))
}

// formatDiagnosticLine assembles a single rendered diagnostic line:
// "<colorized prefix>: <message>[ — <muted location>][<suffix>]". Location is
// rendered in muted style so it reads as metadata; suffix is appended verbatim
// (callers already style it — e.g. the inspect view passes a muted "(project)").
func formatDiagnosticLine(colorize func(string) string, prefix, message, location, suffix string) string {
	line := colorize(prefix+":") + " " + message
	if location != "" {
		line += " " + ui.Muted("— "+location)
	}
	return line + suffix
}

func severityColorize(severity string) func(string) string {
	switch severity {
	case "critical":
		return ui.Danger
	case "warning":
		return ui.Caution
	default:
		return ui.Muted
	}
}
