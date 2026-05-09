package views

import (
	"strings"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
)

// FilterRows applies the given expression to rows. The grammar is dual:
//   - If expr contains "=", it's parsed as an inspect ParseFilter
//     expression (key=value AND'd predicates) and applied via
//     inspect.Filter to the original Output, then projected back.
//   - Otherwise, it's a case-insensitive substring match against the
//     resource address. This is what users naturally reach for in a TUI.
//
// Pure: no terminal output, no globals — safe to call per keystroke.
func FilterRows(rows []ResourceRow, out *format.Output, expr string) []ResourceRow {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return rows
	}

	if strings.Contains(expr, "=") {
		return filterByInspectExpr(rows, out, expr)
	}

	needle := strings.ToLower(expr)
	filtered := make([]ResourceRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Address), needle) ||
			strings.Contains(strings.ToLower(r.Type), needle) ||
			strings.Contains(strings.ToLower(r.Project), needle) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// filterByInspectExpr runs the row set through inspect's structured
// filter grammar. Falls back to substring matching when the expression
// fails to parse so a half-typed key=value still does *something* useful
// instead of disappearing every row.
func filterByInspectExpr(rows []ResourceRow, out *format.Output, expr string) []ResourceRow {
	if out == nil {
		return rows
	}
	opts := inspect.Options{Filter: expr}
	if err := inspect.ParseFilter(opts.Filter, &opts); err != nil {
		// Mid-typing — fall through to substring on the partial value.
		return filterRowsSubstring(rows, expr)
	}
	filtered := inspect.Filter(out, opts)
	if filtered == nil {
		return nil
	}

	keep := map[string]struct{}{}
	for _, p := range filtered.Projects {
		for _, r := range p.Resources {
			keep[p.ProjectName+"\x00"+r.Name] = struct{}{}
		}
	}
	result := make([]ResourceRow, 0, len(keep))
	for _, r := range rows {
		if _, ok := keep[r.Project+"\x00"+r.Address]; ok {
			result = append(result, r)
		}
	}
	return result
}

func filterRowsSubstring(rows []ResourceRow, expr string) []ResourceRow {
	needle := strings.ToLower(expr)
	out := make([]ResourceRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.Address), needle) {
			out = append(out, r)
		}
	}
	return out
}
