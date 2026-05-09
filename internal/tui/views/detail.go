package views

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
)

// Detail renders a per-resource cost breakdown in the right pane: failing
// policies (when present), total cost, cost components, sub-resources,
// tags, and the source file:line. Lays out against its assigned width so
// it reflows on terminal resize.
type Detail struct {
	resource *format.ResourceOutput
	issues   ResourceIssues
	currency string

	vp     viewport.Model
	width  int
	height int
}

// NewDetail returns an empty detail pane.
func NewDetail() Detail { return Detail{vp: viewport.New(0, 0)} }

// SetSize updates pane dimensions; call on tea.WindowSizeMsg.
func (d *Detail) SetSize(width, height int) {
	d.width = width
	d.height = height
	d.vp.Width = width
	d.vp.Height = height
	d.refresh()
}

// SetRow swaps in a row and re-renders. Pass nil to clear. The detail
// pane reads cost data from the underlying ResourceOutput and policy
// failures from row.Issues — both come pre-computed via RowsFromOutput
// so the pane never has to walk the project's policy results itself.
func (d *Detail) SetRow(row *ResourceRow, currency string) {
	if row == nil {
		d.resource = nil
		d.issues = ResourceIssues{}
	} else {
		d.resource = row.Resource
		d.issues = row.Issues
	}
	d.currency = currency
	d.refresh()
}

// Update forwards messages to the embedded viewport (so PgUp/PgDown
// scroll the detail pane independently of the list).
func (d *Detail) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.vp, cmd = d.vp.Update(msg)
	return cmd
}

// View renders the detail pane.
func (d Detail) View() string { return d.vp.View() }

// refresh re-renders the detail content into the viewport.
func (d *Detail) refresh() {
	if d.width <= 0 {
		return
	}
	if d.resource == nil {
		d.vp.SetContent(styles.Muted().Render("  Select a resource to see its cost breakdown."))
		return
	}

	var b strings.Builder
	b.WriteString(styles.Bold().Render(d.resource.Name))
	b.WriteByte('\n')
	b.WriteString(styles.Muted().Render(d.resource.Type))
	if loc := inspect.FormatFileLoc(d.resource.Metadata.Filename, d.resource.Metadata.StartLine); loc != "" {
		b.WriteString("  ")
		b.WriteString(styles.Muted().Render(loc))
	}
	b.WriteString("\n\n")

	total := inspect.ResourceCost(d.resource)
	b.WriteString(styles.Bold().Render("Total"))
	b.WriteString("  ")
	b.WriteString(styles.Cost().Render(inspect.HumanMoney(total, d.currency) + "/mo"))
	b.WriteString("\n\n")

	if d.issues.Any() {
		b.WriteString(styles.Bold().Render("Issues"))
		b.WriteByte('\n')
		writeIssues(&b, d.issues, d.currency, d.width)
		b.WriteByte('\n')
	}

	if len(d.resource.CostComponents) > 0 {
		b.WriteString(styles.Bold().Render("Cost components"))
		b.WriteByte('\n')
		writeCostComponents(&b, d.resource.CostComponents, d.currency, d.width)
		b.WriteByte('\n')
	}

	if len(d.resource.Subresources) > 0 {
		b.WriteString(styles.Bold().Render("Sub-resources"))
		b.WriteByte('\n')
		for i := range d.resource.Subresources {
			sub := &d.resource.Subresources[i]
			subTotal := inspect.ResourceCost(sub)
			subTotalStr := inspect.HumanMoney(subTotal, d.currency) + "/mo"

			// Reserve space for the leading "  " indent (2), the gap
			// before the cost (2), and the cost cell itself, then
			// middle-truncate the name so the cost never gets pushed
			// off the right edge of the pane.
			costColWidth := ui.PrintableWidth(subTotalStr)
			nameColWidth := d.width - 2 - 2 - costColWidth
			if nameColWidth < 12 {
				nameColWidth = 12
			}
			name := inspect.TruncateMiddle(sub.Name, nameColWidth)
			fmt.Fprintf(&b, "  %s  %s\n",
				padRight(name, nameColWidth),
				styles.Cost().Render(subTotalStr),
			)
			if len(sub.CostComponents) > 0 {
				// Tree-glyph rendering makes the parent/child relationship
				// explicit — same idiom most terminal directory listings
				// use (`├─` for non-last, `└─` for last). Components stay
				// indented one extra level under the sub-resource line.
				writeCostComponentsTree(&b, sub.CostComponents, d.currency, d.width)
			}
		}
		b.WriteByte('\n')
	}

	if len(d.resource.Tags) > 0 {
		b.WriteString(styles.Bold().Render("Tags"))
		b.WriteByte('\n')
		keys := make([]string, 0, len(d.resource.Tags))
		for k := range d.resource.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s = %s\n",
				styles.Muted().Render(k),
				d.resource.Tags[k],
			)
		}
		b.WriteByte('\n')
	}

	d.vp.SetContent(strings.TrimRight(b.String(), "\n"))
}

// writeIssues renders the failing-policy block. Each policy gets a
// header line (kind + name), an optional message, and per-issue detail
// (FinOps savings; tagging missing/invalid tags). Lines are soft-wrapped
// to fit within width via ui.WrapText, which respects leading whitespace
// so continuation lines stay indented under the same bullet.
func writeIssues(b *strings.Builder, issues ResourceIssues, currency string, width int) {
	writeWrapped := func(line string) {
		b.WriteString(ui.WrapText(line, width))
		b.WriteByte('\n')
	}
	for _, p := range issues.Finops {
		writeWrapped(fmt.Sprintf("  %s %s",
			styles.Danger().Render(issueIcon() + "FinOps:"),
			styles.Bold().Render(p.PolicyName),
		))
		if p.PolicyMessage != "" {
			writeWrapped("    " + styles.Muted().Render(p.PolicyMessage))
		}
		for _, iss := range p.Issues {
			line := "    • " + iss.Description
			if iss.MonthlySavings != nil && !iss.MonthlySavings.IsZero() {
				line += " " + styles.Muted().Render(
					fmt.Sprintf("— save %s/mo", inspect.HumanMoney(iss.MonthlySavings, currency)),
				)
			}
			writeWrapped(line)
		}
	}
	for _, p := range issues.Tagging {
		writeWrapped(fmt.Sprintf("  %s %s",
			styles.Danger().Render(issueIcon()+"Tagging:"),
			styles.Bold().Render(p.PolicyName),
		))
		if p.PolicyMessage != "" {
			writeWrapped("    " + styles.Muted().Render(p.PolicyMessage))
		}
		if len(p.MissingMandatory) > 0 {
			writeWrapped(fmt.Sprintf("    %s %s",
				styles.Muted().Render("Missing tags:"),
				strings.Join(p.MissingMandatory, ", "),
			))
		}
		for _, t := range p.InvalidTags {
			line := fmt.Sprintf("    • %s = %q", t.Key, t.Value)
			if t.Suggestion != "" {
				line += " " + styles.Muted().Render("→ "+t.Suggestion)
			}
			writeWrapped(line)
		}
	}
}

// writeCostComponents emits one indented line per component with the
// monthly total right-aligned to the pane edge.
//
// Truncation runs against the *plain* component name + unit (no inline
// styling) so we don't risk slicing through an ANSI escape sequence.
// When a unit is present we apply the muted style to the whole cell
// after truncation; that loses the prefix/suffix contrast the previous
// implementation tried for, but avoids broken escape codes (which
// terminals render as the U+FFFD replacement character).
func writeCostComponents(b *strings.Builder, comps []format.CostComponentOutput, currency string, width int) {
	writeCostComponentsIndented(b, comps, currency, width, "  ")
}

// writeCostComponentsTree emits one indented line per component with tree
// glyphs ("├─" / "└─") at the start to communicate the sub-resource
// hierarchy at a glance — same convention used by `tree`(1) and most
// directory listings. Glyphs render in the muted color so they frame the
// content without competing with it.
func writeCostComponentsTree(b *strings.Builder, comps []format.CostComponentOutput, currency string, width int) {
	const baseIndent = "  "
	const branchW = 3 // "├─ " / "└─ " each measure 3 cells
	costColWidth := 14
	nameColWidth := width - costColWidth - ui.PrintableWidth(baseIndent) - branchW - 2
	if nameColWidth < 12 {
		nameColWidth = 12
	}
	for i, c := range comps {
		glyph := "├─ "
		if i == len(comps)-1 {
			glyph = "└─ "
		}
		plain := c.Name
		if c.Unit != "" {
			plain += " (" + c.Unit + ")"
		}
		truncated := inspect.TruncateEnd(plain, nameColWidth)
		cost := inspect.HumanMoney(c.TotalMonthlyCost, currency) + "/mo"
		fmt.Fprintf(b, "%s%s%s  %s\n",
			baseIndent,
			styles.Muted().Render(glyph),
			padRight(truncated, nameColWidth),
			padLeft(cost, costColWidth),
		)
	}
}

// writeCostComponentsIndented is the indent-aware variant used by the
// top-level cost-components block when it nests beneath another section.
// Sub-resources use writeCostComponentsTree instead.
func writeCostComponentsIndented(b *strings.Builder, comps []format.CostComponentOutput, currency string, width int, indent string) {
	costColWidth := 14
	nameColWidth := width - costColWidth - ui.PrintableWidth(indent) - 2
	if nameColWidth < 12 {
		nameColWidth = 12
	}
	for _, c := range comps {
		plain := c.Name
		if c.Unit != "" {
			plain += " (" + c.Unit + ")"
		}
		truncated := inspect.TruncateEnd(plain, nameColWidth)
		cost := inspect.HumanMoney(c.TotalMonthlyCost, currency) + "/mo"
		fmt.Fprintf(b, "%s%s  %s\n",
			indent,
			padRight(truncated, nameColWidth),
			padLeft(cost, costColWidth),
		)
	}
}
