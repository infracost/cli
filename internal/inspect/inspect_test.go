package inspect

import (
	"bytes"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testData() *format.Output {
	return &format.Output{
		Currency: "USD",
		GuardrailResults: []format.GuardrailOutput{
			{
				GuardrailID:      "g-1",
				GuardrailName:    "Cost increase > $100",
				Triggered:        true,
				TotalMonthlyCost: rat.New(500),
			},
		},
		BudgetResults: []format.BudgetOutput{
			{
				BudgetID:    "b-1",
				BudgetName:  "Production budget",
				Tags:        []format.BudgetTagOutput{{Key: "env", Value: "production"}},
				Amount:      rat.New(1000),
				CurrentCost: rat.New(500),
				OverBudget:  false,
			},
			{
				BudgetID:             "b-2",
				BudgetName:           "Frontend Q2",
				Tags:                 []format.BudgetTagOutput{{Key: "team", Value: "frontend"}},
				Amount:               rat.New(300),
				CurrentCost:          rat.New(400),
				OverBudget:           true,
				CustomOverrunMessage: "Notify #frontend-costs",
			},
		},
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web-app",
				Path:        "/web",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_instance.web",
						Type: "aws_instance",
						Tags: map[string]string{"env": "production"},
						CostComponents: []format.CostComponentOutput{
							{Name: "Instance usage", TotalMonthlyCost: rat.New(20)},
						},
						Metadata: format.ResourceMetadata{
							Filename:  "modules/compute/main.tf",
							StartLine: 40,
							EndLine:   55,
						},
					},
					{
						Name:   "aws_s3_bucket.logs",
						Type:   "aws_s3_bucket",
						Tags:   map[string]string{"env": "production"},
						IsFree: true,
					},
					{
						Name: "aws_ebs_volume.data",
						Type: "aws_ebs_volume",
						Tags: map[string]string{"env": "production"},
						CostComponents: []format.CostComponentOutput{
							{Name: "Storage", TotalMonthlyCost: rat.New(10)},
						},
					},
					{
						Name: "google_compute_instance.api",
						Type: "google_compute_instance",
						Tags: map[string]string{"env": "production", "team": "frontend"},
						CostComponents: []format.CostComponentOutput{
							{Name: "Instance usage", TotalMonthlyCost: rat.New(30)},
						},
					},
				},
				FinopsResults: []format.FinopsOutput{
					{
						PolicyName:    "Use GP3",
						PolicySlug:    "use-gp3",
						PolicyMessage: "Consider using GP3 volumes",
						FailingResources: []format.FinopsFailingResourceOutput{
							{
								Name: "aws_ebs_volume.data",
								Issues: []format.FinopsIssueOutput{
									{
										Description:    "Volume type is gp2",
										MonthlySavings: rat.New(5),
										Address:        "aws_ebs_volume.data",
										Attribute:      "type",
									},
								},
							},
						},
					},
				},
				TaggingResults: []format.TaggingOutput{
					{
						PolicyName: "Required Tags",
						Message:    "All resources must have required tags",
						FailingResources: []format.FailingTaggingResourceOutput{
							{
								Address:              "aws_instance.web",
								ResourceType:         "aws_instance",
								Path:                 "modules/compute/main.tf",
								Line:                 42,
								MissingMandatoryTags: []string{"environment", "team"},
								InvalidTags: []format.InvalidTagOutput{
									{
										Key:        "owner",
										Value:      "foo",
										ValidRegex: "^team-.*",
									},
								},
							},
						},
					},
				},
				Diagnostics: []format.DiagnosticOutput{
					{Message: "something critical", Severity: "critical"},
				},
			},
			{
				ProjectName: "api-service",
				Path:        "/api",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_lambda_function.handler",
						Type: "aws_lambda_function",
						CostComponents: []format.CostComponentOutput{
							{Name: "Requests", TotalMonthlyCost: rat.New(5)},
						},
					},
				},
			},
		},
	}
}

func TestFilterByProvider(t *testing.T) {
	data := testData()
	filtered := Filter(data, Options{Provider: "aws"})

	assert.Len(t, filtered.Projects, 2)
	assert.Len(t, filtered.Projects[0].Resources, 3, "should include aws_instance, aws_s3_bucket, and aws_ebs_volume")
	assert.Len(t, filtered.Projects[1].Resources, 1, "should include aws_lambda_function")
}

func TestFilterByProject(t *testing.T) {
	data := testData()
	filtered := Filter(data, Options{Project: "web-app"})

	assert.Len(t, filtered.Projects, 1)
	assert.Equal(t, "web-app", filtered.Projects[0].ProjectName)
}

func TestFilterCostsOnly(t *testing.T) {
	data := testData()
	filtered := Filter(data, Options{CostsOnly: true})

	for _, p := range filtered.Projects {
		for _, r := range p.Resources {
			assert.False(t, r.IsFree, "free resources should be filtered out")
		}
	}
}

func TestSummary(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteSummary(&buf, data, false)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Projects: 2")
	assert.Contains(t, output, "1 with errors")
	assert.Contains(t, output, "web-app")
	assert.Contains(t, output, "api-service")
	assert.Contains(t, output, "Resources: 5")
	assert.Contains(t, output, "4 costed, 1 free")
	assert.Contains(t, output, "$65.00")
	assert.Contains(t, output, "FinOps policies: 1")
	assert.Contains(t, output, "1 failing")
	assert.Contains(t, output, "1 critical")
	assert.Contains(t, output, "Guardrails: 1 (1 triggered)")
	assert.Contains(t, output, "Budgets: 2 (1 over)")
}

func TestSummaryJSON(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteSummary(&buf, data, true)
	require.NoError(t, err)

	assert.Contains(t, buf.String(), `"projects": 2`)
}

func TestGroupByType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"type"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Type")
	assert.Contains(t, output, "Count")
	assert.Contains(t, output, "Monthly Cost")
	assert.Contains(t, output, "google_compute_instance")
	assert.Contains(t, output, "aws_instance")
	assert.Contains(t, output, "aws_lambda_function")
}

func TestGroupByProjectType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"project", "type"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Project")
	assert.Contains(t, output, "Type")
	assert.Contains(t, output, "web-app")
	assert.Contains(t, output, "api-service")
	assert.Contains(t, output, "google_compute_instance")
}

func TestGroupByProvider(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"provider"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "aws")
	assert.Contains(t, output, "google")
}

func TestGroupByPolicy(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"policy"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Use GP3")
	assert.Contains(t, output, "Message")
	assert.Contains(t, output, "Consider using GP3 volumes")
	assert.Contains(t, output, "All resources must have required tags")
}

func TestGroupByTop(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"type"}, Top: 2})
	require.NoError(t, err)

	// Should have header + separator + 2 rows
	output := buf.String()
	assert.Contains(t, output, "google_compute_instance")
	assert.Contains(t, output, "aws_instance")
}

func TestMultiGroupByPolicyType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"policy", "type"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy")
	assert.Contains(t, output, "Type")
	assert.Contains(t, output, "Kind")
	assert.Contains(t, output, "Resource")
	assert.Contains(t, output, "File")
	assert.Contains(t, output, "Use GP3")
	assert.Contains(t, output, "finops")
	assert.Contains(t, output, "aws_ebs_volume")
	assert.Contains(t, output, "aws_ebs_volume.data")
	assert.Contains(t, output, "Required Tags")
	assert.Contains(t, output, "tagging")
	assert.Contains(t, output, "aws_instance")
	assert.Contains(t, output, "aws_instance.web")
	assert.Contains(t, output, "modules/compute/main.tf:42")
}

func TestMultiGroupByProviderType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"provider", "type"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Provider")
	assert.Contains(t, output, "Type")
	assert.Contains(t, output, "Monthly Cost")
	assert.Contains(t, output, "aws")
	assert.Contains(t, output, "google")
}

func TestPolicyDetailFinops(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Use GP3"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy: Use GP3")
	assert.Contains(t, output, "Consider using GP3 volumes")
	assert.Contains(t, output, "aws_ebs_volume.data")
	assert.Contains(t, output, "File")
	assert.Contains(t, output, "1 issue")
}

func TestPolicyDetailBySlug(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "use-gp3"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy: Use GP3")
}

func TestPolicyDetailTagging(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Required Tags"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy: Required Tags")
	assert.Contains(t, output, "aws_instance.web")
	assert.Contains(t, output, "File")
	assert.Contains(t, output, "modules/compute/main.tf:42")
	assert.Contains(t, output, "3 issues")
}

func TestPolicyResourceDetailFinops(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Use GP3", Resource: "aws_ebs_volume.data"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy: Use GP3")
	assert.Contains(t, output, "Resource: aws_ebs_volume.data")
	assert.Contains(t, output, "Issue: Volume type is gp2")
	assert.Contains(t, output, "Savings: $5.00/mo")
	assert.Contains(t, output, "Attribute: type")
}

func TestPolicyResourceDetailTagging(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Required Tags", Resource: "aws_instance.web"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy: Required Tags")
	assert.Contains(t, output, "Resource: aws_instance.web")
	assert.Contains(t, output, "File: modules/compute/main.tf:42")
	assert.Contains(t, output, "Missing mandatory tags: environment, team")
	assert.Contains(t, output, `Invalid tag "owner"`)
}

func TestPolicyNotFound(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "nonexistent"})
	assert.EqualError(t, err, `policy "nonexistent" not found`)
}

func TestPolicyResourceNotFound(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Use GP3", Resource: "nonexistent"})
	assert.EqualError(t, err, `resource "nonexistent" not found for policy "Use GP3"`)
}

func TestResourceCostWithSubresources(t *testing.T) {
	r := format.ResourceOutput{
		CostComponents: []format.CostComponentOutput{
			{TotalMonthlyCost: rat.New(10)},
		},
		Subresources: []format.ResourceOutput{
			{
				CostComponents: []format.CostComponentOutput{
					{TotalMonthlyCost: rat.New(5)},
				},
			},
		},
	}

	cost := ResourceCost(&r)
	assert.True(t, cost.Equals(rat.New(15)))
}

func TestInferProvider(t *testing.T) {
	tests := []struct {
		resourceType string
		want         string
	}{
		{"aws_instance", "aws"},
		{"google_compute_instance", "google"},
		{"azurerm_virtual_machine", "azurerm"},
		{"unknown_thing", "other"},
	}

	for _, tt := range tests {
		t.Run(tt.resourceType, func(t *testing.T) {
			assert.Equal(t, tt.want, InferProvider(tt.resourceType))
		})
	}
}

func TestResourceTypeFromAddress(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"aws_instance.web", "aws_instance"},
		{"module.vpc.aws_subnet.public", "aws_subnet"},
		{"module.a.module.b.google_compute_instance.api", "google_compute_instance"},
		{"bare", "bare"},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			assert.Equal(t, tt.want, resourceTypeFromAddress(tt.addr))
		})
	}
}

func TestGuardrailDetail(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGuardrailDetail(&buf, data, Options{Guardrail: "Cost increase > $100"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Guardrail: Cost increase > $100")
	assert.Contains(t, output, "Total monthly cost: $500.00")
	assert.Contains(t, output, "Status: TRIGGERED")
}

func TestGuardrailDetailByID(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGuardrailDetail(&buf, data, Options{Guardrail: "g-1"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Guardrail: Cost increase > $100")
}

func TestGuardrailNotFound(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGuardrailDetail(&buf, data, Options{Guardrail: "nonexistent"})
	assert.EqualError(t, err, `guardrail "nonexistent" not found`)
}

func TestBudgetDetailUnder(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteBudgetDetail(&buf, data, Options{Budget: "Production budget"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Budget: Production budget")
	assert.Contains(t, output, "Scope: env=production")
	assert.Contains(t, output, "Limit: $1000.00")
	assert.Contains(t, output, "Actual spend: $500.00")
	assert.Contains(t, output, "remaining")
	assert.Contains(t, output, "50.0% left")
	assert.Contains(t, output, "cloud billing data")
	// Resources matching budget tags should be listed.
	assert.Contains(t, output, "Resources in this scan matching budget tags")
	assert.Contains(t, output, "aws_instance")
	// Finops violations on matching resources should be shown.
	assert.Contains(t, output, "FinOps policy violations")
	assert.Contains(t, output, "Use GP3")
}

func TestBudgetDetailOver(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteBudgetDetail(&buf, data, Options{Budget: "Frontend Q2"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Budget: Frontend Q2")
	assert.Contains(t, output, "Scope: team=frontend")
	assert.Contains(t, output, "Limit: $300.00")
	assert.Contains(t, output, "Actual spend: $400.00")
	assert.Contains(t, output, "OVER by $100.00")
	assert.Contains(t, output, "Message: Notify #frontend-costs")
}

func TestBudgetDetailByID(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteBudgetDetail(&buf, data, Options{Budget: "b-1"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Budget: Production budget")
}

func TestBudgetNotFound(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteBudgetDetail(&buf, data, Options{Budget: "nonexistent"})
	assert.EqualError(t, err, `budget "nonexistent" not found`)
}

func TestGroupByBudget(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"budget"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Production budget")
	assert.Contains(t, output, "Frontend Q2")
	assert.Contains(t, output, "OVER")
	assert.Contains(t, output, "under")
	assert.Contains(t, output, "Limit")
	assert.Contains(t, output, "Actual Spend")
}

func TestGroupByGuardrail(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"guardrail"}})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Cost increase > $100")
	assert.Contains(t, output, "TRIGGERED")
	assert.Contains(t, output, "$500.00")
}

func TestRunDefaultsToSummary(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{})
	require.NoError(t, err)

	assert.Contains(t, buf.String(), "Projects:")
}

func TestResourceWithoutPolicyShowsPolicyFailures(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Resource: "aws_instance.web"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Required Tags")
	assert.Contains(t, output, "aws_instance.web")
	assert.Contains(t, output, "Policy")
	assert.NotContains(t, output, "Projects:", "should not fall back to summary")
}

func TestResourceWithoutPolicyFinops(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Resource: "aws_ebs_volume.data"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Use GP3")
	assert.Contains(t, output, "aws_ebs_volume.data")
}

func TestRunPolicyFlagBypassesGroupBy(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Policy: "Use GP3"})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Policy: Use GP3")
}
