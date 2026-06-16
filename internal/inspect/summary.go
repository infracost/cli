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

// ProjectSummary is the per-project row of [SummaryView.ProjectDetails],
// matching the multi-project table rendered beneath the scan/price summary
// box. Returned as part of the typed summary so MCP tool callers can read it
// without parsing the rendered table.
type ProjectSummary struct {
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

// SummaryView is the typed shape of the scan/price summary view — the same
// data the human renderer prints into the "Scan Summary" box (headline
// counts, monthly cost, policy/guardrail/budget tallies, diagnostic counts,
// and the per-project breakdown). Shared between the inspect summary
// renderer and the MCP scan tool so both surfaces show the same numbers.
//
// Drill-in detail (which specific policies failed, which guardrails
// triggered, which budgets are over) is intentionally not on SummaryView —
// see Summary for the inspect-only superset that adds those lists for
// `inspect --json` consumers. MCP callers reach for the per-domain tools
// (policies, guardrails, budgets) when they need that detail.
type SummaryView struct {
	Projects                        int              `json:"projects"`
	ProjectsWithError               int              `json:"projects_with_errors"`
	ProjectDetails                  []ProjectSummary `json:"project_details"`
	Resources                       int              `json:"resources"`
	CostedResources                 int              `json:"costed_resources"`
	FreeResources                   int              `json:"free_resources"`
	MonthlyCost                     *rat.Rat         `json:"monthly_cost"`
	FinopsPolicies                  int              `json:"finops_policies"`
	FailingPolicies                 int              `json:"failing_policies"`
	DistinctFailingFinopsResources  int              `json:"distinct_failing_finops_resources,omitempty"`
	TaggingPolicies                 int              `json:"tagging_policies"`
	FailingTaggingPolicies          int              `json:"failing_tagging_policies"`
	DistinctFailingTaggingResources int              `json:"distinct_failing_tagging_resources,omitempty"`
	Guardrails                      int              `json:"guardrails"`
	TriggeredGuardrails             int              `json:"triggered_guardrails"`
	Budgets                         int              `json:"budgets"`
	OverBudget                      int              `json:"over_budget"`
	CriticalDiags                   int              `json:"critical_diagnostics"`
	WarningDiags                    int              `json:"warning_diagnostics"`
}

// Summary is the inspect-only superset of SummaryView. The embedded view
// keeps the headline JSON wire format flat (no breaking change for existing
// `inspect --json` consumers); the additional fields surface drill-in detail
// requested by inspect's JSON callers so they don't need a follow-up call to
// list the failing items.
//
// Currency and TotalMonthlySavings live here (not on SummaryView) because
// inspect_summary is a standalone MCP return — there's no outer envelope to
// carry the currency for the agent — and TotalMonthlySavings is the value
// inspect's `--total-savings` printout already computes, surfaced as a typed
// field so MCP callers don't need a follow-up tool call to get it.
type Summary struct {
	SummaryView
	Currency               string                   `json:"currency"`
	TotalMonthlySavings    *rat.Rat                 `json:"total_monthly_savings,omitempty"`
	FailingPolicyList      []FailingPolicyEntry     `json:"failing_policy_list,omitempty"`
	TriggeredGuardrailList []format.GuardrailOutput `json:"triggered_guardrail_list,omitempty"`
	OverBudgetList         []format.BudgetOutput    `json:"over_budget_list,omitempty"`
}

// FailingPolicyEntry is one failing policy + its failing resources, used in
// the enriched summary JSON. Per-resource detail (issues / missing+invalid
// tags) lives at the resource level so downstream consumers don't need a
// separate drill-in call.
type FailingPolicyEntry struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Slug    string `json:"slug,omitempty"`
	Message string `json:"message,omitempty"`
	Project string `json:"project"`
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

// summaryFieldValue returns the canonical-name → string-value mapping
// for one scalar summary field. Keys must match fieldsSummary (validated
// at the call site by validateFields).
func summaryFieldValue(s Summary, field, currency string) string {
	switch field {
	case "projects":
		return fmt.Sprintf("%d", s.Projects)
	case "projects_with_errors":
		return fmt.Sprintf("%d", s.ProjectsWithError)
	case "resources":
		return fmt.Sprintf("%d", s.Resources)
	case "costed_resources":
		return fmt.Sprintf("%d", s.CostedResources)
	case "free_resources":
		return fmt.Sprintf("%d", s.FreeResources)
	case "monthly_cost":
		return humanMoney(s.MonthlyCost, currency)
	case "total_monthly_savings":
		return humanMoney(s.TotalMonthlySavings, currency)
	case "finops_policies":
		return fmt.Sprintf("%d", s.FinopsPolicies)
	case "failing_policies":
		return fmt.Sprintf("%d", s.FailingPolicies)
	case "distinct_failing_finops_resources":
		return fmt.Sprintf("%d", s.DistinctFailingFinopsResources)
	case "tagging_policies":
		return fmt.Sprintf("%d", s.TaggingPolicies)
	case "failing_tagging_policies":
		return fmt.Sprintf("%d", s.FailingTaggingPolicies)
	case "distinct_failing_tagging_resources":
		return fmt.Sprintf("%d", s.DistinctFailingTaggingResources)
	case "guardrails":
		return fmt.Sprintf("%d", s.Guardrails)
	case "triggered_guardrails":
		return fmt.Sprintf("%d", s.TriggeredGuardrails)
	case "budgets":
		return fmt.Sprintf("%d", s.Budgets)
	case "over_budget":
		return fmt.Sprintf("%d", s.OverBudget)
	case "critical_diagnostics":
		return fmt.Sprintf("%d", s.CriticalDiags)
	case "warning_diagnostics":
		return fmt.Sprintf("%d", s.WarningDiags)
	}
	return ""
}

// writeSummaryProjection emits the requested summary fields. Single
// field → bare value (one number per question, no surrounding
// chrome). Multiple fields → "key: value" lines (matches the existing
// summary view's idiom). Structured output → flat {field: value} object,
// keys in the caller-specified order.
func writeSummaryProjection(w io.Writer, s Summary, fields []string, opts Options, currency string) error {
	if opts.Structured() {
		out := make(orderedFields, 0, len(fields))
		for _, f := range fields {
			out = append(out, orderedField{Key: f, Value: summaryFieldValue(s, f, currency)})
		}
		return writeStructured(w, out, opts)
	}
	if len(fields) == 1 {
		_, err := fmt.Fprintln(w, summaryFieldValue(s, fields[0], currency))
		return err
	}
	for _, f := range fields {
		if _, err := fmt.Fprintf(w, "%s: %s\n", f, summaryFieldValue(s, f, currency)); err != nil {
			return err
		}
	}
	return nil
}

func WriteSummary(w io.Writer, data *format.Output, opts Options) error {
	s := BuildSummary(data)

	// --fields short-circuit: project to just the requested scalars.
	// Single field → value alone (so a model can `wc -l` or read it
	// directly with no parsing). Multiple fields → key:value lines.
	// Honors --json / --llm by emitting a flat object with just the
	// requested keys.
	if len(opts.Fields) > 0 {
		fields, err := validateFields(opts.Fields, fieldsSummary)
		if err != nil {
			return err
		}
		return writeSummaryProjection(w, s, fields, opts, data.Currency)
	}

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
		kvRow{"Monthly cost", humanMoney(s.MonthlyCost, data.Currency)},
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
		writeProjectTable(&inner, s.ProjectDetails, data.Currency)
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
func writeProjectTable(w io.Writer, projects []ProjectSummary, currency string) {
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
			humanMoney(ps.MonthlyCost, currency),
			flagCount(ps.FinopsPolicies, ps.FinopsFailingPolicies, warnEmoji),
			flagCount(ps.TaggingPolicies, ps.TaggingFailingPolicies, warnEmoji),
		})
	}
	renderTable(w, cols, rows, ui.ContentWidth())
}

// BuildSummaryView computes the headline summary view from a scan/price
// Output. Pure function — same data the human renderer prints into the
// "Scan Summary" box (counts, monthly cost, per-project breakdown,
// diagnostic counts). Drill-in lists for failing policies/guardrails/
// budgets are not included here; they live on the inspect-only superset
// produced by BuildSummary.
func BuildSummaryView(data *format.Output) SummaryView {
	s := SummaryView{MonthlyCost: rat.Zero}

	// Track distinct resource addresses across projects so the same address
	// failing in two projects (or two policies) doesn't double-count.
	failingFinopsAddrs := map[string]struct{}{}
	failingTaggingAddrs := map[string]struct{}{}

	for _, p := range data.Projects {
		s.Projects++
		ps := ProjectSummary{
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
				for _, fr := range f.FailingResources {
					failingFinopsAddrs[fr.Name] = struct{}{}
				}
			}
		}

		for _, t := range p.TaggingResults {
			s.TaggingPolicies++
			ps.TaggingPolicies++
			if len(t.FailingResources) > 0 {
				s.FailingTaggingPolicies++
				ps.TaggingFailingPolicies++
				for _, tr := range t.FailingResources {
					failingTaggingAddrs[tr.Address] = struct{}{}
				}
			}
		}

		s.ProjectDetails = append(s.ProjectDetails, ps)
	}

	for _, gr := range data.GuardrailResults {
		s.Guardrails++
		if gr.Triggered {
			s.TriggeredGuardrails++
		}
	}

	for _, br := range data.BudgetResults {
		s.Budgets++
		if br.OverBudget {
			s.OverBudget++
		}
	}

	s.DistinctFailingFinopsResources = len(failingFinopsAddrs)
	s.DistinctFailingTaggingResources = len(failingTaggingAddrs)

	return s
}

// BuildSummary returns the inspect-only superset of [BuildSummaryView],
// adding currency, total monthly savings, and the failing-policy /
// triggered-guardrail / over-budget drill-in lists used by `inspect --json`
// so its consumers don't need a follow-up call to enumerate the failures.
// The aggregate counts shared with the MCP summary view are computed
// exactly once, by [BuildSummaryView].
func BuildSummary(data *format.Output) Summary {
	s := Summary{
		SummaryView: BuildSummaryView(data),
		Currency:    data.Currency,
	}
	if total := totalFinopsSavings(data); !total.IsZero() {
		s.TotalMonthlySavings = total
	}

	for _, p := range data.Projects {
		for _, f := range p.FinopsResults {
			if len(f.FailingResources) > 0 {
				s.FailingPolicyList = append(s.FailingPolicyList, FailingPolicyEntry{
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
			if len(t.FailingResources) > 0 {
				s.FailingPolicyList = append(s.FailingPolicyList, FailingPolicyEntry{
					Kind:           "tagging",
					Name:           t.PolicyName,
					Message:        t.Message,
					Project:        p.ProjectName,
					TagSchema:      t.TagSchema,
					FailingTagging: t.FailingResources,
				})
			}
		}
	}

	for _, gr := range data.GuardrailResults {
		if gr.Triggered {
			s.TriggeredGuardrailList = append(s.TriggeredGuardrailList, gr)
		}
	}

	for _, br := range data.BudgetResults {
		if br.OverBudget {
			s.OverBudgetList = append(s.OverBudgetList, br)
		}
	}

	return s
}

// SummaryFor applies the inspect filter pipeline (project / provider /
// costs-only / failing / etc.) and then builds the summary. This is the
// pure MCP-facing entry point: SummaryView counts and drill-in lists
// reflect the same scope `inspect --summary` would print with the same
// flags. ValidateGroupBy isn't called here because the summary view
// doesn't consume GroupBy — the caller has already validated other
// view-specific options if it cares about them.
func SummaryFor(data *format.Output, opts Options) (Summary, error) {
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return Summary{}, err
	}
	return BuildSummary(Filter(data, opts)), nil
}

// FailingPanoramaFor applies the inspect filter pipeline and then builds
// the failing-panorama view (failing policies + triggered guardrails +
// over-budget items). Pairs with the `inspect_failing` MCP tool.
func FailingPanoramaFor(data *format.Output, opts Options) (FailingPanorama, error) {
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return FailingPanorama{}, err
	}
	return BuildFailingPanorama(Filter(data, opts)), nil
}
