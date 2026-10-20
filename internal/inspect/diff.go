package inspect

import (
	"fmt"
	"io"
	"strings"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
)

const diffDivider = "──────────────────────────────────"

var diffKey = fmt.Sprintf("Key: %s changed, %s added, %s removed",
	opChar(format.DiffChanged), opChar(format.DiffAdded), opChar(format.DiffRemoved))

// WriteDiff renders a cost diff as the v1 CLI did: a per-resource tree of
// changes, then a per-project summary.
func WriteDiff(w io.Writer, d *format.DiffOutput) error {
	var b strings.Builder

	if d.HasChanges() {
		b.WriteString(diffKey + "\n\n")
	}
	b.WriteString(diffDivider + "\n")

	for _, p := range d.Projects {
		if len(p.Resources) == 0 {
			continue
		}

		fmt.Fprintf(&b, "%s %s\n\n", ui.Bold("Project:"), p.ProjectName)

		for _, r := range p.Resources {
			b.WriteString(diffResource(d.Currency, r, true))
			b.WriteString("\n")
		}

		fmt.Fprintf(&b, "%s %s\nAmount:  %s %s\n",
			ui.Bold("Monthly cost change for"),
			ui.Bold(p.ProjectName),
			costChange(d.Currency, p.DiffTotalMonthlyCost),
			ui.Dim(fmt.Sprintf("(%s → %s)",
				humanMoney(p.PastTotalMonthlyCost, d.Currency),
				humanMoney(p.TotalMonthlyCost, d.Currency))),
		)
		if pct := percentChange(p.PastTotalMonthlyCost, p.TotalMonthlyCost); pct != "" {
			fmt.Fprintf(&b, "Percent: %s\n", pct)
		}

		b.WriteString("\n" + diffDivider + "\n")
	}

	if !d.HasChanges() {
		b.WriteString("No cost estimate changes.\n")
		b.WriteString(diffDivider + "\n")
		_, err := io.WriteString(w, b.String())
		return err
	}

	b.WriteString(diffKey + "\n")

	if unchanged := d.UnchangedProjects(); len(unchanged) > 0 {
		word := "projects"
		if len(unchanged) == 1 {
			word = "project"
		}
		fmt.Fprintf(&b, "\n%d %s with no cost estimate changes: %s\n",
			len(unchanged), word, strings.Join(unchanged, ", "))
	}

	fmt.Fprintf(&b, "\nInfracost estimate: %s\n\n", changeSentence(d))
	writeDiffTable(&b, d)

	_, err := io.WriteString(w, b.String())
	return err
}

// diffResource renders one resource and recurses. Only the top level prints
// the resource's own total.
func diffResource(currency string, r format.DiffResource, topLevel bool) string {
	var b strings.Builder

	name := r.Name
	if topLevel {
		name = ui.Bold(name)
	}
	fmt.Fprintf(&b, "%s %s\n", opChar(r.Op), name)

	if topLevel && r.CostDependsOnUsage {
		b.WriteString("  Monthly cost depends on usage\n")
	} else if topLevel {
		fmt.Fprintf(&b, "  %s %s\n",
			costChange(currency, r.DiffMonthlyCost),
			ui.Dim(fmt.Sprintf("(%s → %s)",
				humanMoney(r.PastMonthlyCost, currency),
				humanMoney(r.MonthlyCost, currency))))
	}

	for _, c := range r.CostComponents {
		b.WriteString("\n")
		b.WriteString(indent(diffCostComponent(currency, c), "    "))
	}

	for _, sub := range r.Subresources {
		b.WriteString("\n")
		b.WriteString(indent(diffResource(currency, sub, false), "    "))
	}

	return b.String()
}

func diffCostComponent(currency string, c format.DiffCostComponent) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s %s\n", opChar(c.Op), c.Name)

	// Both sides unpriced: the component is usage-driven, so show unit price.
	if c.CostDependsOnUsage {
		b.WriteString("  Monthly cost depends on usage\n")
		fmt.Fprintf(&b, "    %s per %s\n", priceChange(currency, c.DiffPrice), c.Unit)
		return b.String()
	}

	fmt.Fprintf(&b, "  %s %s\n",
		costChange(currency, c.DiffMonthlyCost),
		ui.Dim(fmt.Sprintf("(%s → %s)",
			humanMoney(c.PastMonthlyCost, currency),
			humanMoney(c.MonthlyCost, currency))))

	return b.String()
}

func writeDiffTable(w io.Writer, d *format.DiffOutput) {
	cols := []tableCol{
		{header: "Changed project"},
		{header: "Before", right: true},
		{header: "After", right: true},
		{header: "Change", right: true},
	}

	var rows [][]string
	for _, p := range d.Projects {
		if len(p.Resources) == 0 {
			continue
		}
		rows = append(rows, []string{
			p.ProjectName,
			humanMoney(p.PastTotalMonthlyCost, d.Currency),
			humanMoney(p.TotalMonthlyCost, d.Currency),
			costChange(d.Currency, p.DiffTotalMonthlyCost),
		})
	}

	renderTable(w, cols, rows, ui.ContentWidth())
}

// opChar is the leading marker for a diff node, matching the v1 key line.
func opChar(op format.DiffOp) string {
	switch op {
	case format.DiffAdded:
		return ui.Positive("+")
	case format.DiffRemoved:
		return ui.Danger("-")
	default:
		return ui.Caution("~")
	}
}

// costChange formats a delta with an explicit sign, to distinguish it from an
// absolute cost.
func costChange(currency string, d *rat.Rat) string {
	if d == nil || d.IsZero() {
		return humanMoney(rat.Zero, currency)
	}
	if d.GreaterThanZero() {
		return ui.Danger("+" + humanMoney(d, currency))
	}
	return ui.Positive("-" + humanMoney(d.Abs(), currency))
}

// priceChange keeps small unit prices legible: humanMoney drops cents, turning
// $0.0000166667 per GB-second into $0.
func priceChange(currency string, d *rat.Rat) string {
	if d == nil || d.IsZero() {
		return currencySymbol(currency) + "0"
	}

	s := strings.TrimRight(d.Abs().StringFixed(12), "0")
	if whole, frac, ok := strings.Cut(s, "."); ok && len(frac) < 2 {
		s = whole + "." + (frac + "00")[:2]
	}

	if d.GreaterThanZero() {
		return ui.Danger("+" + currencySymbol(currency) + s)
	}
	return ui.Positive("-" + currencySymbol(currency) + s)
}

func percentChange(past, current *rat.Rat) string {
	if past == nil || past.IsZero() || current == nil {
		return ""
	}
	pct := current.Sub(past).Div(past).Mul(rat.New(100))
	if pct.IsZero() {
		return ""
	}
	sign := ""
	if pct.GreaterThanZero() {
		sign = "+"
	}
	return sign + pct.StringFixed(0) + "%"
}

func changeSentence(d *format.DiffOutput) string {
	totals := ui.Dim(fmt.Sprintf("(%s → %s)",
		humanMoney(d.PastTotalMonthlyCost, d.Currency),
		humanMoney(d.TotalMonthlyCost, d.Currency)))

	// Totals can cancel out while resources changed, so a zero net change
	// still needs a summary line.
	if d.DiffTotalMonthlyCost.IsZero() {
		return fmt.Sprintf("Monthly estimate unchanged %s", totals)
	}

	direction := "increased"
	if d.DiffTotalMonthlyCost.LessThan(rat.Zero) {
		direction = "decreased"
	}
	return fmt.Sprintf("Monthly estimate %s by %s %s",
		direction,
		humanMoney(d.DiffTotalMonthlyCost.Abs(), d.Currency),
		totals)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
