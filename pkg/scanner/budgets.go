package scanner

import (
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/provider"
)

// ResourceCostInfos converts proto resources into pre-computed cost info
// suitable for budget evaluation. It flattens child resources so every
// resource (including children) gets its own entry.
func ResourceCostInfos(resources []*provider.Resource) []goprotoevent.ResourceCostInfo {
	infos := make([]goprotoevent.ResourceCostInfo, 0, len(resources))
	for _, r := range resources {
		infos = append(infos, resourceCostInfos(r)...)
	}
	return infos
}

func resourceCostInfos(r *provider.Resource) []goprotoevent.ResourceCostInfo {
	infos := make([]goprotoevent.ResourceCostInfo, 0, 1+len(r.ChildResources))

	tags := make(map[string]string)
	if r.Tagging != nil {
		for _, t := range r.Tagging.Tags {
			tags[t.Key] = t.Value
		}
	}

	// Compute cost for this resource only (not children — they get their own entries).
	cost := rat.Zero
	if r.Costs != nil {
		for _, c := range r.Costs.Components {
			cost = cost.Add(ComponentMonthlyCost(c))
		}
	}

	infos = append(infos, goprotoevent.ResourceCostInfo{
		Tags:        tags,
		MonthlyCost: cost,
	})

	for _, child := range r.ChildResources {
		infos = append(infos, resourceCostInfos(child)...)
	}

	return infos
}