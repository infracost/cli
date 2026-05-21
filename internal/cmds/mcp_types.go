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

// MCPPriceOutput is the wire shape returned by the MCP `price` tool. Unlike
// MCPScanOutput, it carries the per-resource breakdown directly because the
// agent has shipped a small piece of IaC and almost always wants to know
// what's expensive about *those resources* — making them call a separate
// inspect tool to drill back into their own input would be a needless
// round-trip. For whole-repo scans the cost of that detail would be
// prohibitive, so the scan tool keeps its lean summary-only shape.
type MCPPriceOutput struct {
	Currency  string              `json:"currency"`
	Summary   inspect.SummaryView `json:"summary"`
	Resources []MCPResource       `json:"resources"`
}

// MCPResource is the flat, schema-friendly version of [format.ResourceOutput]
// used by the MCP `price` tool. Subresources are pre-summed into
// TotalMonthlyCost rather than nested — that gets rid of the
// jsonschema-go-unsupported cycle in ResourceOutput and matches the way no
// human-facing renderer ever lists subresources individually (they only
// contribute to totals).
type MCPResource struct {
	Name                string                       `json:"name"`
	Type                string                       `json:"type"`
	IsSupported         bool                         `json:"is_supported"`
	IsFree              bool                         `json:"is_free"`
	TotalMonthlyCost    *rat.Rat                     `json:"total_monthly_cost,omitempty"`
	CostComponents      []format.CostComponentOutput `json:"cost_components,omitempty"`
	Tags                map[string]string            `json:"tags,omitempty"`
	SupportsTags        bool                         `json:"supports_tags,omitempty"`
	SupportsDefaultTags bool                         `json:"supports_default_tags,omitempty"`
	Metadata            format.ResourceMetadata      `json:"metadata"`
}

// toMCPPriceOutput projects a *format.Output to the MCP price tool's wire
// shape. The summary view mirrors what the human renderer prints; the flat
// Resources slice walks every project's resources and pre-sums their
// subresource costs so the agent gets one row per top-level resource with a
// TotalMonthlyCost that already accounts for any nested LaunchTemplate /
// EBS volume / etc. children produced by the plugin layer.
func toMCPPriceOutput(o *format.Output) MCPPriceOutput {
	out := MCPPriceOutput{
		Currency: o.Currency,
		Summary:  inspect.BuildSummaryView(o),
	}
	for _, p := range o.Projects {
		for _, r := range p.Resources {
			out.Resources = append(out.Resources, toMCPResource(r))
		}
	}
	return out
}

// toMCPResource flattens a single resource. The TotalMonthlyCost field rolls
// up cost components plus all nested subresources via inspect.ResourceCost so
// the wire shape loses the hierarchy but not the totals.
func toMCPResource(r format.ResourceOutput) MCPResource {
	return MCPResource{
		Name:                r.Name,
		Type:                r.Type,
		IsSupported:         r.IsSupported,
		IsFree:              r.IsFree,
		TotalMonthlyCost:    inspect.ResourceCost(&r),
		CostComponents:      r.CostComponents,
		Tags:                r.Tags,
		SupportsTags:        r.SupportsTags,
		SupportsDefaultTags: r.SupportsDefaultTags,
		Metadata:            r.Metadata,
	}
}

// priceToolOutputSchema returns the JSON schema describing [MCPPriceOutput].
// Same rat.Rat -> string override as scanToolOutputSchema; MCPResource and
// CostComponentOutput both carry monetary fields that need the same
// treatment.
func priceToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[MCPPriceOutput](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building price tool output schema: %w", err)
	}
	return schema, nil
}

// policiesToolOutputSchema returns the JSON schema describing
// [PoliciesResult]. PoliciesResult is built from clean Go-defined types
// (FinopsPolicyEntry / TaggingPolicyEntry / PolicyStringFilter / …) with
// no proto embeds or rat.Rat fields, so no per-type overrides are needed
// — but the helper exists for symmetry with the scan and price schemas
// and so the registration call site looks the same.
func policiesToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[PoliciesResult](nil)
	if err != nil {
		return nil, fmt.Errorf("building policies tool output schema: %w", err)
	}
	return schema, nil
}

// budgetsToolOutputSchema returns the JSON schema describing
// [BudgetsResult]. Same rat.Rat -> string override as scan/price —
// BudgetEntry.Amount and BudgetEntry.CurrentCost serialize as
// number-formatted strings via rat.Rat.MarshalJSON.
func budgetsToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[BudgetsResult](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building budgets tool output schema: %w", err)
	}
	return schema, nil
}

// guardrailsToolOutputSchema returns the JSON schema describing
// [GuardrailsResult]. GuardrailEntry has three rat.Rat fields
// (TotalThreshold, IncreaseThreshold, IncreasePercentThreshold) that need
// the rat.Rat -> string override so the schema matches MarshalJSON.
func guardrailsToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[GuardrailsResult](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building guardrails tool output schema: %w", err)
	}
	return schema, nil
}