package views

import (
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
)

// RenderSummary returns the stat block shown above the list/detail
// split. When projectName is empty the block aggregates across every
// project in the scan (the default view). When the user has tabbed
// into a specific project, projectName scopes the block to just that
// project's resources and policies — guardrails and budgets remain
// scan-level so they aren't repeated under a single project view.
//
// Stats render as inline "label: value" pairs separated by a muted
// "·" divider. Pairs that don't fit on one line wrap to the next.
// Returns "" when out is nil so the caller can omit the box on a
// cache miss.
func RenderSummary(out *format.Output, projectName string, width int) string {
	if out == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}

	s := computeStats(out, projectName)
	pairs := []kv{}

	pairs = append(pairs, kv{"Monthly cost", styles.Cost().Render(inspect.HumanMoney(s.MonthlyCost, out.Currency) + "/mo")})
	pairs = append(pairs, kv{"Resources", fmt.Sprintf("%d", s.Resources)})
	if s.CostedResources > 0 || s.FreeResources > 0 {
		pairs = append(pairs, kv{"Costed", fmt.Sprintf("%d", s.CostedResources)})
		pairs = append(pairs, kv{"Free", fmt.Sprintf("%d", s.FreeResources)})
	}
	if s.Projects > 1 {
		pairs = append(pairs, kv{"Projects", fmt.Sprintf("%d", s.Projects)})
	}
	if s.FinopsPolicies > 0 {
		v := fmt.Sprintf("%d / %d", s.DistinctFailingFinopsResources, s.Resources)
		if s.DistinctFailingFinopsResources > 0 {
			v = styles.Danger().Render(v)
		}
		pairs = append(pairs, kv{"FinOps failing", v})
	}
	if s.TaggingPolicies > 0 {
		v := fmt.Sprintf("%d / %d", s.DistinctFailingTaggingResources, s.Resources)
		if s.DistinctFailingTaggingResources > 0 {
			v = styles.Danger().Render(v)
		}
		pairs = append(pairs, kv{"Tagging failing", v})
	}
	if s.Guardrails > 0 {
		v := fmt.Sprintf("%d / %d", s.TriggeredGuardrails, s.Guardrails)
		if s.TriggeredGuardrails > 0 {
			v = styles.Danger().Render(v)
		}
		pairs = append(pairs, kv{"Guardrails triggered", v})
	}
	if s.Budgets > 0 {
		v := fmt.Sprintf("%d / %d", s.OverBudget, s.Budgets)
		if s.OverBudget > 0 {
			v = styles.Danger().Render(v)
		}
		pairs = append(pairs, kv{"Over budget", v})
	}

	return renderInlinePairs(pairs, width)
}

// summaryStats holds the values rendered by RenderSummary, computed
// either across the whole scan or scoped to a single project.
//
// FinOps and tagging are reported as "distinct failing resources /
// total resources" — a true subset relation where the numerator can
// never exceed the denominator. We considered using individual issue
// instances as the numerator (a resource with three storage-class
// problems contributes 3) but the data model has no companion "max
// possible issues" metric to serve as a denominator, so the fraction
// would have been numerator-can-exceed-denominator and stop reading
// as a fraction at all. Resource-level counting also matches the way
// the user typically asks the question: "how many resources do I
// need to look at?".
type summaryStats struct {
	MonthlyCost                     *rat.Rat
	Resources                       int
	CostedResources                 int
	FreeResources                   int
	Projects                        int
	FinopsPolicies                  int
	DistinctFailingFinopsResources  int
	TaggingPolicies                 int
	DistinctFailingTaggingResources int
	Guardrails                      int
	TriggeredGuardrails             int
	Budgets                         int
	OverBudget                      int
}

// computeStats builds a summaryStats from the active output. When
// projectName is empty it prefers the pre-computed Output.Summary
// (cheap, populated by format.ToOutput). When scoped to a single
// project it walks that project's resources/policies directly —
// the per-project breakdown isn't carried in Output.Summary.
//
// Guardrails and budgets are scan-level and aren't repeated under a
// single project view: they apply across the whole scan, so seeing
// them under one project would imply they only flag that project.
func computeStats(out *format.Output, projectName string) summaryStats {
	var s summaryStats
	s.MonthlyCost = rat.Zero

	if projectName != "" {
		for i := range out.Projects {
			if out.Projects[i].ProjectName == projectName {
				s = addProject(s, &out.Projects[i])
				s.Projects = 1
				return s
			}
		}
		return s
	}

	// Aggregate. Output.Summary has pre-computed cost / resource counts
	// but doesn't carry per-issue tallies — those we always derive by
	// walking the projects directly. Walking is linear in resources, so
	// dropping the cache costs nothing measurable here.
	for i := range out.Projects {
		s = addProject(s, &out.Projects[i])
	}
	s.Projects = len(out.Projects)
	s.Guardrails = len(out.GuardrailResults)
	for _, g := range out.GuardrailResults {
		if g.Triggered {
			s.TriggeredGuardrails++
		}
	}
	s.Budgets = len(out.BudgetResults)
	for _, b := range out.BudgetResults {
		if b.OverBudget {
			s.OverBudget++
		}
	}
	return s
}

// addProject folds a single project's resource and policy stats into s.
//
// Failing-resource counts dedupe across policies within the project: a
// resource flagged by two FinOps policies contributes 1, not 2, since
// the user looks for "how many distinct resources need attention".
// Aggregating across projects, we sum these per-project counts —
// resource addresses include the project context in practice, so
// cross-project collisions are extremely rare; if they happen the
// scan-level total slightly overcounts, which is acceptable for a
// summary heuristic.
func addProject(s summaryStats, p *format.ProjectOutput) summaryStats {
	for i := range p.Resources {
		r := &p.Resources[i]
		s.Resources++
		cost := inspect.ResourceCost(r)
		s.MonthlyCost = s.MonthlyCost.Add(cost)
		if cost.GreaterThanZero() {
			s.CostedResources++
		} else {
			s.FreeResources++
		}
	}

	s.FinopsPolicies += len(p.FinopsResults)
	finopsFailing := map[string]struct{}{}
	for _, f := range p.FinopsResults {
		for _, fr := range f.FailingResources {
			finopsFailing[fr.Name] = struct{}{}
		}
	}
	s.DistinctFailingFinopsResources += len(finopsFailing)

	s.TaggingPolicies += len(p.TaggingResults)
	taggingFailing := map[string]struct{}{}
	for _, t := range p.TaggingResults {
		for _, tr := range t.FailingResources {
			taggingFailing[tr.Address] = struct{}{}
		}
	}
	s.DistinctFailingTaggingResources += len(taggingFailing)

	return s
}

// kv is a label/value pair for the summary's inline rendering.
type kv struct {
	label string
	value string
}

// renderInlinePairs lays out pairs as "Label: value   Label: value …"
// using ui.PrintableWidth-aware truncation so styled values don't blow
// past the pane edge. Pairs that don't fit on one line wrap to the
// next.
func renderInlinePairs(pairs []kv, width int) string {
	separator := styles.Muted().Render("   ·   ")
	sepWidth := ui.PrintableWidth(separator)

	var lines []string
	var current strings.Builder
	currentWidth := 0

	for _, p := range pairs {
		piece := styles.Muted().Render(p.label+": ") + p.value
		w := ui.PrintableWidth(piece)

		need := w
		if currentWidth > 0 {
			need += sepWidth
		}

		if currentWidth+need > width && currentWidth > 0 {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}

		if currentWidth > 0 {
			current.WriteString(separator)
			currentWidth += sepWidth
		}
		current.WriteString(piece)
		currentWidth += w
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return strings.Join(lines, "\n")
}
