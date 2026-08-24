package format

import (
	"encoding/json"
	"io"
	"math"
	"sort"

	"github.com/infracost/go-proto/pkg/rat"
)

// ScanDiffOutput is the top-level JSON structure produced by `scan --diff`.
// Field names and casing are a published contract with external consumers, so
// they deliberately do not follow the snake_case convention of Output.
// Monetary values are fixed two-decimal strings; percentages are numbers, and
// null when the previous cost is zero (the change is undefined, not 0%).
type ScanDiffOutput struct {
	Currency                         string                       `json:"Currency"`
	TotalMonthlyCost                 string                       `json:"TotalMonthlyCost"`
	PastTotalMonthlyCost             string                       `json:"PastTotalMonthlyCost"`
	DiffTotalMonthlyCost             string                       `json:"DiffTotalMonthlyCost"`
	PercentageChangeTotalMonthlyCost *float64                     `json:"PercentageChangeTotalMonthlyCost"`
	Diff                             map[string]*ResourceTypeDiff `json:"Diff"`
}

// ResourceTypeDiff aggregates the changed resources of one resource type
// (e.g. "aws_instance"). Its cost fields sum the resources listed in Diff —
// unchanged resources of the same type are excluded, so the block is
// self-consistent with its entries.
type ResourceTypeDiff struct {
	CurrentMonthlyCost          string               `json:"CurrentMonthlyCost"`
	PreviousMonthlyCost         string               `json:"PreviousMonthlyCost"`
	DiffMonthlyCost             string               `json:"DiffMonthlyCost"`
	PercentageChangeMonthlyCost *float64             `json:"PercentageChangeMonthlyCost"`
	Diff                        []*ResourceDiffEntry `json:"Diff"`
}

// ResourceDiffEntry is the cost change of a single resource. Subresources
// breaks the change down one level: cost components by component name, and
// child resources by their name (recursive total). Only changed entries
// appear, matching the resource-level filter.
type ResourceDiffEntry struct {
	Name                        string               `json:"Name"`
	CurrentMonthlyCost          string               `json:"CurrentMonthlyCost"`
	PreviousMonthlyCost         string               `json:"PreviousMonthlyCost"`
	DiffMonthlyCost             string               `json:"DiffMonthlyCost"`
	PercentageChangeMonthlyCost *float64             `json:"PercentageChangeMonthlyCost"`
	Subresources                map[string]*CostDiff `json:"Subresources,omitempty"`
}

// CostDiff is the leaf cost-change block shared by subresource/component
// entries.
type CostDiff struct {
	CurrentMonthlyCost          string   `json:"CurrentMonthlyCost"`
	PreviousMonthlyCost         string   `json:"PreviousMonthlyCost"`
	DiffMonthlyCost             string   `json:"DiffMonthlyCost"`
	PercentageChangeMonthlyCost *float64 `json:"PercentageChangeMonthlyCost"`
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
func diffSubcosts(prev, curr *ResourceOutput) map[string]*CostDiff {
	prevCosts := subcosts(prev)
	currCosts := subcosts(curr)

	out := map[string]*CostDiff{}
	for name, prevCost := range prevCosts {
		currCost, ok := currCosts[name]
		if !ok {
			currCost = rat.Zero
		}
		if prevCost.Equals(currCost) {
			continue
		}
		out[name] = &CostDiff{
			CurrentMonthlyCost:          currCost.StringFixed(2),
			PreviousMonthlyCost:         prevCost.StringFixed(2),
			DiffMonthlyCost:             currCost.Sub(prevCost).StringFixed(2),
			PercentageChangeMonthlyCost: percentChange(prevCost, currCost),
		}
	}
	for name, currCost := range currCosts {
		if _, seen := prevCosts[name]; seen {
			continue
		}
		if currCost.IsZero() {
			continue
		}
		out[name] = &CostDiff{
			CurrentMonthlyCost:          currCost.StringFixed(2),
			PreviousMonthlyCost:         rat.Zero.StringFixed(2),
			DiffMonthlyCost:             currCost.StringFixed(2),
			PercentageChangeMonthlyCost: percentChange(rat.Zero, currCost),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
