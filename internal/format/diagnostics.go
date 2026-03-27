package format

import (
	"fmt"
	"os"

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
	_, _ = fmt.Fprintln(os.Stderr, diag.FormatMessage())
}
