package format

import (
	"encoding/json"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/infracost/go-proto/pkg/rat"
)

// ScanDiffOutput is the top-level JSON structure produced by `scan --diff`.
// Field names follow the snake_case convention of Output so `scan --json` and
// `scan --diff --json` emit one consistent casing.
// Monetary values are fixed two-decimal strings; percentages are numbers, and
// null when the previous cost is zero (the change is undefined, not 0%).
type ScanDiffOutput struct {
	Currency                         string                       `json:"currency"`
	TotalMonthlyCost                 string                       `json:"total_monthly_cost"`
	PastTotalMonthlyCost             string                       `json:"past_total_monthly_cost"`
	DiffTotalMonthlyCost             string                       `json:"diff_total_monthly_cost"`
	PercentageChangeTotalMonthlyCost *float64                     `json:"percentage_change_total_monthly_cost"`
	Diff                             map[string]*ResourceTypeDiff `json:"diff"`
}

// ResourceTypeDiff aggregates the changed resources of one resource type
// (e.g. "aws_instance"). Its cost fields sum the resources listed in Diff —
// unchanged resources of the same type are excluded, so the block is
// self-consistent with its entries.
type ResourceTypeDiff struct {
	CurrentMonthlyCost          string               `json:"current_monthly_cost"`
	PreviousMonthlyCost         string               `json:"previous_monthly_cost"`
	DiffMonthlyCost             string               `json:"diff_monthly_cost"`
	PercentageChangeMonthlyCost *float64             `json:"percentage_change_monthly_cost"`
	Diff                        []*ResourceDiffEntry `json:"diff"`
}

// ResourceDiffEntry is the cost change of a single resource. Subresources
// breaks the change down one level: cost components by component name, and
// child resources by their name (recursive total). Only changed entries
// appear, matching the resource-level filter.
type ResourceDiffEntry struct {
	Name                        string               `json:"name"`
	CurrentMonthlyCost          string               `json:"current_monthly_cost"`
	PreviousMonthlyCost         string               `json:"previous_monthly_cost"`
	DiffMonthlyCost             string               `json:"diff_monthly_cost"`
	PercentageChangeMonthlyCost *float64             `json:"percentage_change_monthly_cost"`
	Subresources                map[string]*CostDiff `json:"subresources,omitempty"`
}

// CostDiff is the leaf cost-change block shared by subresource/component
// entries.
type CostDiff struct {
	CurrentMonthlyCost          string   `json:"current_monthly_cost"`
	PreviousMonthlyCost         string   `json:"previous_monthly_cost"`
	DiffMonthlyCost             string   `json:"diff_monthly_cost"`
	PercentageChangeMonthlyCost *float64 `json:"percentage_change_monthly_cost"`
}

// ToJSON writes a ScanDiffOutput as indented JSON to w.
func (d *ScanDiffOutput) ToJSON(w io.Writer) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// BuildScanDiff diffs two scan outputs — prev from the plan's prior state,
// curr from its planned values — into the `scan --diff` shape. Totals cover
// every resource on each side; the Diff map carries only resources whose
// monthly cost changed, grouped by resource type. Resources are matched by
// address, so a resource that only appears on one side is treated as
// added/removed with a zero cost on the missing side.
func BuildScanDiff(prev, curr *Output) *ScanDiffOutput {
	prevRes := flattenResources(prev)
	currRes := flattenResources(curr)

	prevTotal := sumResourceCosts(prevRes)
	currTotal := sumResourceCosts(currRes)

	out := &ScanDiffOutput{
		Currency:                         curr.Currency,
		TotalMonthlyCost:                 currTotal.StringFixed(2),
		PastTotalMonthlyCost:             prevTotal.StringFixed(2),
		DiffTotalMonthlyCost:             currTotal.Sub(prevTotal).StringFixed(2),
		PercentageChangeTotalMonthlyCost: percentChange(prevTotal, currTotal),
		Diff:                             map[string]*ResourceTypeDiff{},
	}

	type pair struct {
		resType string
		prev    *ResourceOutput
		curr    *ResourceOutput
	}
	pairs := map[string]*pair{}
	for name, r := range prevRes {
		pairs[name] = &pair{resType: r.Type, prev: r}
	}
	for name, r := range currRes {
		p, ok := pairs[name]
		if !ok {
			p = &pair{}
			pairs[name] = p
		}
		// The current side wins the type label on the (unlikely) event a
		// resource address changed type between states.
		p.resType = r.Type
		p.curr = r
	}

	names := make([]string, 0, len(pairs))
	for name := range pairs {
		names = append(names, name)
	}
	sort.Strings(names)

	typeTotals := map[string][2]*rat.Rat{}
	for _, name := range names {
		p := pairs[name]
		prevCost := rat.Zero
		if p.prev != nil {
			prevCost = resourceMonthlyCost(p.prev)
		}
		currCost := rat.Zero
		if p.curr != nil {
			currCost = resourceMonthlyCost(p.curr)
		}
		if prevCost.Equals(currCost) {
			continue
		}

		entry := &ResourceDiffEntry{
			Name:                        name,
			CurrentMonthlyCost:          currCost.StringFixed(2),
			PreviousMonthlyCost:         prevCost.StringFixed(2),
			DiffMonthlyCost:             currCost.Sub(prevCost).StringFixed(2),
			PercentageChangeMonthlyCost: percentChange(prevCost, currCost),
			Subresources:                diffSubcosts(p.prev, p.curr),
		}

		td, ok := out.Diff[p.resType]
		if !ok {
			td = &ResourceTypeDiff{}
			out.Diff[p.resType] = td
			typeTotals[p.resType] = [2]*rat.Rat{rat.Zero, rat.Zero}
		}
		td.Diff = append(td.Diff, entry)
		totals := typeTotals[p.resType]
		typeTotals[p.resType] = [2]*rat.Rat{totals[0].Add(prevCost), totals[1].Add(currCost)}
	}

	for resType, td := range out.Diff {
		totals := typeTotals[resType]
		td.PreviousMonthlyCost = totals[0].StringFixed(2)
		td.CurrentMonthlyCost = totals[1].StringFixed(2)
		td.DiffMonthlyCost = totals[1].Sub(totals[0]).StringFixed(2)
		td.PercentageChangeMonthlyCost = percentChange(totals[0], totals[1])
	}

	return out
}

// flattenResources indexes every top-level resource in the output by name
// (its full address). Names collide across projects only if the same address
// appears twice, in which case the costs would be ambiguous either way; the
// last one wins.
func flattenResources(o *Output) map[string]*ResourceOutput {
	res := map[string]*ResourceOutput{}
	for pi := range o.Projects {
		p := &o.Projects[pi]
		for ri := range p.Resources {
			r := &p.Resources[ri]
			res[r.Name] = r
		}
	}
	return res
}

func sumResourceCosts(res map[string]*ResourceOutput) *rat.Rat {
	total := rat.Zero
	for _, r := range res {
		total = total.Add(resourceMonthlyCost(r))
	}
	return total
}

// diffSubcosts builds the one-level breakdown of a resource's cost change:
// each cost component by name, and each child resource by name with its
// recursive total. Entries whose cost did not change are dropped. Either side
// may be nil (added/removed resource).
//
// Lines are paired by exact name first, then — mirroring the legacy CLI's
// findMatchingCostComponent — by the text before the bracket, so a resize
// pairs "Instance usage (..., t2.small)" with "Instance usage (..., t2.medium)"
// as one changed entry (keyed by the current name) instead of two entries
// bouncing through zero. The bracket fallback requires a bracket on both
// sides; a bare name never pairs with a bracketed one.
func diffSubcosts(prev, curr *ResourceOutput) map[string]*CostDiff {
	prevCosts := subcosts(prev)
	currCosts := subcosts(curr)

	type costPair struct {
		prev *rat.Rat
		curr *rat.Rat
	}
	pairs := map[string]costPair{}
	matchedCurr := map[string]bool{}

	// Iterate names sorted so bracket-fallback pairing is deterministic when
	// several lines share a prefix.
	prevNames := sortedKeys(prevCosts)
	currNames := sortedKeys(currCosts)

	var unmatchedPrev []string
	for _, name := range prevNames {
		if currCost, ok := currCosts[name]; ok {
			pairs[name] = costPair{prev: prevCosts[name], curr: currCost}
			matchedCurr[name] = true
			continue
		}
		unmatchedPrev = append(unmatchedPrev, name)
	}

	for _, prevName := range unmatchedPrev {
		pairName := prevName
		pair := costPair{prev: prevCosts[prevName], curr: rat.Zero}
		if prefix, ok := bracketPrefix(prevName); ok {
			for _, currName := range currNames {
				if matchedCurr[currName] {
					continue
				}
				if currPrefix, ok := bracketPrefix(currName); ok && currPrefix == prefix {
					pairName = currName
					pair.curr = currCosts[currName]
					matchedCurr[currName] = true
					break
				}
			}
		}
		pairs[pairName] = pair
	}

	for _, currName := range currNames {
		if !matchedCurr[currName] {
			pairs[currName] = costPair{prev: rat.Zero, curr: currCosts[currName]}
		}
	}

	out := map[string]*CostDiff{}
	for name, pair := range pairs {
		if pair.prev.Equals(pair.curr) {
			continue
		}
		out[name] = &CostDiff{
			CurrentMonthlyCost:          pair.curr.StringFixed(2),
			PreviousMonthlyCost:         pair.prev.StringFixed(2),
			DiffMonthlyCost:             pair.curr.Sub(pair.prev).StringFixed(2),
			PercentageChangeMonthlyCost: percentChange(pair.prev, pair.curr),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// bracketPrefix returns the text before the first " (" in a cost line name,
// reporting whether the name has a bracketed suffix at all.
func bracketPrefix(name string) (string, bool) {
	prefix, _, found := strings.Cut(name, " (")
	return prefix, found
}

func sortedKeys(m map[string]*rat.Rat) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// subcosts maps a resource's immediate cost lines by name: cost components
// to their monthly total and child resources to their recursive total.
// Duplicate names accumulate.
func subcosts(r *ResourceOutput) map[string]*rat.Rat {
	out := map[string]*rat.Rat{}
	if r == nil {
		return out
	}
	add := func(name string, cost *rat.Rat) {
		if cost == nil {
			return
		}
		if existing, ok := out[name]; ok {
			out[name] = existing.Add(cost)
			return
		}
		out[name] = cost
	}
	for i := range r.CostComponents {
		add(r.CostComponents[i].Name, r.CostComponents[i].TotalMonthlyCost)
	}
	for i := range r.Subresources {
		add(r.Subresources[i].Name, resourceMonthlyCost(&r.Subresources[i]))
	}
	return out
}

// percentChange returns the percentage change from prev to curr, rounded to
// two decimal places. A change from zero is undefined and returns nil (except
// zero to zero, which is 0).
func percentChange(prev, curr *rat.Rat) *float64 {
	if prev.IsZero() {
		if curr.IsZero() {
			zero := 0.0
			return &zero
		}
		return nil
	}
	v := curr.Sub(prev).Div(prev).Mul(rat.New(100)).Float64()
	v = math.Round(v*100) / 100
	return &v
}
