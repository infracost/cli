package inspect

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
)

type projectSummary struct {
	Name                   string   `json:"name"`
	Path                   string   `json:"path"`
	Resources              int      `json:"resources"`
	MonthlyCost            *rat.Rat `json:"monthly_cost"`
	FinopsPolicies         int      `json:"finops_policies"`
	FinopsFailingPolicies  int      `json:"finops_failing_policies"`
	TaggingPolicies        int      `json:"tagging_policies"`
	TaggingFailingPolicies int      `json:"tagging_failing_policies"`
	HasErrors              bool     `json:"has_errors"`
}

type summaryData struct {
	Projects               int              `json:"projects"`
	ProjectsWithError      int              `json:"projects_with_errors"`
	ProjectDetails         []projectSummary `json:"project_details"`
	Resources              int              `json:"resources"`
	CostedResources        int              `json:"costed_resources"`
	FreeResources          int              `json:"free_resources"`
	MonthlyCost            *rat.Rat         `json:"monthly_cost"`
	FinopsPolicies         int              `json:"finops_policies"`
	FailingPolicies        int              `json:"failing_policies"`
	TaggingPolicies        int              `json:"tagging_policies"`
	FailingTaggingPolicies int              `json:"failing_tagging_policies"`
	Guardrails             int              `json:"guardrails"`
	TriggeredGuardrails    int              `json:"triggered_guardrails"`
	Budgets                int              `json:"budgets"`
	OverBudget             int              `json:"over_budget"`
	CriticalDiags          int              `json:"critical_diagnostics"`
	WarningDiags           int              `json:"warning_diagnostics"`

	// Detail lists for `inspect --json` consumers (LLMs, scripts) that need
	// to act on the failures. The aggregate counts above stay as-is so
	// existing consumers keep working.
	FailingPolicyList     []failingPolicyEntry      `json:"failing_policy_list,omitempty"`
	TriggeredGuardrailList []format.GuardrailOutput `json:"triggered_guardrail_list,omitempty"`
	OverBudgetList        []format.BudgetOutput     `json:"over_budget_list,omitempty"`
}

// failingPolicyEntry is one failing policy + its failing resources, used in
// the enriched summary JSON. Per-resource detail (issues / missing+invalid
// tags) lives at the resource level so downstream consumers don't need a
// separate drill-in call.
type failingPolicyEntry struct {
	Kind           string                                `json:"kind"`
	Name           string                                `json:"name"`
	Slug           string                                `json:"slug,omitempty"`
	Message        string                                `json:"message,omitempty"`
	Project        string                                `json:"project"`
	// TagSchema is the policy's per-key tag schema (allowed values, regex,
	// mandatory flag), present only for tagging entries. Carried here so the
	// summary's failing list is self-contained — consumers don't need to
	// drill back into the per-project TaggingResults to look up valid values.
	TagSchema      []format.TagSchemaEntry               `json:"tag_schema,omitempty"`
	FailingFinops  []format.FinopsFailingResourceOutput  `json:"failing_finops,omitempty"`
	FailingTagging []format.FailingTaggingResourceOutput `json:"failing_tagging,omitempty"`
}

func ResourceCost(r *format.ResourceOutput) *rat.Rat {
	total := rat.Zero
	for _, cc := range r.CostComponents {
		if cc.TotalMonthlyCost != nil {
			total = total.Add(cc.TotalMonthlyCost)
		}
	}
	for _, sub := range r.Subresources {
		total = total.Add(ResourceCost(&sub))
	}
	return total
}

func WriteSummary(w io.Writer, data *format.Output, opts Options) error {
	s := buildSummary(data)

	if opts.Structured() {
		return writeStructured(w, s, opts)
	}

	var inner bytes.Buffer
	fmt.Fprintln(&inner, ui.Bold("Scan Summary"))
	fmt.Fprintln(&inner)

	rows := []kvRow{}
	if s.Projects > 1 {
		v := humanInt(s.Projects)
		if s.ProjectsWithError > 0 {
			v += " " + ui.Danger(critMark(s.ProjectsWithError))
		}
		rows = append(rows, kvRow{"Projects", v})
	}
	resourceVal := humanInt(s.Resources)
	if s.CostedResources > 0 || s.FreeResources > 0 {
		resourceVal += ui.Muted(fmt.Sprintf(" (%s costed, %s free)", humanInt(s.CostedResources), humanInt(s.FreeResources)))
	}
	rows = append(rows,
		kvRow{"Resources", resourceVal},
		kvRow{"Monthly cost", humanDollar(s.MonthlyCost)},
		kvRow{},
		kvRow{"FinOps", flagCount(s.FinopsPolicies, s.FailingPolicies, warnEmoji)},
		kvRow{"Tagging", flagCount(s.TaggingPolicies, s.FailingTaggingPolicies, warnEmoji)},
		kvRow{"Guardrails", flagCount(s.Guardrails, s.TriggeredGuardrails, stopEmoji)},
		kvRow{"Budgets", flagCount(s.Budgets, s.OverBudget, moneyEmoji)},
	)
	if s.CriticalDiags > 0 || s.WarningDiags > 0 {
		rows = append(rows, kvRow{"Diagnostics", diagnosticsValue(s.CriticalDiags, s.WarningDiags)})
	}
	writeKV(&inner, rows)

	usesWarn := s.FailingPolicies > 0 || s.FailingTaggingPolicies > 0
	usesStop := s.TriggeredGuardrails > 0
	usesMoney := s.OverBudget > 0
	usesCrit := s.CriticalDiags > 0

	if s.Projects > 1 {
		fmt.Fprintln(&inner)
		writeProjectTable(&inner, s.ProjectDetails)
	}

	if usesWarn || usesStop || usesMoney || usesCrit {
		fmt.Fprintln(&inner)
		if usesWarn {
			fmt.Fprintln(&inner, ui.Muted(warnEmoji+"  = failing policy"))
		}
		if usesStop {
			fmt.Fprintln(&inner, ui.Muted(stopEmoji+"  = triggered guardrail"))
		}
		if usesMoney {
			fmt.Fprintln(&inner, ui.Muted(moneyEmoji+"  = over budget"))
		}
		if usesCrit {
			fmt.Fprintln(&inner, ui.Muted(critEmoji+"  = scan error; results for this project may be incomplete"))
		}
	}

	_, err := fmt.Fprint(w, ui.Box(inner.String()))
	return err
}

// flagCount renders "<total>" when nothing is flagged, otherwise
// "<total> (<symbol> xN)" with the parenthetical highlighted. Caller passes
// the symbol so each row can use its own (⚠️ failing, 🛑 triggered, 💸 over).
func flagCount(total, flagged int, symbol string) string {
	if flagged == 0 {
		return humanInt(total)
	}
	return fmt.Sprintf("%s %s", humanInt(total), ui.Caution(flagMark(flagged, symbol)))
}

func flagMark(n int, symbol string) string {
	return fmt.Sprintf("(%s x%s)", symbol, humanInt(n))
}

func critMark(n int) string {
	return fmt.Sprintf("(%s x%s)", critEmoji, humanInt(n))
}

// diagnosticsValue formats the Diagnostics row. There's no overall total to
// anchor against — the value is just severity counts. Critical uses the bare
// "❗ xN" form (no parens) so it doesn't read as a parenthetical orphan.
func diagnosticsValue(critical, warning int) string {
	parts := []string{}
	if critical > 0 {
		parts = append(parts, ui.Danger(fmt.Sprintf("%s x%s", critEmoji, humanInt(critical))))
	}
	if warning > 0 {
		parts = append(parts, ui.Caution(fmt.Sprintf("%s warning", humanInt(warning))))
	}
	return strings.Join(parts, ", ")
}

// writeProjectTable renders the per-project breakdown using an ANSI-aware,
// per-column-aligned renderer (text/tabwriter measures by raw byte count and
// can't handle colored cells correctly).
func writeProjectTable(w io.Writer, projects []projectSummary) {
	cols := []tableCol{
		{header: "Project", right: false},
		{header: "Resources", right: true},
		{header: "Monthly Cost", right: true},
		{header: "FinOps", right: false},
		{header: "Tagging", right: false},
	}
	rows := make([][]string, 0, len(projects))
	for _, ps := range projects {
		name := ps.Name
		if ps.HasErrors {
			name += " " + ui.Danger(critEmoji)
		}
		rows = append(rows, []string{
			name,
			humanInt(ps.Resources),
			humanDollar(ps.MonthlyCost),
			flagCount(ps.FinopsPolicies, ps.FinopsFailingPolicies, warnEmoji),
			flagCount(ps.TaggingPolicies, ps.TaggingFailingPolicies, warnEmoji),
		})
	}
	renderTable(w, cols, rows, ui.ContentWidth())
}

func buildSummary(data *format.Output) summaryData {
	s := summaryData{MonthlyCost: rat.Zero}

	for _, p := range data.Projects {
		s.Projects++
		ps := projectSummary{
			Name:        p.ProjectName,
			Path:        p.Path,
			MonthlyCost: rat.Zero,
		}

		if len(p.Diagnostics) > 0 {
			hasCritical := false
			for _, d := range p.Diagnostics {
				switch d.Severity {
				case "critical":
					hasCritical = true
					s.CriticalDiags++
				case "warning":
					s.WarningDiags++
				}
			}
			if hasCritical {
				s.ProjectsWithError++
				ps.HasErrors = true
			}
		}

		for _, r := range p.Resources {
			s.Resources++
			ps.Resources++
			if r.IsFree {
				s.FreeResources++
			} else {
				s.CostedResources++
			}
			cost := ResourceCost(&r)
			s.MonthlyCost = s.MonthlyCost.Add(cost)
			ps.MonthlyCost = ps.MonthlyCost.Add(cost)
		}

		for _, f := range p.FinopsResults {
			s.FinopsPolicies++
			ps.FinopsPolicies++
			if len(f.FailingResources) > 0 {
				s.FailingPolicies++
				ps.FinopsFailingPolicies++
				s.FailingPolicyList = append(s.FailingPolicyList, failingPolicyEntry{
					Kind:          "finops",
					Name:          f.PolicyName,
					Slug:          f.PolicySlug,
					Message:       f.PolicyMessage,
					Project:       p.ProjectName,
					FailingFinops: f.FailingResources,
				})
			}
		}

		for _, t := range p.TaggingResults {
			s.TaggingPolicies++
			ps.TaggingPolicies++
			if len(t.FailingResources) > 0 {
				s.FailingTaggingPolicies++
				ps.TaggingFailingPolicies++
				s.FailingPolicyList = append(s.FailingPolicyList, failingPolicyEntry{
					Kind:           "tagging",
					Name:           t.PolicyName,
					Message:        t.Message,
					Project:        p.ProjectName,
					TagSchema:      t.TagSchema,
					FailingTagging: t.FailingResources,
				})
			}
		}

		s.ProjectDetails = append(s.ProjectDetails, ps)
	}

	for _, gr := range data.GuardrailResults {
		s.Guardrails++
		if gr.Triggered {
			s.TriggeredGuardrails++
			s.TriggeredGuardrailList = append(s.TriggeredGuardrailList, gr)
		}
	}

	for _, br := range data.BudgetResults {
		s.Budgets++
		if br.OverBudget {
			s.OverBudget++
			s.OverBudgetList = append(s.OverBudgetList, br)
		}
	}

	return s
}
