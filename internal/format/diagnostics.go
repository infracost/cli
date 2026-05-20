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
	prefix := diagnosticPrefix(diag)
	colorize := severityColorize(diag.Warning)
	_, _ = fmt.Fprintf(os.Stderr, "%s %s\n", colorize(prefix+":"), diag.Error)
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
	colorize := severityColorize(d.Severity == "warning")
	_, _ = fmt.Fprintf(w, "%s %s%s\n", colorize(d.Prefix+":"), d.Message, suffix)
}

func severityColorize(warning bool) func(string) string {
	if warning {
		return ui.Caution
	}
	return ui.Danger
}
