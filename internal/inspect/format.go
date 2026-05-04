package inspect

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
)

// Symbols used across summary and inspect output, each tied to a category.
// All render at 2 cells in modern terminals.
//   warnEmoji    (U+26A0  + VS-16) — failing FinOps/Tagging policy.
//   stopEmoji    (U+1F6D1)         — triggered guardrail.
//   moneyEmoji   (U+1F4B8)         — over budget.
//   critEmoji    (U+2757)          — critical scan diagnostic.
//   finopsIcon   (U+2699  + VS-16) — kind prefix for finops policies.
//   taggingIcon  (U+1F3F7 + VS-16) — kind prefix for tagging policies.
// VS-16 is needed on codepoints that default to text presentation (warning,
// gear, label) to force the 2-cell emoji glyph; PrintableWidth bumps width
// by 1 per VS-16 since runewidth doesn't credit it. Wide-by-default emojis
// (stop, money, crit) must NOT carry VS-16 — that would over-count.
const (
	warnEmoji   = "⚠️"
	stopEmoji   = "🛑"
	moneyEmoji  = "💸"
	critEmoji   = "❗"
	finopsIcon  = "⚙️"
	taggingIcon = "🏷️"
)

// currencySymbol returns the leading symbol for an ISO 4217 code. Falls back
// to "<code> " (with a trailing space) for currencies we don't have a symbol
// for, so the value still reads naturally (e.g. "JPY 1,200").
func currencySymbol(code string) string {
	switch code {
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	default:
		return code + " "
	}
}

// humanInt formats n with thousands separators (e.g. 29318 → "29,318").
func humanInt(n int) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	pre := len(s) % 3
	if pre == 0 {
		pre = 3
	}
	var b strings.Builder
	b.WriteString(s[:pre])
	for i := pre; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i:i+3])
	}
	return b.String()
}

// humanMoney rounds r to the nearest unit of the currency and prepends the
// currency symbol with thousands separators (e.g. EUR 29318.42 → "€29,318").
// Cents are intentionally dropped — cost estimates already round monthly hours,
// so cent-level precision is false precision.
func humanMoney(r *rat.Rat, currency string) string {
	sym := currencySymbol(currency)
	rounded := r.StringFixed(0)
	n, err := strconv.Atoi(rounded)
	if err != nil {
		return sym + rounded
	}
	return sym + humanInt(n)
}

// humanDollar is the USD shorthand for humanMoney — used by summary code
// where the data model doesn't yet carry currency.
func humanDollar(r *rat.Rat) string {
	return humanMoney(r, "USD")
}

// tableCol describes one column for renderTable.
//   right         — right-align cells (numbers, currency).
//   truncateRight — when the column is shrunk to fit, drop characters from
//                   the END with a trailing "…". Default is to drop from the
//                   MIDDLE ("aws_appauto…dynamodb_read"), which preserves both
//                   ends of identifier-shaped values (resource names, file
//                   paths). Set this for prose-shaped columns where the start
//                   carries the meaning (Message, Description).
type tableCol struct {
	header        string
	right         bool
	truncateRight bool
}

// renderTable writes an aligned, ANSI-aware table to w. Headers are muted;
// per-column alignment (left/right) is honored. Cell widths are measured via
// ui.PrintableWidth so colored cells and emoji align correctly.
//
// maxWidth, when > 0, caps the total visible width of the table. If the
// natural column widths exceed maxWidth, the widest column is shrunk
// repeatedly until the row fits, with cells in shrunk columns truncated with
// a trailing "…". Headers are never truncated below their own width — if
// the headers themselves don't fit, the table renders at its minimum width
// regardless. 0 disables the cap (unconstrained, like tests/pipes).
func renderTable(w io.Writer, cols []tableCol, rows [][]string, maxWidth int) {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = ui.PrintableWidth(c.header)
	}
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], ui.PrintableWidth(cell))
		}
	}

	const sep = "  "
	if maxWidth > 0 {
		shrinkWidthsToFit(widths, cols, maxWidth, len(sep))
	}

	last := len(cols) - 1
	// padIfNotLast skips trailing-whitespace padding on the rightmost column
	// when it's left-aligned. Right-aligned last columns (numeric) still need
	// their leading spaces to align under the header.
	padIfNotLast := func(cell string, i int, right bool) string {
		if i == last && !right {
			return cell
		}
		return padCell(cell, widths[i], right)
	}

	headerCells := make([]string, len(cols))
	for i, c := range cols {
		headerCells[i] = ui.Muted(padIfNotLast(c.header, i, c.right))
	}
	_, _ = fmt.Fprintln(w, strings.Join(headerCells, sep))

	for _, row := range rows {
		cells := make([]string, len(cols))
		for i, cell := range row {
			cells[i] = padIfNotLast(truncateCell(cell, widths[i], cols[i].truncateRight), i, cols[i].right)
		}
		_, _ = fmt.Fprintln(w, strings.Join(cells, sep))
	}
}

// shrinkWidthsToFit reduces column widths so the rendered row fits in
// maxWidth. Repeatedly trims the widest column (down to its header's own
// width) until the budget is satisfied or no column can shrink further.
func shrinkWidthsToFit(widths []int, cols []tableCol, maxWidth, sepWidth int) {
	const minPerCol = 4 // small floor so a shrunk column still has room for a glyph + ellipsis
	totalSep := sepWidth * (len(widths) - 1)
	for {
		total := totalSep
		for _, w := range widths {
			total += w
		}
		excess := total - maxWidth
		if excess <= 0 {
			return
		}
		widest, widestIdx := -1, -1
		for i, w := range widths {
			floor := max(minPerCol, ui.PrintableWidth(cols[i].header))
			if w > floor && w > widest {
				widest = w
				widestIdx = i
			}
		}
		if widestIdx < 0 {
			return // no column can shrink further
		}
		floor := max(minPerCol, ui.PrintableWidth(cols[widestIdx].header))
		widths[widestIdx] = max(floor, widths[widestIdx]-excess)
	}
}

// truncateCell shortens cell to fit width visible columns. When truncateRight
// is true, drops trailing runes and appends "…". Otherwise drops from the
// middle ("aws_appauto…dynamodb_read"), preserving both ends — better for
// identifier-shaped values where the suffix is the distinguishing part.
// Cells already within width are returned unchanged.
//
// Cells containing ANSI escapes aren't truncated (preserving the escapes
// while measuring visible width is more complex than we need; status pills
// and the like are short anyway).
func truncateCell(cell string, width int, truncateRight bool) string {
	visible := ui.PrintableWidth(cell)
	if visible <= width {
		return cell
	}
	if width <= 1 {
		return "…"
	}
	if strings.ContainsRune(cell, 0x1b) {
		return cell
	}
	runes := []rune(cell)
	if truncateRight {
		for len(runes) > 0 && ui.PrintableWidth(string(runes))+1 > width {
			runes = runes[:len(runes)-1]
		}
		return string(runes) + "…"
	}

	// Middle truncation: keep leftHalf runes from the start, "…" in the
	// middle, rightHalf runes from the end. Ellipsis takes 1 cell. We bias
	// the left half to keep the prefix slightly longer when the budget is odd.
	available := width - 1
	leftHalf := (available + 1) / 2
	rightHalf := available - leftHalf
	if leftHalf+rightHalf >= len(runes) {
		// Nothing meaningful to drop — fall back to right-truncation.
		return string(runes[:width-1]) + "…"
	}
	return string(runes[:leftHalf]) + "…" + string(runes[len(runes)-rightHalf:])
}

func padCell(cell string, width int, right bool) string {
	gap := max(0, width-ui.PrintableWidth(cell))
	if right {
		return strings.Repeat(" ", gap) + cell
	}
	return cell + strings.Repeat(" ", gap)
}

// kvRow is a label/value pair for writeKV. Empty rows render as a blank line.
type kvRow struct {
	label, value string
}

// writeWrapped writes content to w, prefixing each line with indent. When
// maxWidth > 0, the content is soft-wrapped to fit (maxWidth − len(indent))
// visible columns; pre-existing newlines in content are preserved.
//
// Use this for any block-layout line that might be wider than the box —
// resource headers with long addresses, "Missing: tag1, tag2, …" detail
// lines, etc. Unconstrained (maxWidth = 0) prints content as-is, which is
// what tests get since stdout there isn't a TTY.
func writeWrapped(w io.Writer, indent, content string, maxWidth int) {
	if maxWidth <= 0 {
		_, _ = fmt.Fprintln(w, indent+content)
		return
	}
	budget := maxWidth - ui.PrintableWidth(indent)
	if budget <= 0 {
		budget = maxWidth
	}
	for line := range strings.SplitSeq(ui.WrapText(content, budget), "\n") {
		_, _ = fmt.Fprintln(w, indent+line)
	}
}

// writeKV writes label/value rows with the labels right-padded so the values
// line up at a single column. Labels render in accent color with a trailing
// colon; the gap between colon and value is 2 spaces.
func writeKV(w io.Writer, rows []kvRow) {
	maxLabel := 0
	for _, r := range rows {
		maxLabel = max(maxLabel, len(r.label))
	}
	for _, r := range rows {
		if r.label == "" && r.value == "" {
			_, _ = fmt.Fprintln(w)
			continue
		}
		gap := strings.Repeat(" ", maxLabel-len(r.label))
		_, _ = fmt.Fprintf(w, "%s:%s  %s\n", ui.Accent(r.label), gap, r.value)
	}
}
