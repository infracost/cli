package cmds

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
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

// TestGuardrailsToolOutputSchema is the same canary for GuardrailsResult.
// GuardrailEntry has three rat.Rat threshold fields plus a
// PolicyStringFilter scope filter; both the rat.Rat override and the
// clean-shape projection must hold for the schema to build cleanly.
func TestGuardrailsToolOutputSchema(t *testing.T) {
	schema, err := guardrailsToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectSummaryToolOutputSchema covers [inspect.Summary], which has
// many rat.Rat fields (top-level monthly cost / total monthly savings,
// per-project monthly cost) plus rat.Rat fields embedded in the drill-in
// format.GuardrailOutput / format.BudgetOutput lists. The rat.Rat ->
// string override must cover every nested occurrence for the schema to
// build cleanly.
func TestInspectSummaryToolOutputSchema(t *testing.T) {
	schema, err := inspectSummaryToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectFailingToolOutputSchema covers [inspect.FailingPanorama]
// — much leaner than Summary, but still embeds format.GuardrailOutput
// / format.BudgetOutput so the rat.Rat override remains load-bearing.
func TestInspectFailingToolOutputSchema(t *testing.T) {
	schema, err := inspectFailingToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectDiagnosticsToolOutputSchema verifies the bare envelope
// schema is finite. No rat.Rat to worry about — DiagnosticEntry carries
// text only.
func TestInspectDiagnosticsToolOutputSchema(t *testing.T) {
	schema, err := inspectDiagnosticsToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectResourcesToolOutputSchema covers the unified flat/grouped
// envelope. Resources rows carry a monthly_cost rat.Rat; grouped rows
// carry a Cost rat.Rat. The shared override fans out across both.
func TestInspectResourcesToolOutputSchema(t *testing.T) {
	schema, err := inspectResourcesToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectTopSavingsToolOutputSchema covers [inspect.TopSavingsResult].
// Multiple rat.Rat fields (each item's monthly_savings plus the headline
// total) need the rat.Rat -> string override to produce an honest schema.
func TestInspectTopSavingsToolOutputSchema(t *testing.T) {
	schema, err := inspectTopSavingsToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectPolicyDetailToolOutputSchema covers [inspect.PolicyDetail].
// Carries rat.Rat indirectly via format.FinopsIssueOutput's
// monthly_savings field.
func TestInspectPolicyDetailToolOutputSchema(t *testing.T) {
	schema, err := inspectPolicyDetailToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectBudgetDetailToolOutputSchema covers [inspect.BudgetDetail].
// Has multiple rat.Rat surfaces (amount + current_cost on the embedded
// BudgetOutput, monthly_cost on the matching resources, savings).
func TestInspectBudgetDetailToolOutputSchema(t *testing.T) {
	schema, err := inspectBudgetDetailToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestInspectGuardrailDetailToolOutputSchema covers
// [format.GuardrailOutput] returned by inspect_guardrail_detail.
// rat.Rat appears on the total_monthly_cost field.
func TestInspectGuardrailDetailToolOutputSchema(t *testing.T) {
	schema, err := inspectGuardrailDetailToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestDoctorToolOutputSchema covers [DoctorOutput]. No rat.Rat — the
// doctor report and bundle are text + counts only — but the canary
// still guards against any future doctor type accidentally pulling a
// cyclic or schema-hostile field.
//
// It also pins the regression from FIX-395: doctor.Status is a Go int
// enum with a MarshalJSON that emits its lowercase string name ("pass",
// …). Without the TypeSchemas override, jsonschema-go reflects the
// underlying int kind and declares status as "integer", so MCP output
// validation rejects the string the tool actually returns and the
// doctor tool fails outright. Assert the schema declares status as the
// string enum the wire format carries.
func TestDoctorToolOutputSchema(t *testing.T) {
	schema, err := doctorToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	status := followSchemaPath(t, schema,
		"report", "categories", "items", "results", "items", "status")
	assert.Equal(t, "string", status.Type,
		"doctor status marshals to a string name; schema must not declare it integer")
	assert.ElementsMatch(t,
		[]any{"pass", "warning", "fail", "skipped"}, status.Enum,
		"status enum must match doctor.Status.String() values")
}

func TestFindingsListToolOutputSchema(t *testing.T) {
	schema, err := findingsListToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// Even on list rows, Agents may include `triggerDetail` (it's omitempty
	// on the Go side but the API does emit it on some rows). The schema
	// must accept any JSON value rather than the byte-slice default.
	assertOpenJSONSchema(t, schema, "findings", "items", "triggerDetail")
}

// TestFindingsGetToolOutputSchema asserts the schema's object-ness and
// also pins the open-schema treatment of every Agents field documented as
// `unknown` / `Record<string, unknown>`. Without the rawJSONSchemaOverride
// jsonschema-go infers json.RawMessage as `{"type":["null","array"],
// "items":{"type":"integer","minimum":0,"maximum":255}}` — which rejects
// every real payload the API returns (object, scalar, etc.) and was the
// proximate cause of the validation error this regression test guards.
func TestFindingsGetToolOutputSchema(t *testing.T) {
	schema, err := findingsGetToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	assertOpenJSONSchema(t, schema, "finding", "triggerDetail")
	assertOpenJSONSchema(t, schema, "finding", "tasks", "items", "actionContext")
	assertOpenJSONSchema(t, schema, "finding", "tasks", "items", "events", "items", "detail")
	assertOpenJSONSchema(t, schema, "finding", "actions", "items", "config")
	assertOpenJSONSchema(t, schema, "finding", "actions", "items", "result")
	assertOpenJSONSchema(t, schema, "finding", "events", "items", "detail")
}

func TestPreviewFixToolOutputSchema(t *testing.T) {
	schema, err := previewFixToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// PreviewFixResult.Config is the drafted action blob — open JSON so
	// the agent (and any client) can hand it back to create_fix verbatim.
	assertOpenJSONSchema(t, schema, "config")
}

func TestCreateFixToolOutputSchema(t *testing.T) {
	schema, err := createFixToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// TestCreateFixToolInputSchema is the input-side counterpart. The MCP
// SDK normally infers the input schema by reflection, which would render
// CreateFixInput.ConfigJSON (json.RawMessage) as an integer array —
// making it impossible for an LLM to round-trip the config blob it just
// received from preview_fix. createFixToolInputSchema applies the same
// rawJSONSchemaOverride so the wire shapes match on both ends.
func TestCreateFixToolInputSchema(t *testing.T) {
	schema, err := createFixToolInputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	assertOpenJSONSchema(t, schema, "config")
}

func TestUpdateFindingStatusToolOutputSchema(t *testing.T) {
	schema, err := updateFindingStatusToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// The wrapped Finding carries the same RawMessage fields findings_get
	// does — make sure the override carried through this schema too.
	assertOpenJSONSchema(t, schema, "finding", "triggerDetail")
	assertOpenJSONSchema(t, schema, "finding", "actions", "items", "config")
}

func TestUpdateTaskStatusToolOutputSchema(t *testing.T) {
	schema, err := updateTaskStatusToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)

	// The nested Task carries actionContext + event detail as
	// RawMessage; the open-schema treatment is required for the same
	// reason findings_get needs it.
	assertOpenJSONSchema(t, schema, "task", "actionContext")
	assertOpenJSONSchema(t, schema, "task", "events", "items", "detail")
}

func TestRetryActionToolOutputSchema(t *testing.T) {
	schema, err := retryActionToolOutputSchema()
	require.NoError(t, err)
	require.NotNil(t, schema)
	assert.Equal(t, "object", schema.Type)
}

// followSchemaPath walks the given object-property / "items" path through
// schema and returns the leaf schema, failing the test if any step is
// missing. "items" descends into an array's element schema; any other
// step is looked up as an object property.
func followSchemaPath(t *testing.T, schema *jsonschema.Schema, path ...string) *jsonschema.Schema {
	t.Helper()
	require.NotNil(t, schema, "starting schema is nil")
	cur := schema
	for i, step := range path {
		require.NotNilf(t, cur, "schema is nil at %v", path[:i])
		if step == "items" {
			require.NotNilf(t, cur.Items, "missing items at %v", path[:i+1])
			cur = cur.Items
			continue
		}
		require.NotNilf(t, cur.Properties, "missing properties at %v", path[:i])
		next, ok := cur.Properties[step]
		require.Truef(t, ok, "missing property %q at %v", step, path[:i])
		cur = next
	}
	require.NotNilf(t, cur, "leaf schema is nil at %v", path)
	return cur
}

// assertOpenJSONSchema follows the given object-property / "items" path
// through schema and asserts the leaf is the permissive object schema
// rawJSONSchemaOverride installs — `type: object`, no per-property /
// item constraints. The regression we're guarding against is
// jsonschema-go's default RawMessage inference (a byte-array schema
// with `items.type=integer, minimum=0, maximum=255` and an outer
// `type: ["null", "array"]`), which would reappear as `cur.Items !=
// nil` or `cur.Types != nil`. The leaf is also asserted to marshal as
// a JSON object rather than the boolean `true` an empty schema would
// produce, because MCP hosts reject boolean tool schemas at
// registration time.
func assertOpenJSONSchema(t *testing.T, schema *jsonschema.Schema, path ...string) {
	t.Helper()
	cur := followSchemaPath(t, schema, path...)
	assert.Equalf(t, "object", cur.Type, "expected open object schema at %v, got Type=%q", path, cur.Type)
	assert.Nilf(t, cur.Types, "expected open object schema at %v, got Types=%v", path, cur.Types)
	assert.Nilf(t, cur.Items, "expected open object schema at %v (Items being set means the byte-array default crept back), got Items=%v", path, cur.Items)
	assert.Nilf(t, cur.Properties, "expected open object schema at %v, got Properties=%v", path, cur.Properties)

	encoded, err := json.Marshal(cur)
	require.NoError(t, err)
	assert.Truef(t, len(encoded) > 0 && encoded[0] == '{',
		"expected leaf at %v to marshal as a JSON object so MCP hosts accept it, got %s", path, encoded)
}
