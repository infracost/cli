package inspect

import (
	"fmt"
	"io"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
)

// WriteFailing renders the unified "what needs attention" panorama for
// `inspect --failing`. Three sections:
//
//	Failing policies — one block per failing policy×resource pairing,
//	                   reusing the writePolicyGroupRows layout.
//	Triggered guardrails
//	Over budget
//
// When at least one category has issues, all three section headers render
// (empty ones show "✓ None.") so the view is visibly a panorama, not a
// disguised single-section list. When everything's clear, a single positive
// line prints.
//
// `data` has already been through Filter, so passing policies are dropped
// from FinopsResults / TaggingResults.
func WriteFailing(w io.Writer, data *format.Output, opts Options) error {
	if opts.Structured() {
		return writeStructured(w, failingPanoramaJSONFor(data), opts)
	}
	policyRows := collectPolicyRows(data)
	distinctPolicies := countDistinctPolicies(policyRows)

	var triggered []format.GuardrailOutput
	for _, gr := range data.GuardrailResults {
		if gr.Triggered {
			triggered = append(triggered, gr)
		}
	}

	var over []format.BudgetOutput
	for _, br := range data.BudgetResults {
		if br.OverBudget {
			over = append(over, br)
		}
	}

	if len(policyRows) == 0 && len(triggered) == 0 && len(over) == 0 {
		_, _ = fmt.Fprintln(w, ui.Positive("✓ Nothing failing."))
		return nil
	}

	// Section 1: failing policies — per-pairing blocks. Section header shows
	// both numbers so the user can tell how concentrated the failures are.
	policyCount := fmt.Sprintf("(%d %s · %d %s)",
		distinctPolicies, pluralize("policy", distinctPolicies),
		len(policyRows), pluralize("resource", len(policyRows)),
	)
	writeSectionHeading(w, "Failing policies", policyCount)
	if len(policyRows) == 0 {
		_, _ = fmt.Fprintln(w, ui.Positive("✓ No failing policies."))
	} else {
		writePolicyGroupRows(w, policyRows, []string{string(GroupByPolicy)})
	}

	// Section 2: triggered guardrails
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w)
	writeSectionHeading(w, "Triggered guardrails", fmt.Sprintf("(%d)", len(triggered)))
	if len(triggered) == 0 {
		_, _ = fmt.Fprintln(w, ui.Positive("✓ None triggered."))
	} else {
		for i, gr := range triggered {
			if i > 0 {
				_, _ = fmt.Fprintln(w)
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", stopEmoji, ui.Bold(gr.GuardrailName))
			if gr.TotalMonthlyCost != nil {
				_, _ = fmt.Fprintf(w, "   %s\n", ui.Muted(humanMoney(gr.TotalMonthlyCost, data.Currency)+"/mo"))
			}
		}
	}

	// Section 3: over budget
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w)
	writeSectionHeading(w, "Over budget", fmt.Sprintf("(%d)", len(over)))
	if len(over) == 0 {
		_, _ = fmt.Fprintln(w, ui.Positive("✓ Nothing over budget."))
	} else {
		for i, br := range over {
			if i > 0 {
				_, _ = fmt.Fprintln(w)
			}
			_, _ = fmt.Fprintf(w, "%s  %s\n", moneyEmoji, ui.Bold(br.BudgetName))
			overBy := br.CurrentCost.Sub(br.Amount)
			_, _ = fmt.Fprintf(w, "   %s\n", ui.Muted(fmt.Sprintf(
				"Over by %s (%s actual / %s limit)",
				humanMoney(overBy, data.Currency),
				humanMoney(br.CurrentCost, data.Currency),
				humanMoney(br.Amount, data.Currency),
			)))
			if len(br.Tags) > 0 {
				_, _ = fmt.Fprintf(w, "   %s %s\n", ui.Accent("Scope:"), formatBudgetTagScope(br.Tags))
			}
			if br.CustomOverrunMessage != "" {
				_, _ = fmt.Fprintf(w, "   %s\n", ui.Muted(br.CustomOverrunMessage))
			}
		}
	}

	return nil
}

func writeSectionHeading(w io.Writer, label, count string) {
	_, _ = fmt.Fprintf(w, "%s  %s\n", ui.Bold(label), ui.Muted(count))
	_, _ = fmt.Fprintln(w)
}

// countDistinctPolicies counts unique policies across rows. Used for the
// panorama heading "(N policies · M resources)".
func countDistinctPolicies(rows []tableRow) int {
	seen := map[string]struct{}{}
	for _, r := range rows {
		key := r.Columns["kind"] + "\x00" + r.Columns[string(GroupByPolicy)]
		seen[key] = struct{}{}
	}
	return len(seen)
}
