package scanner

import (
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
)

// EvaluateGuardrails evaluates guardrail configs against the base and head scan results,
// matching projects by name to compute per-project cost diffs.
func EvaluateGuardrails(guardrails []*event.Guardrail, baseProjects, headProjects []ProjectResult) []goprotoevent.GuardrailResult {
	projectCostMap := make(map[string]goprotoevent.ProjectCostInfo)
	headTotal := rat.Zero
	baseTotal := rat.Zero

	for _, p := range headProjects {
		cost := p.TotalMonthlyCost
		if cost == nil {
			cost = rat.Zero
		}
		headTotal = headTotal.Add(cost)
		projectCostMap[p.Name] = goprotoevent.ProjectCostInfo{
			ProjectName:          p.Name,
			PastTotalMonthlyCost: rat.Zero,
			TotalMonthlyCost:     cost,
		}
	}

	for _, p := range baseProjects {
		cost := p.TotalMonthlyCost
		if cost == nil {
			cost = rat.Zero
		}
		baseTotal = baseTotal.Add(cost)
		if existing, exists := projectCostMap[p.Name]; exists {
			existing.PastTotalMonthlyCost = cost
			projectCostMap[p.Name] = existing
			continue
		}
		projectCostMap[p.Name] = goprotoevent.ProjectCostInfo{
			ProjectName:          p.Name,
			PastTotalMonthlyCost: cost,
			TotalMonthlyCost:     rat.Zero,
		}
	}

	projectCosts := make([]goprotoevent.ProjectCostInfo, 0, len(projectCostMap))
	for _, info := range projectCostMap {
		projectCosts = append(projectCosts, info)
	}

	return goprotoevent.Guardrails(guardrails).Evaluate(headTotal, baseTotal, projectCosts)
}