package format

import (
	"testing"
	"time"

	repoconfig "github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/diagnostic"
	"github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToOutput_BudgetResults(t *testing.T) {
	result := &Result{
		Config: &repoconfig.Config{Currency: "USD"},
		BudgetResults: []event.BudgetResult{
			{
				BudgetID:    "b-1",
				Tags:        []event.BudgetTag{{Key: "env", Value: "production"}},
				StartDate:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				EndDate:     time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
				Amount:      rat.New(1000),
				CurrentCost: rat.New(500),
			},
			{
				BudgetID:             "b-2",
				Tags:                 []event.BudgetTag{{Key: "team", Value: "frontend"}},
				StartDate:            time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
				EndDate:              time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
				Amount:               rat.New(300),
				CurrentCost:          rat.New(400),
				CustomOverrunMessage: "Contact FinOps",
			},
		},
	}

	output := ToOutput(result)

	require.Len(t, output.BudgetResults, 2)

	assert.Equal(t, "b-1", output.BudgetResults[0].BudgetID)
	assert.Equal(t, "env", output.BudgetResults[0].Tags[0].Key)
	assert.Equal(t, "production", output.BudgetResults[0].Tags[0].Value)
	assert.True(t, output.BudgetResults[0].Amount.Equals(rat.New(1000)))
	assert.True(t, output.BudgetResults[0].CurrentCost.Equals(rat.New(500)))
	assert.False(t, output.BudgetResults[0].OverBudget)
	assert.Empty(t, output.BudgetResults[0].CustomOverrunMessage)

	assert.Equal(t, "b-2", output.BudgetResults[1].BudgetID)
	assert.True(t, output.BudgetResults[1].OverBudget)
	assert.Equal(t, "Contact FinOps", output.BudgetResults[1].CustomOverrunMessage)
}

func TestToOutput_NoBudgets(t *testing.T) {
	result := &Result{
		Config: &repoconfig.Config{Currency: "USD"},
	}

	output := ToOutput(result)
	assert.Empty(t, output.BudgetResults)
}

func TestComputeSummary(t *testing.T) {
	// Hand-built Output exercising every counter the summary block tracks:
	// 2 projects, mix of free vs costed resources, finops + tagging
	// failures with overlap (two policies hitting the same resource), and
	// guardrails / budgets in both states.
	out := &Output{
		Currency: "USD",
		Projects: []ProjectOutput{
			{
				ProjectName: "web",
				Resources: []ResourceOutput{
					{
						Name: "aws_instance.web",
						CostComponents: []CostComponentOutput{
							{Name: "Instance usage", TotalMonthlyCost: rat.New(100)},
						},
					},
					{
						Name:   "aws_s3_bucket.logs",
						IsFree: true,
					},
					{
						Name: "aws_ebs_volume.data",
						CostComponents: []CostComponentOutput{
							{Name: "Storage", TotalMonthlyCost: rat.New(20)},
						},
						Subresources: []ResourceOutput{
							{
								Name: "aws_ebs_volume.data.snapshot",
								CostComponents: []CostComponentOutput{
									{Name: "Snapshot storage", TotalMonthlyCost: rat.New(5)},
								},
							},
						},
					},
				},
				FinopsResults: []FinopsOutput{
					{
						PolicyName: "Use GP3",
						FailingResources: []FinopsFailingResourceOutput{
							{
								Name: "aws_ebs_volume.data",
								Issues: []FinopsIssueOutput{
									{Description: "Use GP3", MonthlySavings: rat.New(5)},
									{Description: "Smaller IOPS", MonthlySavings: rat.New(3)},
								},
							},
						},
					},
					{PolicyName: "Use Graviton"}, // present but no failing resources
				},
				TaggingResults: taggingPolicyFixture("Required Tags", "aws_instance.web", "aws_ebs_volume.data"),
			},
			{
				ProjectName: "api",
				Resources: []ResourceOutput{
					{
						Name: "aws_lambda_function.handler",
						CostComponents: []CostComponentOutput{
							{Name: "Requests", TotalMonthlyCost: rat.New(2)},
						},
					},
				},
				FinopsResults: []FinopsOutput{
					{
						PolicyName: "Use GP3", // same policy name as in `web` project
						FailingResources: []FinopsFailingResourceOutput{
							{
								Name:   "aws_ebs_volume.data", // same address as in `web` — shouldn't double-count distinct
								Issues: []FinopsIssueOutput{{Description: "Use GP3", MonthlySavings: rat.New(2)}},
							},
						},
					},
				},
			},
		},
		GuardrailResults: []GuardrailOutput{
			{GuardrailName: "g1", Triggered: true},
			{GuardrailName: "g2", Triggered: false},
			{GuardrailName: "g3", Triggered: true},
		},
		BudgetResults: []BudgetOutput{
			{BudgetName: "b1", OverBudget: false},
			{BudgetName: "b2", OverBudget: true},
		},
	}

	s := computeSummary(out)
	require.NotNil(t, s)

	assert.Equal(t, 2, s.Projects, "two projects")
	assert.Equal(t, 4, s.Resources, "four top-level resources across projects (subresources don't add to the count)")
	assert.Equal(t, 3, s.CostedResources, "three resources have non-empty cost components")
	assert.Equal(t, 1, s.FreeResources, "one resource is marked free")

	// 100 (web) + 20 + 5 (subresource) + 2 (api) = 127
	assert.True(t, s.TotalMonthlyCost.Equals(rat.New(127)),
		"total monthly cost should sum cost components and recurse into subresources; got %s", s.TotalMonthlyCost.String())

	// FinOps: 2 policies declared (Use GP3, Use Graviton), 1 of them has failures
	// (Use GP3 in `web`); the same name in `api` shouldn't merge — we count
	// per-project policy entries, not deduped names. Behavior matches the prior
	// summaryData implementation.
	assert.Equal(t, 3, s.FinopsPolicies, "3 FinopsResults entries across the two projects")
	assert.Equal(t, 2, s.FailingFinopsPolicies, "two FinopsResults entries have failing resources")
	assert.Equal(t, 2, s.DistinctFailingFinopsResources, "the same address fails in both projects → counts once per project")

	// Total potential savings = 5 + 3 + 2 = 10
	assert.True(t, s.TotalPotentialMonthlySavings.Equals(rat.New(10)),
		"sum of monthly_savings across all FinOps issues; got %s", s.TotalPotentialMonthlySavings.String())

	// Tagging: helper added 1 policy with 2 failing resources in `web`.
	assert.Equal(t, 1, s.TaggingPolicies)
	assert.Equal(t, 1, s.FailingTaggingPolicies)
	assert.Equal(t, 2, s.DistinctFailingTaggingResources)

	assert.Equal(t, 3, s.Guardrails)
	assert.Equal(t, 2, s.TriggeredGuardrails)
	assert.Equal(t, 2, s.Budgets)
	assert.Equal(t, 1, s.OverBudget)
}

func TestComputeSummaryEmpty(t *testing.T) {
	s := computeSummary(&Output{Currency: "USD"})
	require.NotNil(t, s)

	assert.Equal(t, 0, s.Projects)
	assert.Equal(t, 0, s.Resources)
	assert.True(t, s.TotalMonthlyCost.IsZero(), "total cost on empty output is zero")
	assert.Nil(t, s.TotalPotentialMonthlySavings, "no savings → field omitted from summary")
	assert.Equal(t, 0, s.FailingFinopsPolicies)
	assert.Equal(t, 0, s.FailingTaggingPolicies)
}

// taggingPolicyFixture builds a single tagging policy with the given failing
// resource addresses. Inline helper to keep the TestComputeSummary fixture
// readable; not exported.
func taggingPolicyFixture(name string, addresses ...string) []TaggingOutput {
	failing := make([]FailingTaggingResourceOutput, 0, len(addresses))
	for _, a := range addresses {
		failing = append(failing, FailingTaggingResourceOutput{Address: a, MissingMandatoryTags: []string{"team"}})
	}
	return []TaggingOutput{{PolicyName: name, FailingResources: failing}}
}

func TestConvertDiagnostic(t *testing.T) {
	t.Run("classified critical type with friendly prefix", func(t *testing.T) {
		d := diagnostic.FromError(parser.DiagnosticType_DIAGNOSTIC_TYPE_HCL_PARSE_ERROR, assert.AnError).
			WithSourceRange(&parser.SourceRange{Filename: "main.tf", StartLine: 42, EndLine: 42})

		got := convertDiagnostic(d)

		assert.Equal(t, "HCL parse error", got.Prefix)
		assert.Equal(t, "critical", got.Severity)
		assert.Equal(t, "main.tf:42", got.Location)
	})

	t.Run("classified warning type with friendly prefix", func(t *testing.T) {
		d := diagnostic.FromError(parser.DiagnosticType_DIAGNOSTIC_TYPE_MISSING_INPUT_VARIABLE, assert.AnError)

		got := convertDiagnostic(d)

		assert.Equal(t, "Missing input variable", got.Prefix)
		assert.Equal(t, "warning", got.Severity)
		assert.Empty(t, got.Location)
	})

	t.Run("unclassified type falls back to severity-derived prefix", func(t *testing.T) {
		// FAILED_FUNCTION_CALL and MISSING_REFERENCE are unmapped in go-proto —
		// they exit FromError as info-severity (not critical, not warning) and
		// without a friendly prefix. The fallback must label them "Info", not
		// "Error", so they don't read as criticals in the inspect view.
		d := diagnostic.FromError(parser.DiagnosticType_DIAGNOSTIC_TYPE_FAILED_FUNCTION_CALL, assert.AnError)

		got := convertDiagnostic(d)

		assert.Equal(t, "Info", got.Prefix)
		assert.Equal(t, "info", got.Severity)
	})

	t.Run("source range spanning multiple lines uses start-end format", func(t *testing.T) {
		d := diagnostic.FromError(parser.DiagnosticType_DIAGNOSTIC_TYPE_HCL_PARSE_ERROR, assert.AnError).
			WithSourceRange(&parser.SourceRange{Filename: "main.tf", StartLine: 10, EndLine: 14})

		got := convertDiagnostic(d)

		assert.Equal(t, "main.tf:10-14", got.Location)
	})

	t.Run("remote module url is preserved verbatim", func(t *testing.T) {
		d := diagnostic.FromError(parser.DiagnosticType_DIAGNOSTIC_TYPE_HCL_PARSE_ERROR, assert.AnError).
			WithSourceRange(&parser.SourceRange{Filename: "git::https://example.com/mod.git//main.tf"})

		got := convertDiagnostic(d)

		assert.Equal(t, "git::https://example.com/mod.git//main.tf", got.Location)
	})
}
