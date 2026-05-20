package inspect

import (
	"fmt"
	"io"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
)

// diagnosticEntry pairs a project name with one of its diagnostics so the
// flat list can preserve the per-project attribution that the rendered view
// shows in muted parentheses.
type diagnosticEntry struct {
	Project    string                  `json:"project"`
	Diagnostic format.DiagnosticOutput `json:"diagnostic"`
}

// WriteDiagnostics renders every per-project diagnostic from the latest scan
// (critical and warning). Powers `inspect --diagnostics`. The critical-only
// follow-up shown beneath a scan/price summary goes through
// WriteSummaryDiagnostics instead.
func WriteDiagnostics(w io.Writer, data *format.Output, opts Options) error {
	entries := collectDiagnostics(data, true)

	if opts.Structured() {
		return writeStructured(w, entries, opts)
	}

	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, ui.Positive("✓ No diagnostics."))
		return err
	}

	writeSectionHeading(w, "Diagnostics", fmt.Sprintf("(%d)", len(entries)))
	writeDiagnosticEntries(w, entries, hasMultipleProjects(data))
	return nil
}

// WriteSummaryDiagnostics prints the diagnostics block rendered beneath a
// scan/price summary. The default is critical-only — that matches what the
// summary box already counts and keeps the headline view tight. Pass
// includeWarnings to also surface warning-severity entries (e.g. when the
// user runs `scan --include-warnings`).
//
// Returns silently when there's nothing to show — the summary already
// reports zero counts and an empty section would be noise.
func WriteSummaryDiagnostics(w io.Writer, data *format.Output, includeWarnings bool) {
	entries := collectDiagnostics(data, includeWarnings)
	if len(entries) == 0 {
		return
	}

	_, _ = fmt.Fprintln(w)
	writeSectionHeading(w, "Diagnostics", fmt.Sprintf("(%d)", len(entries)))
	writeDiagnosticEntries(w, entries, hasMultipleProjects(data))
}

func collectDiagnostics(data *format.Output, includeWarnings bool) []diagnosticEntry {
	var entries []diagnosticEntry
	for _, p := range data.Projects {
		for _, d := range p.Diagnostics {
			if d.Severity != "critical" && !includeWarnings {
				continue
			}
			entries = append(entries, diagnosticEntry{
				Project:    p.ProjectName,
				Diagnostic: d,
			})
		}
	}
	return entries
}

func hasMultipleProjects(data *format.Output) bool {
	return len(data.Projects) > 1
}

func writeDiagnosticEntries(w io.Writer, entries []diagnosticEntry, showProject bool) {
	for _, e := range entries {
		if showProject {
			format.WriteDiagnosticOutputWithSuffix(w, e.Diagnostic, " "+ui.Muted("("+e.Project+")"))
		} else {
			format.WriteDiagnosticOutput(w, e.Diagnostic)
		}
	}
}
