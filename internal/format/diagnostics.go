package format

import (
	"os"
	"strings"

	"github.com/infracost/go-proto/pkg/diagnostic"
	"github.com/liamg/tml"
)

// Diagnostics prints the diagnostics to stderr.
func Diagnostics(diags *diagnostic.Diagnostics) {
	for _, diag := range diags.Unwrap() {
		Diagnostic(diag)
	}
}

// Diagnostic prints a diagnostic to stderr.
func Diagnostic(diag *diagnostic.Diagnostic) {
	prefix := diagnostic.MessagePrefix(diag.Type)
	if strings.HasPrefix(prefix, "DIAGNOSTIC_TYPE_") {
		// No human-readable prefix for this type, use severity instead.
		if diag.Warning {
			prefix = "Warning"
		} else {
			prefix = "Error"
		}
	}
	_ = tml.Fprintf(os.Stderr, "<lightred>%s:</lightred> %s\n", prefix, diag.Error)
}
