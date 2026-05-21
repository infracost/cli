package cmds

import (
	"encoding/json"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScanToolOutputSchema is the canary for the recursive-schema problem
// (FIX-150 / MCP_SCHEMA_DECISION): jsonschema-go panics on
// format.ResourceOutput's self-referential Subresources field. MCPScanOutput
// is the dedicated wire shape that sidesteps the cycle by surfacing only the
// human-rendered summary. If a future change accidentally drags
// ResourceOutput (or another cyclic type) back onto MCPScanOutput, this test
// fails at schema build rather than at MCP server startup.
func TestScanToolOutputSchema(t *testing.T) {
	schema, err := scanToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestToMCPScanOutput asserts the converter's wire shape so consumers (the
// agent prompt design, anything that talks to the scan MCP tool) can rely on
// stable JSON field names and a finite, non-recursive structure.
func TestToMCPScanOutput(t *testing.T) {
	out := &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web-app",
				Path:        "/web",
				Resources: []format.ResourceOutput{
					{Name: "aws_instance.web", IsSupported: true, CostComponents: []format.CostComponentOutput{
						{Name: "vCPU", TotalMonthlyCost: rat.New(10)},
					}},
				},
				Diagnostics: []format.DiagnosticOutput{
					{Prefix: "HCL parse error", Severity: "critical"},
				},
			},
		},
	}

	got := toMCPScanOutput(out)

	assert.Equal(t, "USD", got.Currency)
	assert.Equal(t, 1, got.Summary.Projects)
	assert.Equal(t, 1, got.Summary.Resources)
	assert.Equal(t, 1, got.Summary.CriticalDiags)
	assert.Equal(t, 1, got.Summary.ProjectsWithError)
	require.Len(t, got.Summary.ProjectDetails, 1)
	assert.Equal(t, "web-app", got.Summary.ProjectDetails[0].Name)
}

// TestMCPScanOutputJSON locks in the wire format the agent will see — the
// embedded SummaryView fields flatten under "summary", monthly_cost
// serializes as a number-formatted string (matching rat.Rat.MarshalJSON), and
// drill-in lists (failing_policy_list, …) are absent because they live on
// the inspect-only superset.
func TestMCPScanOutputJSON(t *testing.T) {
	out := &format.Output{
		Currency: "EUR",
		Projects: []format.ProjectOutput{{ProjectName: "p", Path: "/p"}},
	}

	body, err := json.Marshal(toMCPScanOutput(out))
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(body, &wire))

	assert.Equal(t, "EUR", wire["currency"])
	summary, ok := wire["summary"].(map[string]any)
	require.True(t, ok, "summary must be an object")
	assert.Contains(t, summary, "projects")
	assert.Contains(t, summary, "monthly_cost")
	assert.NotContains(t, summary, "failing_policy_list", "drill-in detail belongs on per-domain tools")
	assert.NotContains(t, summary, "triggered_guardrail_list", "drill-in detail belongs on per-domain tools")
	assert.NotContains(t, summary, "over_budget_list", "drill-in detail belongs on per-domain tools")
}

// TestPriceToolOutputSchema is the canary for the same recursive-schema
// problem TestScanToolOutputSchema guards against — but for MCPPriceOutput,
// which adds a per-resource breakdown. If MCPResource ever sprouts a
// Subresources field of its own type, this fails before the MCP server tries
// to register the tool.
func TestPriceToolOutputSchema(t *testing.T) {
	schema, err := priceToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestToMCPPriceOutput verifies the per-resource flattening: nested
// subresources contribute to TotalMonthlyCost but disappear from the wire
// shape, so the agent gets one row per top-level resource with the right
// total.
func TestToMCPPriceOutput(t *testing.T) {
	out := &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "stdin",
				Resources: []format.ResourceOutput{
					{
						Name:        "aws_eks_node_group.app",
						Type:        "aws_eks_node_group",
						IsSupported: true,
						CostComponents: []format.CostComponentOutput{
							{Name: "vCPU", TotalMonthlyCost: rat.New(20)},
						},
						Subresources: []format.ResourceOutput{
							{
								Name: "LaunchTemplate",
								CostComponents: []format.CostComponentOutput{
									{Name: "root_block_device", TotalMonthlyCost: rat.New(5)},
								},
								Subresources: []format.ResourceOutput{
									{
										Name: "ebs_block_device[0]",
										CostComponents: []format.CostComponentOutput{
											{Name: "Storage", TotalMonthlyCost: rat.New(3)},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	got := toMCPPriceOutput(out)

	assert.Equal(t, "USD", got.Currency)
	require.Len(t, got.Resources, 1, "subresources flatten into TotalMonthlyCost, not into Resources")
	assert.Equal(t, "aws_eks_node_group.app", got.Resources[0].Name)
	// 20 (vCPU) + 5 (LaunchTemplate.root_block_device) + 3 (EBS storage) = 28.
	assert.Equal(t, "28", got.Resources[0].TotalMonthlyCost.String())
}

// TestMCPPriceOutputJSON locks the price wire format: currency, summary,
// and a flat resources array with no nested subresources.
func TestMCPPriceOutputJSON(t *testing.T) {
	out := &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "stdin",
				Resources: []format.ResourceOutput{
					{Name: "aws_instance.web", Type: "aws_instance", IsSupported: true},
				},
			},
		},
	}

	body, err := json.Marshal(toMCPPriceOutput(out))
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(body, &wire))

	assert.Equal(t, "USD", wire["currency"])
	assert.Contains(t, wire, "summary")
	resources, ok := wire["resources"].([]any)
	require.True(t, ok, "resources must be an array")
	require.Len(t, resources, 1)
	res := resources[0].(map[string]any)
	assert.Equal(t, "aws_instance.web", res["name"])
	assert.NotContains(t, res, "subresources", "MCPResource is flat by design")
}

// TestPoliciesToolOutputSchema is the canary for any future proto-embedded
// type accidentally getting added to PoliciesResult. The clean-shape
// projection in toFinopsPolicyEntry / toTaggingPolicyEntry exists
// specifically to keep the wire schema readable; a regression would either
// break this test or surface raw proto field names to agent consumers.
func TestPoliciesToolOutputSchema(t *testing.T) {
	schema, err := policiesToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestBudgetsToolOutputSchema is the same canary for BudgetsResult.
// BudgetEntry has rat.Rat fields (Amount, CurrentCost) that need the
// rat.Rat -> string override; if that ever goes missing or a proto type
// leaks into BudgetEntry, the schema build will fail here.
func TestBudgetsToolOutputSchema(t *testing.T) {
	schema, err := budgetsToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}