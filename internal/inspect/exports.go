package inspect

import (
	"github.com/infracost/go-proto/pkg/rat"
)

// FormatFileLoc formats "<file>:<line>" with proper handling of zero/negative
// line numbers. Exposed so external renderers (e.g. the TUI) share the same
// display convention as inspect's text output.
func FormatFileLoc(filename string, line int) string {
	return formatFileLoc(filename, line)
}

// HumanMoney formats a monetary amount with the given ISO 4217 currency code,
// matching inspect's text-output convention. Exposed for external renderers.
func HumanMoney(r *rat.Rat, currency string) string {
	return humanMoney(r, currency)
}

// ResourceTypeFromAddress extracts the resource type from a full resource
// address ("aws_instance.web" → "aws_instance"). Exposed for external
// renderers that group/filter resources by type.
func ResourceTypeFromAddress(addr string) string {
	return resourceTypeFromAddress(addr)
}

// TruncateMiddle shortens cell to fit width visible columns by dropping
// runes from the middle and inserting "…", preserving both ends.
// Identifier-shaped values (resource addresses) often differ only in the
// suffix, so middle truncation is more useful than trailing truncation.
// Cells already within width are returned unchanged. Exposed for the TUI.
func TruncateMiddle(cell string, width int) string {
	return truncateCell(cell, width, false)
}

// TruncateEnd is the conventional ".../<elided>…" tail truncation,
// suitable for free-form text where the prefix carries the meaning.
func TruncateEnd(cell string, width int) string {
	return truncateCell(cell, width, true)
}
