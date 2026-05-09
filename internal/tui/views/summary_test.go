package views

import (
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/stretchr/testify/assert"
)

// minimalOutput builds a multi-project Output with enough policy
// content to exercise issue counts, and a guardrail/budget so we can
// test the scan-level fields too.
func minimalOutput() *format.Output {
	return &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_instance.a",
						Type: "aws_instance",
						CostComponents: []format.CostComponentOutput{
							{Name: "compute", TotalMonthlyCost: parseRat("100")},
						},
					},
					{
						Name: "aws_kms_key.free",
						Type: "aws_kms_key", // free
					},
				},
				FinopsResults: []format.FinopsOutput{
					{
						PolicyName: "Use spot",
						FailingResources: []format.FinopsFailingResourceOutput{
							{
								Name: "aws_instance.a",
								Issues: []format.FinopsIssueOutput{
									{Description: "switch to spot"},
									{Description: "smaller instance"},
								},
							},
						},
					},
				},
			},
			{
				ProjectName: "api",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_lambda.b",
						Type: "aws_lambda",
						CostComponents: []format.CostComponentOutput{
							{Name: "requests", TotalMonthlyCost: parseRat("2")},
						},
					},
				},
				TaggingResults: []format.TaggingOutput{
					{
						PolicyName: "Mandatory tags",
						FailingResources: []format.FailingTaggingResourceOutput{
							{
								Address:              "aws_lambda.b",
								MissingMandatoryTags: []string{"env", "owner"},
							},
						},
					},
				},
			},
		},
		GuardrailResults: []format.GuardrailOutput{
			{GuardrailName: "monthly_cap", Triggered: true},
			{GuardrailName: "growth_check"},
		},
		BudgetResults: []format.BudgetOutput{
			{
				BudgetName: "core_team",
				OverBudget: true,
			},
		},
	}
}

func TestComputeStats_Aggregate(t *testing.T) {
	s := computeStats(minimalOutput(), "")

	assert.Equal(t, 2, s.Projects)
	assert.Equal(t, 3, s.Resources, "3 resources across both projects")
	assert.Equal(t, 2, s.CostedResources, "instance + lambda are costed")
	assert.Equal(t, 1, s.FreeResources, "kms key has no cost components")

	// Distinct failing resources, deduped within each project: instance.a
	// fails 1 FinOps policy; lambda.b fails 1 tagging policy. Each
	// resource counts once even if it'd fail multiple policies.
	assert.Equal(t, 1, s.DistinctFailingFinopsResources)
	assert.Equal(t, 1, s.DistinctFailingTaggingResources)

	// Policy totals — sum across projects (one FinOps policy in `web`,
	// one tagging policy in `api`).
	assert.Equal(t, 1, s.FinopsPolicies)
	assert.Equal(t, 1, s.TaggingPolicies)

	// Scan-level: guardrail and budget tallies come from out.Guardrail
	// Results / out.BudgetResults, not the projects.
	assert.Equal(t, 2, s.Guardrails)
	assert.Equal(t, 1, s.TriggeredGuardrails)
	assert.Equal(t, 1, s.Budgets)
	assert.Equal(t, 1, s.OverBudget)
}

func TestComputeStats_PerProjectScopesCorrectly(t *testing.T) {
	out := minimalOutput()

	web := computeStats(out, "web")
	api := computeStats(out, "api")

	// Per-project stats split cleanly: each project's resources
	// belong to it.
	assert.Equal(t, 2, web.Resources)
	assert.Equal(t, 1, api.Resources)

	// Per-project FinOps/tagging tallies follow the project's own
	// policy results — web has the FinOps failure, api has the
	// tagging failure, and they don't bleed into each other.
	assert.Equal(t, 1, web.DistinctFailingFinopsResources)
	assert.Equal(t, 0, web.DistinctFailingTaggingResources)
	assert.Equal(t, 0, api.DistinctFailingFinopsResources)
	assert.Equal(t, 1, api.DistinctFailingTaggingResources)

	// Guardrails and budgets stay scan-level — the per-project view
	// deliberately omits them since they apply across the whole scan.
	assert.Equal(t, 0, web.Guardrails)
	assert.Equal(t, 0, web.Budgets)
	assert.Equal(t, 0, api.Guardrails)
	assert.Equal(t, 0, api.Budgets)
}

func TestComputeStats_PerProjectAggregatesSumToWhole(t *testing.T) {
	// Project-scoped resource and policy stats should sum to the
	// aggregate. This is a property the user implicitly relies on:
	// tabbing through projects shouldn't reveal more (or fewer)
	// failing resources than the "all projects" summary advertises.
	out := minimalOutput()
	whole := computeStats(out, "")

	var sumResources, sumCosted, sumFree, sumFinops, sumTagging int
	for _, p := range out.Projects {
		s := computeStats(out, p.ProjectName)
		sumResources += s.Resources
		sumCosted += s.CostedResources
		sumFree += s.FreeResources
		sumFinops += s.DistinctFailingFinopsResources
		sumTagging += s.DistinctFailingTaggingResources
	}

	assert.Equal(t, whole.Resources, sumResources)
	assert.Equal(t, whole.CostedResources, sumCosted)
	assert.Equal(t, whole.FreeResources, sumFree)
	assert.Equal(t, whole.DistinctFailingFinopsResources, sumFinops)
	assert.Equal(t, whole.DistinctFailingTaggingResources, sumTagging)
}

func TestComputeStats_UnknownProjectReturnsEmpty(t *testing.T) {
	s := computeStats(minimalOutput(), "does-not-exist")

	// Returning an empty stats block (rather than panicking or
	// erroring) keeps the picker → main transition forgiving when
	// project names happen to not match.
	assert.Equal(t, 0, s.Resources)
	assert.Equal(t, 0, s.Projects)
}
