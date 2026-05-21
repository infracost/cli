package cmds

import (
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/go-proto/pkg/rat"
)

// MCPScanOutput is the wire shape returned by the MCP `scan` tool. It mirrors
// the human-rendered scan/price summary box exactly: headline counts, monthly
// cost, policy/guardrail/budget tallies, diagnostic counts, and the
// per-project breakdown — and nothing else. Drill-in detail (which policies
// failed, which guardrails triggered, which budgets are over) is reached by
// calling the per-domain tools (`policies`, `guardrails`, `budgets`,
// `inspect_*`) so the agent only pays for the tokens it needs.
//
// Why a separate type instead of returning *format.Output directly: the
// recursive ResourceOutput.Subresources []ResourceOutput inside *format.Output
// can't be schema'd by jsonschema-go (cycle), and the per-resource detail
// would dwarf the actual summary the human sees. This shape is finite and
// LLM-friendly.
type MCPScanOutput struct {
	Currency string              `json:"currency"`
	Summary  inspect.SummaryView `json:"summary"`
}

// toMCPScanOutput projects a *format.Output to the MCP scan tool's wire
// shape. Currency comes from the Output's top-level field; the summary view
// is rebuilt from the Output rather than read off the pre-computed
// format.OutputSummary so it stays consistent with what `inspect.WriteSummary`
// would render for a human.
func toMCPScanOutput(o *format.Output) MCPScanOutput {
	return MCPScanOutput{
		Currency: o.Currency,
		Summary:  inspect.BuildSummaryView(o),
	}
}

// scanToolOutputSchema returns the JSON schema describing [MCPScanOutput].
// jsonschema-go's reflection-based inference doesn't know about rat.Rat — the
// type has only an unexported *big.Rat field and a MarshalJSON method that
// emits a number-as-string. Without the override the auto-generated schema
// would claim MonthlyCost is an empty object, which lies to schema-aware
// clients. The override maps rat.Rat to the wire shape its MarshalJSON
// actually produces.
func scanToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[MCPScanOutput](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building scan tool output schema: %w", err)
	}
	return schema, nil
}