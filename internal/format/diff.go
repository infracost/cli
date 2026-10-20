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

// DiffOutput is the top-level structure produced by the diff command. Costs
// named Diff* are current minus baseline; a missing side counts as zero.
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
	// Resources holds only what changed. A project with no changed resources
	// is still listed so the caller can report it as unchanged.
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
	// CostDependsOnUsage is true when no component on either side carries a
	// monthly cost, so the only honest total is "depends on usage".
	CostDependsOnUsage bool `json:"cost_depends_on_usage,omitempty"`
}

type DiffCostComponent struct {
	// Name is "old → new" when the baseline and current names differ but
	// matched on the part before the bracket, e.g. an instance type change.
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
	// CostDependsOnUsage is true when neither side carries a monthly cost, so
	// the price per unit is the only honest number to show.
	CostDependsOnUsage bool `json:"cost_depends_on_usage,omitempty"`
}

// Diff compares a current scan against a baseline. Projects are matched by
// name, resources and subresources by name within their parent, and cost
// components by name with a fallback on the part before " (".
func Diff(past, current *Output) *DiffOutput {
	currency := current.Currency
	if currency == "" {
		currency = past.Currency
	}
	if currency == "" {
		currency = "USD"
	}
	out := &DiffOutput{Currency: currency}

	// Baseline indexes per name, so repeated project names pair up one by one
	// instead of collapsing onto a single entry.
	pastByName := make(map[string][]int, len(past.Projects))
	for i := range past.Projects {
		name := past.Projects[i].ProjectName
		pastByName[name] = append(pastByName[name], i)
	}
	matched := make([]bool, len(past.Projects))

	for i := range current.Projects {
		cur := &current.Projects[i]

		var prev *ProjectOutput
		op := DiffAdded
		if queue := pastByName[cur.ProjectName]; len(queue) > 0 {
			prev = &past.Projects[queue[0]]
			matched[queue[0]] = true
			pastByName[cur.ProjectName] = queue[1:]
			op = DiffChanged
		}
		out.Projects = append(out.Projects, diffProject(op, prev, cur))
	}

	// Anything left in the baseline is gone from the current scan. Ordered by
	// the baseline's own order so the output is stable.
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

// diffResources walks the baseline's resources first so removals keep their
// original position, then appends resources only the current scan has.
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
	d.CostDependsOnUsage = unpriced(past) && unpriced(current)

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

	var out []DiffCostComponent

	for i := range past {
		prev := &past[i]
		if d, changed := diffCostComponent(prev, findMatchingCostComponent(current, matched, prev.Name)); changed {
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
	d.CostDependsOnUsage = (past == nil || past.TotalMonthlyCost == nil) &&
		(current == nil || current.TotalMonthlyCost == nil)

	changed := !d.DiffMonthlyCost.IsZero() || !d.DiffPrice.IsZero() || !d.DiffQuantity.IsZero()
	return d, changed
}

// findMatchingCostComponent matches on the full name, then on everything
// before " (" so a resized instance reads as a change, not an add and remove.
// Only components not already paired are considered, and the bracket fallback
// needs a single candidate so sibling pricing tiers are never paired.
func findMatchingCostComponent(components []CostComponentOutput, matched []bool, name string) *CostComponentOutput {
	for i := range components {
		if !matched[i] && components[i].Name == name {
			matched[i] = true
			return &components[i]
		}
	}

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

// unpriced reports whether a resource has no monthly cost anywhere in its
// tree. A nil resource is unpriced: the other side decides.
func unpriced(r *ResourceOutput) bool {
	if r == nil {
		return true
	}
	for _, c := range r.CostComponents {
		if c.TotalMonthlyCost != nil {
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

func orZero(r *rat.Rat) *rat.Rat {
	if r == nil {
		return rat.Zero
	}
	return r
}
