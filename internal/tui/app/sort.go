package app

import (
	"sort"

	"github.com/infracost/cli/internal/tui/views"
)

// SortMode determines how the list is ordered. Cycled by the `s` key in
// the order they're declared here.
type SortMode int

const (
	SortByCostDesc SortMode = iota
	SortByAddressAsc
	SortByTypeAsc
)

// label returns a short human-readable name shown in the status bar.
func (s SortMode) label() string {
	switch s {
	case SortByCostDesc:
		return "cost ↓"
	case SortByAddressAsc:
		return "address ↑"
	case SortByTypeAsc:
		return "type ↑"
	}
	return ""
}

// next returns the next mode in the cycle.
func (s SortMode) next() SortMode {
	return SortMode((int(s) + 1) % 3)
}

// applySort returns a new slice sorted according to mode. Stable sort so
// equal keys preserve the previous (cost-desc) ordering, which keeps the
// list visually steady as the user toggles modes.
func applySort(rows []views.ResourceRow, mode SortMode) []views.ResourceRow {
	out := make([]views.ResourceRow, len(rows))
	copy(out, rows)
	switch mode {
	case SortByCostDesc:
		sort.SliceStable(out, func(i, j int) bool {
			ci, cj := out[i].Cost, out[j].Cost
			switch {
			case ci == nil && cj == nil:
				return out[i].Address < out[j].Address
			case ci == nil:
				return false
			case cj == nil:
				return true
			}
			if ci.Equals(cj) {
				return out[i].Address < out[j].Address
			}
			return ci.GreaterThan(cj)
		})
	case SortByAddressAsc:
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Address < out[j].Address
		})
	case SortByTypeAsc:
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Type == out[j].Type {
				return out[i].Address < out[j].Address
			}
			return out[i].Type < out[j].Type
		})
	}
	return out
}
