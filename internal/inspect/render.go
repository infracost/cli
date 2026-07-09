package inspect

import (
	"io"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/infracost/cli/internal/ui"
)

// This file is the single source of truth for how inspect renders tabular
// data to a human. Every view that emits a list of records (--top-savings,
// the resource-filter views, --group-by --fields, …) goes through
// writeRecordTable so column headers, alignment, truncation and numeric
// right-justification stay identical across the command.
//
// Adding a new tabular field? Declare it once in fieldSpecs below and every
// view that surfaces it renders consistently for free. The registry-coverage
// guard test (render_test.go) fails if a view exposes a field with no spec,
// so drift can't slip in silently.

// fieldSpec describes how one canonical field renders in a human table.
// The field's canonical name (the map key in fieldSpecs) doubles as its
// --json/--llm key, so specs only carry human-presentation concerns.
type fieldSpec struct {
	// header is the column title shown in multi-column human output. Empty
	// means "Title-case the canonical name" (see fieldHeader).
	header string
	// right right-aligns the column — used for numeric and currency values
	// so magnitudes line up for easy scanning.
	right bool
	// prose marks free-text columns (messages, descriptions) so that, when a
	// column has to be shrunk to fit, characters are dropped from the END
	// with a trailing "…" rather than the middle — the start of prose carries
	// the meaning. Identifier-shaped columns keep the default middle
	// truncation so both ends survive.
	prose bool
	// humanize, when set, transforms the stored (machine) string value into
	// its human display form. The stored value is what --json/--llm emit, so
	// this only affects the rendered table (e.g. is_free "true" → "yes").
	humanize func(string) string
}

// fieldSpecs is the registry of every field that can appear in a human
// table. Keys are the canonical field names shared with the structured
// output. Numeric/currency fields are right-aligned; prose fields
// suffix-truncate; the rest use the defaults.
var fieldSpecs = map[string]fieldSpec{
	// Resource / policy identity columns (default left-align, middle-truncate).
	"address":     {header: "Address"},
	"type":        {header: "Type"},
	"provider":    {header: "Provider"},
	"project":     {header: "Project"},
	"resource":    {header: "Resource"},
	"file":        {header: "File"},
	"policy":      {header: "Policy"},
	"policy_slug": {header: "Policy Slug"},
	"kind":        {header: "Kind"},
	"guardrail":   {header: "Guardrail"},
	"budget":      {header: "Budget"},
	"status":      {header: "Status"},

	// Prose columns — suffix truncation reads more naturally than middle.
	"message":     {header: "Message", prose: true},
	"description": {header: "Description", prose: true},

	// Numeric / currency columns — right-aligned so magnitudes align.
	"count":           {header: "Count", right: true},
	"monthly_cost":    {header: "Monthly Cost", right: true},
	"monthly_savings": {header: "Monthly Savings", right: true},
	"limit":           {header: "Limit", right: true},
	"actual spend":    {header: "Actual Spend", right: true},

	// Booleans — rendered as a quiet flag column ("yes" / blank).
	"is_free": {header: "Free", humanize: humanizeBool},
}

// humanizeBool turns the stored "true"/"false" string into a quiet flag: a
// true value shows "yes"; anything else renders blank so the column stays
// visually calm and only the notable (free) rows draw the eye.
func humanizeBool(v string) string {
	if v == "true" {
		return "yes"
	}
	return ""
}

var titleCaser = cases.Title(language.English)

// fieldHeader returns the human column header for a canonical field name,
// falling back to a Title-cased version of the name for fields without an
// explicit header (keeps group-by dimension columns sensible without needing
// a spec for every combination).
func fieldHeader(field string) string {
	if spec, ok := fieldSpecs[field]; ok && spec.header != "" {
		return spec.header
	}
	return titleCaser.String(strings.ReplaceAll(field, "_", " "))
}

// fieldDisplay returns the rendered (human) value for a field given its
// stored string value, applying the field's humanize transform when present.
func fieldDisplay(field, value string) string {
	if spec, ok := fieldSpecs[field]; ok && spec.humanize != nil {
		return spec.humanize(value)
	}
	return value
}

// writeRecordTable is the one renderer for human tabular output across
// inspect. rows are keyed by canonical field name; only the fields in the
// requested order are shown.
//
//   - A single field renders as one bare value per line, no header — this is
//     the muscle-memory "give me the list" shape (--addresses-only and any
//     single --fields projection) that pipes cleanly into other tools.
//   - Multiple fields render as an aligned table via renderTable, with
//     per-column alignment and truncation taken from the field registry so
//     numbers right-justify and headers read consistently.
//
// maxWidth caps the multi-column table's total width (0 = unconstrained, as
// in tests / non-TTY pipes). The single-column form is never width-capped —
// bare values must survive intact for downstream tooling.
func writeRecordTable(w io.Writer, fields []string, rows []map[string]string, maxWidth int) {
	if len(fields) == 0 {
		return
	}

	if len(fields) == 1 {
		f := fields[0]
		for _, row := range rows {
			_, _ = io.WriteString(w, fieldDisplay(f, row[f])+"\n")
		}
		return
	}

	cols := make([]tableCol, 0, len(fields))
	for _, f := range fields {
		spec := fieldSpecs[f]
		cols = append(cols, tableCol{
			header:        fieldHeader(f),
			right:         spec.right,
			truncateRight: spec.prose,
		})
	}

	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, 0, len(fields))
		for _, f := range fields {
			cells = append(cells, fieldDisplay(f, row[f]))
		}
		tableRows = append(tableRows, cells)
	}

	renderTable(w, cols, tableRows, maxWidth)
}

// writeNoMatch writes the standard muted empty-state line used when a
// selection/filter view matches nothing. Views where "nothing" is good news
// (no failing policies, no diagnostics) use the positive ✓ form instead —
// this helper is for neutral "your query returned no rows" cases so their
// styling stays consistent.
func writeNoMatch(w io.Writer, msg string) error {
	_, err := io.WriteString(w, ui.Muted(msg)+"\n")
	return err
}
