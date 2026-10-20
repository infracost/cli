package format

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/infracost/cli/internal/format/toon"
	"github.com/infracost/go-proto/pkg/rat"
)

// DiffOp is how a node changed between the baseline and the current scan.
type DiffOp string

const (
	DiffAdded   DiffOp = "added"
	DiffRemoved DiffOp = "removed"
	DiffChanged DiffOp = "changed"
)

// DiffOutput is what the diff command produces. Diff* costs are current minus
// baseline; a missing side counts as zero.
type DiffOutput struct {
	Currency             string        `json:"currency"`
	Projects             []DiffProject `json:"projects"`
	PastTotalMonthlyCost *rat.Rat      `json:"past_total_monthly_cost,omitempty"`
	TotalMonthlyCost     *rat.Rat      `json:"total_monthly_cost,omitempty"`
	DiffTotalMonthlyCost *rat.Rat      `json:"diff_total_monthly_cost,omitempty"`
}

type DiffProject struct {
	ProjectName string `json:"project_name"`
	Path        string `json:"path,omitempty"`
	Op          DiffOp `json:"op"`
	// Resources holds only what changed. A project with none is still listed
	// so the caller can report it as unchanged.
	Resources            []DiffResource `json:"resources"`
	PastTotalMonthlyCost *rat.Rat       `json:"past_total_monthly_cost,omitempty"`
	TotalMonthlyCost     *rat.Rat       `json:"total_monthly_cost,omitempty"`
	DiffTotalMonthlyCost *rat.Rat       `json:"diff_total_monthly_cost,omitempty"`
}

type DiffResource struct {
	Name            string              `json:"name"`
	Type            string              `json:"type,omitempty"`
	Op              DiffOp              `json:"op"`
	PastMonthlyCost *rat.Rat            `json:"past_monthly_cost,omitempty"`
	MonthlyCost     *rat.Rat            `json:"monthly_cost,omitempty"`
	DiffMonthlyCost *rat.Rat            `json:"diff_monthly_cost,omitempty"`
	CostComponents  []DiffCostComponent `json:"cost_components,omitempty"`
	Subresources    []DiffResource      `json:"subresources,omitempty"`
	// CostDependsOnUsage is true when either side's costs are all
	// usage-driven, so the total delta is not a real change.
	CostDependsOnUsage bool `json:"cost_depends_on_usage,omitempty"`
}

type DiffCostComponent struct {
	// Name is "old → new" when the two sides matched on the part before the
	// bracket, e.g. an instance type change.
	Name            string   `json:"name"`
	Unit            string   `json:"unit,omitempty"`
	Op              DiffOp   `json:"op"`
	PastMonthlyCost *rat.Rat `json:"past_monthly_cost,omitempty"`
	MonthlyCost     *rat.Rat `json:"monthly_cost,omitempty"`
	DiffMonthlyCost *rat.Rat `json:"diff_monthly_cost,omitempty"`
	PastPrice       *rat.Rat `json:"past_price,omitempty"`
	Price           *rat.Rat `json:"price,omitempty"`
	DiffPrice       *rat.Rat `json:"diff_price,omitempty"`
	PastQuantity    *rat.Rat `json:"past_quantity,omitempty"`
	Quantity        *rat.Rat `json:"quantity,omitempty"`
	DiffQuantity    *rat.Rat `json:"diff_quantity,omitempty"`
	// CostDependsOnUsage is true when either side is usage-driven, so the
	// price per unit is the only number to show.
	CostDependsOnUsage bool `json:"cost_depends_on_usage,omitempty"`
}

// Diff compares a current scan against a baseline. Projects, resources and
// subresources match by name; cost components fall back to the part before " (".
func Diff(past, current *Output) *DiffOutput {
	currency := current.Currency
	if currency == "" {
		currency = past.Currency
	}
	if currency == "" {
		currency = "USD"
	}
	out := &DiffOutput{Currency: currency}

	// Indexes per name, so repeated project names pair up one by one.
	matched := make([]bool, len(past.Projects))

	// Name and path first, so reordering or removing one of two same-named
	// projects does not pair the survivor with the wrong baseline entry.
	pairs := make([]*ProjectOutput, len(current.Projects))
	for i := range current.Projects {
		pairs[i] = matchProject(past.Projects, matched, &current.Projects[i], true)
	}
	for i := range current.Projects {
		if pairs[i] == nil {
			pairs[i] = matchProject(past.Projects, matched, &current.Projects[i], false)
		}
	}

	for i := range current.Projects {
		op := DiffAdded
		if pairs[i] != nil {
			op = DiffChanged
		}
		out.Projects = append(out.Projects, diffProject(op, pairs[i], &current.Projects[i]))
	}

	// Anything left in the baseline is gone, in the baseline's own order.
	for i := range past.Projects {
		if matched[i] {
			continue
		}
		out.Projects = append(out.Projects, diffProject(DiffRemoved, &past.Projects[i], nil))
	}

	out.PastTotalMonthlyCost = rat.Zero
	out.TotalMonthlyCost = rat.Zero
	for _, p := range out.Projects {
		out.PastTotalMonthlyCost = out.PastTotalMonthlyCost.Add(orZero(p.PastTotalMonthlyCost))
		out.TotalMonthlyCost = out.TotalMonthlyCost.Add(orZero(p.TotalMonthlyCost))
	}
	out.DiffTotalMonthlyCost = out.TotalMonthlyCost.Sub(out.PastTotalMonthlyCost)

	return out
}

// matchProject claims the first unmatched baseline project with the same name,
// requiring an equal path when withPath is set.
func matchProject(past []ProjectOutput, matched []bool, cur *ProjectOutput, withPath bool) *ProjectOutput {
	for i := range past {
		if matched[i] || past[i].ProjectName != cur.ProjectName {
			continue
		}
		if withPath && past[i].Path != cur.Path {
			continue
		}
		matched[i] = true
		return &past[i]
	}
	return nil
}

// HasChanges reports whether any project has a changed resource.
func (d *DiffOutput) HasChanges() bool {
	for _, p := range d.Projects {
		if len(p.Resources) > 0 {
			return true
		}
	}
	return false
}

func (d *DiffOutput) ToJSON(w io.Writer) error {
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(out)
	return err
}

func (d *DiffOutput) ToTOON(w io.Writer) error {
	return toon.MarshalTo(w, d)
}

// UnchangedProjects names the projects with no resource-level change.
func (d *DiffOutput) UnchangedProjects() []string {
	var names []string
	for _, p := range d.Projects {
		if len(p.Resources) == 0 {
			names = append(names, p.ProjectName)
		}
	}
	return names
}

func diffProject(op DiffOp, past, current *ProjectOutput) DiffProject {
	base := current
	if base == nil {
		base = past
	}

	p := DiffProject{
		ProjectName:          base.ProjectName,
		Path:                 base.Path,
		Op:                   op,
		PastTotalMonthlyCost: projectMonthlyCost(past),
		TotalMonthlyCost:     projectMonthlyCost(current),
	}
	p.DiffTotalMonthlyCost = p.TotalMonthlyCost.Sub(p.PastTotalMonthlyCost)
	p.Resources = diffResources(resourcesOf(past), resourcesOf(current))

	return p
}

// diffResources walks the baseline first so removals keep their position,
// then appends resources only the current scan has.
func diffResources(past, current []ResourceOutput) []DiffResource {
	currentByName := make(map[string]*ResourceOutput, len(current))
	for i := range current {
		currentByName[current[i].Name] = &current[i]
	}

	var out []DiffResource
	seen := make(map[string]bool, len(past))

	for i := range past {
		prev := &past[i]
		seen[prev.Name] = true
		if d, changed := diffResource(prev, currentByName[prev.Name]); changed {
			out = append(out, d)
		}
	}

	for i := range current {
		cur := &current[i]
		if seen[cur.Name] {
			continue
		}
		if d, changed := diffResource(nil, cur); changed {
			out = append(out, d)
		}
	}

	return out
}

func diffResource(past, current *ResourceOutput) (DiffResource, bool) {
	base := current
	op := DiffChanged
	switch {
	case current == nil:
		base = past
		op = DiffRemoved
	case past == nil:
		op = DiffAdded
	}

	d := DiffResource{
		Name:            base.Name,
		Type:            base.Type,
		Op:              op,
		PastMonthlyCost: monthlyCostOf(past),
		MonthlyCost:     monthlyCostOf(current),
	}
	d.DiffMonthlyCost = d.MonthlyCost.Sub(d.PastMonthlyCost)
	d.CostDependsOnUsage = (past != nil && unpriced(past)) || (current != nil && unpriced(current))

	components, componentsChanged := diffCostComponents(componentsOf(past), componentsOf(current))
	d.CostComponents = components

	subresources := diffResources(subresourcesOf(past), subresourcesOf(current))
	d.Subresources = subresources

	return d, componentsChanged || len(subresources) > 0
}

// diffCostComponents pairs components by name, keeping the baseline's order
// and appending the ones only the current scan has.
func diffCostComponents(past, current []CostComponentOutput) ([]DiffCostComponent, bool) {
	matched := make([]bool, len(current))
	pairs := make([]*CostComponentOutput, len(past))

	// Exact names across the whole list first, so the bracket fallback can
	// never consume a component that a later baseline entry matches exactly.
	for i := range past {
		pairs[i] = matchExactName(current, matched, past[i].Name)
	}
	for i := range past {
		if pairs[i] == nil {
			pairs[i] = matchBracketPrefix(current, matched, past[i].Name)
		}
	}

	var out []DiffCostComponent

	for i := range past {
		if d, changed := diffCostComponent(&past[i], pairs[i]); changed {
			out = append(out, d)
		}
	}

	for i := range current {
		if matched[i] {
			continue
		}
		if d, changed := diffCostComponent(nil, &current[i]); changed {
			out = append(out, d)
		}
	}

	return out, len(out) > 0
}

func diffCostComponent(past, current *CostComponentOutput) (DiffCostComponent, bool) {
	base := current
	op := DiffChanged
	switch {
	case current == nil:
		base = past
		op = DiffRemoved
	case past == nil:
		op = DiffAdded
	}

	d := DiffCostComponent{
		Name:            diffName(current, past),
		Unit:            base.Unit,
		Op:              op,
		PastMonthlyCost: costOf(past),
		MonthlyCost:     costOf(current),
		PastPrice:       priceOf(past),
		Price:           priceOf(current),
		PastQuantity:    quantityOf(past),
		Quantity:        quantityOf(current),
	}
	d.DiffMonthlyCost = d.MonthlyCost.Sub(d.PastMonthlyCost)
	d.DiffPrice = d.Price.Sub(d.PastPrice)
	d.DiffQuantity = d.Quantity.Sub(d.PastQuantity)
	// Either side being usage-driven makes the cost delta meaningless, so a
	// $100 component losing its usage must not read as a $100 saving.
	pastUnknown, currentUnknown := usageUnknown(past), usageUnknown(current)
	d.CostDependsOnUsage = pastUnknown || currentUnknown

	changed := !d.DiffMonthlyCost.IsZero() || !d.DiffPrice.IsZero() || !d.DiffQuantity.IsZero() ||
		pastUnknown != currentUnknown
	return d, changed
}

func matchExactName(components []CostComponentOutput, matched []bool, name string) *CostComponentOutput {
	for i := range components {
		if !matched[i] && components[i].Name == name {
			matched[i] = true
			return &components[i]
		}
	}
	return nil
}

// matchBracketPrefix pairs on the part before " (" so a resized instance reads
// as a change. It needs a single candidate, so pricing tiers never pair.
func matchBracketPrefix(components []CostComponentOutput, matched []bool, name string) *CostComponentOutput {
	wanted, _, bracketed := strings.Cut(name, " (")
	if !bracketed {
		return nil
	}

	found := -1
	for i := range components {
		if matched[i] {
			continue
		}
		if prefix, _, ok := strings.Cut(components[i].Name, " ("); ok && prefix == wanted {
			if found >= 0 {
				return nil
			}
			found = i
		}
	}
	if found < 0 {
		return nil
	}

	matched[found] = true
	return &components[found]
}

// diffName shows both names when a bracket match paired two different ones.
func diffName(current, past *CostComponentOutput) string {
	switch {
	case current == nil:
		return past.Name
	case past == nil || past.Name == current.Name:
		return current.Name
	default:
		return past.Name + " → " + current.Name
	}
}

func projectMonthlyCost(p *ProjectOutput) *rat.Rat {
	total := rat.Zero
	if p == nil {
		return total
	}
	for i := range p.Resources {
		total = total.Add(resourceMonthlyCost(&p.Resources[i]))
	}
	return total
}

func resourcesOf(p *ProjectOutput) []ResourceOutput {
	if p == nil {
		return nil
	}
	return p.Resources
}

func subresourcesOf(r *ResourceOutput) []ResourceOutput {
	if r == nil {
		return nil
	}
	return r.Subresources
}

func componentsOf(r *ResourceOutput) []CostComponentOutput {
	if r == nil {
		return nil
	}
	return r.CostComponents
}

func monthlyCostOf(r *ResourceOutput) *rat.Rat {
	if r == nil {
		return rat.Zero
	}
	return resourceMonthlyCost(r)
}

func costOf(c *CostComponentOutput) *rat.Rat {
	if c == nil {
		return rat.Zero
	}
	return orZero(c.TotalMonthlyCost)
}

func priceOf(c *CostComponentOutput) *rat.Rat {
	if c == nil {
		return rat.Zero
	}
	return orZero(c.Price)
}

func quantityOf(c *CostComponentOutput) *rat.Rat {
	if c == nil {
		return rat.Zero
	}
	return orZero(c.Quantity)
}

// unpriced reports whether every cost in a resource's tree is usage-driven.
// Callers check for nil first: a missing side has no usage to be unsure of.
func unpriced(r *ResourceOutput) bool {
	if r == nil {
		return true
	}
	for i := range r.CostComponents {
		if !usageUnknown(&r.CostComponents[i]) {
			return false
		}
	}
	for i := range r.Subresources {
		if !unpriced(&r.Subresources[i]) {
			return false
		}
	}
	return true
}

// usageUnknown reports whether a component that exists is usage-driven. A
// missing side is not unknown: it has no usage to be uncertain about.
func usageUnknown(c *CostComponentOutput) bool {
	return c != nil && (c.UsageUnknown || c.TotalMonthlyCost == nil)
}

func orZero(r *rat.Rat) *rat.Rat {
	if r == nil {
		return rat.Zero
	}
	return r
}
