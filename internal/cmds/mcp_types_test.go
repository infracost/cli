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