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

	assertGolden(t, buf.String())
}

func TestSummaryJSON(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteSummary(&buf, data, true)
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"type"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByProjectType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"project", "type"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByProvider(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"provider"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByResource(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"resource"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByFile(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"file"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByProject(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"project"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByPolicy(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"policy"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestGroupByTop(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"type"}, Top: 2})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestMultiGroupByPolicyType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"policy", "type"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestMultiGroupByProviderType(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"provider", "type"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestPolicyDetailFinops(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Use GP3"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestPolicyDetailBySlug(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "use-gp3"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestPolicyDetailTagging(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Required Tags"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestPolicyResourceDetailFinops(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Use GP3", Resource: "aws_ebs_volume.data"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestPolicyResourceDetailTagging(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WritePolicyDetail(&buf, data, Options{Policy: "Required Tags", Resource: "aws_instance.web"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
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

	assertGolden(t, buf.String())
}

func TestGuardrailDetailByID(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGuardrailDetail(&buf, data, Options{Guardrail: "g-1"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
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

	assertGolden(t, buf.String())
}

func TestBudgetDetailOver(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteBudgetDetail(&buf, data, Options{Budget: "Frontend Q2"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestBudgetDetailByID(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteBudgetDetail(&buf, data, Options{Budget: "b-1"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
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

	assertGolden(t, buf.String())
}

func TestGroupByGuardrail(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := WriteGroupBy(&buf, data, Options{GroupBy: []string{"guardrail"}})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestRunDefaultsToSummary(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestResourceWithoutPolicyShowsPolicyFailures(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Resource: "aws_instance.web"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestResourceWithoutPolicyFinops(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Resource: "aws_ebs_volume.data"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestRunPolicyFlagBypassesGroupBy(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Policy: "Use GP3"})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}

func TestRunFailingPanorama(t *testing.T) {
	data := testData()
	var buf bytes.Buffer

	err := Run(&buf, data, Options{Failing: true})
	require.NoError(t, err)

	assertGolden(t, buf.String())
}
