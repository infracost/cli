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
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web-app",
				Path:        "/web",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_instance.web",
						Type: "aws_instance",
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
						IsFree: true,
					},
					{
						Name: "google_compute_instance.api",
						Type: "google_compute_instance",
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
	assert.Len(t, filtered.Projects[0].Resources, 2, "should include aws_instance and aws_s3_bucket")
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
	assert.Contains(t, output, "Resources: 4")
	assert.Contains(t, output, "3 costed, 1 free")
	assert.Contains(t, output, "$55.00")
	assert.Contains(t, output, "FinOps policies: 1")
	assert.Contains(t, output, "1 failing")
	assert.Contains(t, output, "1 critical")
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
