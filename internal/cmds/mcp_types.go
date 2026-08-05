package cmds

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/infracost/cli/internal/doctor"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/go-proto/pkg/rat"
)

// rawJSONSchemaOverride maps json.RawMessage to a permissive
// object-shaped schema — "any JSON object is valid". Several Agents wire
// types use RawMessage for fields the API documents as `unknown` /
// `Record<string, unknown>` (Finding.TriggerDetail, Task.ActionContext,
// Action.Config / Action.Result, FindingEvent.Detail,
// FindingTaskEvent.Detail, plus PreviewFixResult.Config and
// CreateFixInput.ConfigJSON in the CLI's own types). jsonschema-go's
// reflection-based inference doesn't recognize RawMessage as opaque JSON
// — it sees `type RawMessage []byte` and emits
// `{"type":["null","array"],"items":{"type":"integer","minimum":0,"maximum":255}}`,
// which rejects every real payload the API returns.
//
// The override declares `type: object` so:
//   - MCP hosts (which require tool schemas to be objects) accept the
//     declaration at registration time.
//   - LLM tool-use layers know to send a JSON object on the wire rather
//     than serializing the value as a string. Without a declared type
//     they default to string, which is what Agents rejected as
//     `invalid type or config` when create_fix received a stringified
//     config.
//
// All of the RawMessage-typed wire fields in the Agents contract are
// object-shaped in practice (Record<string, unknown> or unknown that's
// always populated as a struct). If a future field actually needs to
// carry an array or scalar, switch it to a typed field or move it to a
// per-field override rather than loosening this map.
func rawJSONSchemaOverride() map[reflect.Type]*jsonschema.Schema {
	return map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[json.RawMessage](): {
			Type:        "object",
			Description: "Opaque JSON object; shape depends on the surrounding action / event type.",
		},
	}
}

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

// findingsListToolOutputSchema returns the JSON schema describing
// [FindingsListResult]. Money is plain float64 so no rat.Rat override is
// needed, but Agents' Finding type carries json.RawMessage fields
// (trigger_detail and the nested Tasks / Actions / Events blobs) — see
// [rawJSONSchemaOverride] for why those need an open schema rather than
// the default byte-slice inference.
func findingsListToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[FindingsListResult](&jsonschema.ForOptions{
		TypeSchemas: rawJSONSchemaOverride(),
	})
	if err != nil {
		return nil, fmt.Errorf("building findings_list tool output schema: %w", err)
	}
	return schema, nil
}

// findingsGetToolOutputSchema returns the JSON schema describing
// [FindingsGetResult]. The Finding's nested Task / Action / Event types
// carry json.RawMessage payloads that the API documents as `unknown` /
// `Record<string, unknown>`; [rawJSONSchemaOverride] keeps the wire shape
// honest by exposing those as open JSON values.
func findingsGetToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[FindingsGetResult](&jsonschema.ForOptions{
		TypeSchemas: rawJSONSchemaOverride(),
	})
	if err != nil {
		return nil, fmt.Errorf("building findings_get tool output schema: %w", err)
	}
	return schema, nil
}

// previewFixToolOutputSchema returns the JSON schema describing
// [PreviewFixResult]. Config is opaque JSON whose concrete shape depends
// on the action type (open_pr / create_ticket); [rawJSONSchemaOverride]
// ensures the blob is exposed as an open value rather than a byte slice.
func previewFixToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[PreviewFixResult](&jsonschema.ForOptions{
		TypeSchemas: rawJSONSchemaOverride(),
	})
	if err != nil {
		return nil, fmt.Errorf("building preview_fix tool output schema: %w", err)
	}
	return schema, nil
}

// createFixToolOutputSchema returns the JSON schema describing
// [CreateFixResult]. Just `action_id` — the action itself can be fetched
// separately if the agent needs more detail.
func createFixToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[CreateFixResult](nil)
	if err != nil {
		return nil, fmt.Errorf("building create_fix tool output schema: %w", err)
	}
	return schema, nil
}

// createFixToolInputSchema returns the JSON schema describing
// [CreateFixInput]. The ConfigJSON field is json.RawMessage — without an
// explicit override, the SDK's reflection-based input inference would
// expose `config` as `[null, array-of-bytes]`, forcing callers to
// hand-encode their JSON object as an integer array. The
// [rawJSONSchemaOverride] keeps the input wire shape aligned with the
// matching field on [PreviewFixResult].
func createFixToolInputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[CreateFixInput](&jsonschema.ForOptions{
		TypeSchemas: rawJSONSchemaOverride(),
	})
	if err != nil {
		return nil, fmt.Errorf("building create_fix tool input schema: %w", err)
	}
	return schema, nil
}

// updateFindingStatusToolOutputSchema returns the JSON schema describing
// [UpdateFindingStatusResult]. The wrapped Finding carries the same
// json.RawMessage fields that [findingsGetToolOutputSchema] handles, so
// the same override applies.
func updateFindingStatusToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[UpdateFindingStatusResult](&jsonschema.ForOptions{
		TypeSchemas: rawJSONSchemaOverride(),
	})
	if err != nil {
		return nil, fmt.Errorf("building update_finding_status tool output schema: %w", err)
	}
	return schema, nil
}

// updateTaskStatusToolOutputSchema returns the JSON schema describing
// [UpdateTaskStatusResult]. The nested Task carries `action_context` and
// event detail blobs as json.RawMessage; the override keeps those open.
func updateTaskStatusToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[UpdateTaskStatusResult](&jsonschema.ForOptions{
		TypeSchemas: rawJSONSchemaOverride(),
	})
	if err != nil {
		return nil, fmt.Errorf("building update_task_status tool output schema: %w", err)
	}
	return schema, nil
}

// retryActionToolOutputSchema returns the JSON schema describing
// [RetryActionResult] — a single boolean. No Agents types involved, so no
// per-type overrides are needed; the helper exists for symmetry with the
// other tool registrations.
func retryActionToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[RetryActionResult](nil)
	if err != nil {
		return nil, fmt.Errorf("building retry_action tool output schema: %w", err)
	}
	return schema, nil
}

// InspectSummaryInput is the input shape for the `inspect_summary` MCP
// tool. Fields mirror the subset of inspect.Options relevant to the
// summary view — narrower than the full inspect surface area so the
// agent's decision space is clear.
type InspectSummaryInput struct {
	Path      string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Project   string `json:"project,omitempty" jsonschema:"Filter to a specific project by name."`
	Provider  string `json:"provider,omitempty" jsonschema:"Filter to a specific provider (aws, azurerm, google)."`
	CostsOnly bool   `json:"costs_only,omitempty" jsonschema:"Exclude free resources from the summary counts and totals."`
	Failing   bool   `json:"failing,omitempty" jsonschema:"Limit the summary to failing / triggered / over-budget items only."`
	Filter    string `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression (e.g. \"tag.team=missing,provider=aws\"). Matches inspect --filter syntax."`
}

// InspectFailingInput is the input shape for the `inspect_failing` MCP
// tool. Same filter set as inspect_summary — both views care about
// scoping the panorama to a project / provider.
type InspectFailingInput struct {
	Path     string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Project  string `json:"project,omitempty" jsonschema:"Filter to a specific project by name."`
	Provider string `json:"provider,omitempty" jsonschema:"Filter to a specific provider (aws, azurerm, google)."`
	Filter   string `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression (e.g. \"tag.team=missing,provider=aws\")."`
}

// InspectDiagnosticsInput is the input shape for the `inspect_diagnostics`
// MCP tool. Default behavior is to include every per-project diagnostic
// (critical, warning, info) — agents drilling into a scan want the full
// picture by default. Pass critical_only=true to filter down.
type InspectDiagnosticsInput struct {
	Path         string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Project      string `json:"project,omitempty" jsonschema:"Filter diagnostics to a specific project by name."`
	CriticalOnly bool   `json:"critical_only,omitempty" jsonschema:"Filter to critical-severity diagnostics only. Default: include warning + info too."`
}

// inspectSummaryToolOutputSchema returns the JSON schema describing
// [inspect.Summary]. The shape carries multiple rat.Rat fields (monthly
// cost on Summary, on each ProjectSummary, total yearly savings) plus
// rat.Rat fields embedded in format.GuardrailOutput / format.BudgetOutput
// inside the drill-in lists — all covered by the same rat.Rat -> string
// override scan / price / budgets / guardrails use.
func inspectSummaryToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[inspect.Summary](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_summary tool output schema: %w", err)
	}
	return schema, nil
}

// inspectFailingToolOutputSchema returns the JSON schema describing
// [inspect.FailingPanorama]. Carries rat.Rat via the embedded
// format.GuardrailOutput / format.BudgetOutput.
func inspectFailingToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[inspect.FailingPanorama](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_failing tool output schema: %w", err)
	}
	return schema, nil
}

// InspectDiagnosticsResult wraps the flat list returned by
// CollectDiagnostics so the MCP wire shape is an object (the SDK requires
// tool outputs to be objects, not bare arrays) and so the count is one
// field-read away.
type InspectDiagnosticsResult struct {
	Count       int                       `json:"count"`
	Diagnostics []inspect.DiagnosticEntry `json:"diagnostics"`
}

// inspectDiagnosticsToolOutputSchema returns the schema for
// InspectDiagnosticsResult. No rat.Rat involved — diagnostics carry text
// only — so no per-type overrides are needed.
func inspectDiagnosticsToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[InspectDiagnosticsResult](nil)
	if err != nil {
		return nil, fmt.Errorf("building inspect_diagnostics tool output schema: %w", err)
	}
	return schema, nil
}

// InspectResourcesInput is the input shape for the unified
// `inspect_resources` MCP tool. The tool runs in one of two modes
// depending on whether group_by is set:
//
//   - Flat mode (group_by empty): returns one row per matching
//     resource (resource-shaped predicates filter the set).
//   - Grouped mode (group_by populated): returns one row per group
//     using the same group-by / top pipeline as `inspect --group-by`.
//
// Both modes share the same Project / Provider / Filter / Resource /
// CostsOnly scoping. Resource-shaped predicates (MissingTag /
// InvalidTag / MinCost / MaxCost) only apply in flat mode; Top only
// applies in grouped mode.
type InspectResourcesInput struct {
	Path       string   `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Project    string   `json:"project,omitempty" jsonschema:"Filter to a specific project by name."`
	Provider   string   `json:"provider,omitempty" jsonschema:"Filter to a specific provider (aws, azurerm, google)."`
	Resource   string   `json:"resource,omitempty" jsonschema:"Filter to a specific resource address."`
	Filter     string   `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression (e.g. \"tag.team=missing,provider=aws\")."`
	CostsOnly  bool     `json:"costs_only,omitempty" jsonschema:"Exclude free resources."`
	MissingTag string   `json:"missing_tag,omitempty" jsonschema:"Flat mode only: limit to resources missing the given tag key."`
	InvalidTag string   `json:"invalid_tag,omitempty" jsonschema:"Flat mode only: limit to resources where the given tag is set but its value is outside the policy's allowed list."`
	MinCost    float64  `json:"min_cost,omitempty" jsonschema:"Flat mode only: limit to resources with monthly cost >= N."`
	MaxCost    float64  `json:"max_cost,omitempty" jsonschema:"Flat mode only: limit to resources with monthly cost <= N."`
	GroupBy    []string `json:"group_by,omitempty" jsonschema:"Switches the tool into grouped mode. Valid values: type, provider, project, file, policy, budget, guardrail. Multiple dimensions are AND'd into a single aggregation key."`
	Top        int      `json:"top,omitempty" jsonschema:"Grouped mode only: limit results to the top N rows by cost (descending)."`
}

// InspectResourcesResult is the wire shape for the unified
// `inspect_resources` tool. Mode discriminates between flat and
// grouped payloads — exactly one of Resources / Groups is populated
// per call. Count is the length of whichever slice the mode names so
// LLMs can read it without inspecting both branches.
type InspectResourcesResult struct {
	Mode      string                `json:"mode"`
	Currency  string                `json:"currency,omitempty"`
	GroupBy   []string              `json:"group_by,omitempty"`
	Resources []inspect.ResourceRow `json:"resources,omitempty"`
	Groups    []inspect.GroupedRow  `json:"groups,omitempty"`
	Count     int                   `json:"count"`
}

// inspectResourcesToolOutputSchema returns the schema for the unified
// inspect_resources output. ResourceRow carries a monthly_cost rat.Rat;
// GroupedRow carries a Cost rat.Rat. The override covers both.
func inspectResourcesToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[InspectResourcesResult](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_resources tool output schema: %w", err)
	}
	return schema, nil
}

// InspectTopSavingsInput is the input shape for the
// `inspect_top_savings` MCP tool. N defaults to 10 in the handler when
// omitted so an agent calling the tool with no arguments still gets a
// useful headline list.
type InspectTopSavingsInput struct {
	Path     string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	N        int    `json:"n,omitempty" jsonschema:"Number of top items to return. Defaults to 10. The total_yearly_savings field on the response always reflects the full filtered scan, regardless of n."`
	Project  string `json:"project,omitempty" jsonschema:"Filter to a specific project by name."`
	Provider string `json:"provider,omitempty" jsonschema:"Filter to a specific provider (aws, azurerm, google)."`
	Filter   string `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression (e.g. \"tag.team=missing,provider=aws\")."`
}

// inspectTopSavingsToolOutputSchema returns the schema for the
// [inspect.TopSavingsResult] shape. Carries multiple rat.Rat fields
// (the per-item yearly_savings plus the headline total) covered by
// the same rat.Rat -> string override the rest of the inspect surface
// uses.
func inspectTopSavingsToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[inspect.TopSavingsResult](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_top_savings tool output schema: %w", err)
	}
	return schema, nil
}

// InspectPolicyDetailInput is the input shape for the
// `inspect_policy_detail` MCP tool. Policy is the identifier — name or
// slug; both finops and tagging policies match. Resource narrows the
// per-resource list when set.
type InspectPolicyDetailInput struct {
	Path     string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Policy   string `json:"policy" jsonschema:"Policy name or slug (matches either). Required."`
	Resource string `json:"resource,omitempty" jsonschema:"Optional resource address; narrows the failing-resources list to just this address."`
	Project  string `json:"project,omitempty" jsonschema:"Filter to a specific project by name before drilling in."`
	Filter   string `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression."`
}

// InspectBudgetDetailInput is the input shape for the
// `inspect_budget_detail` MCP tool.
type InspectBudgetDetailInput struct {
	Path    string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Budget  string `json:"budget" jsonschema:"Budget name or ID. Required."`
	Project string `json:"project,omitempty" jsonschema:"Filter to a specific project by name before drilling in."`
	Filter  string `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression."`
}

// InspectGuardrailDetailInput is the input shape for the
// `inspect_guardrail_detail` MCP tool.
type InspectGuardrailDetailInput struct {
	Path      string `json:"path,omitempty" jsonschema:"Target a specific previously-scanned directory (absolute or relative). Defaults to the most recent scan in this MCP session."`
	Guardrail string `json:"guardrail" jsonschema:"Guardrail name or ID. Required."`
	Project   string `json:"project,omitempty" jsonschema:"Filter to a specific project by name before drilling in."`
	Filter    string `json:"filter,omitempty" jsonschema:"Generic AND'd filter expression."`
}

// inspectPolicyDetailToolOutputSchema returns the schema for
// [inspect.PolicyDetail]. Carries rat.Rat indirectly via
// format.FinopsIssueOutput (yearly_savings) so the override applies.
func inspectPolicyDetailToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[inspect.PolicyDetail](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_policy_detail tool output schema: %w", err)
	}
	return schema, nil
}

// inspectBudgetDetailToolOutputSchema returns the schema for
// [inspect.BudgetDetail]. Carries rat.Rat both directly (amount /
// current_cost on the embedded BudgetOutput) and via the matching
// resources / savings rows.
func inspectBudgetDetailToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[inspect.BudgetDetail](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_budget_detail tool output schema: %w", err)
	}
	return schema, nil
}

// inspectGuardrailDetailToolOutputSchema returns the schema for
// [format.GuardrailOutput] — the raw guardrail entry, matching the CLI
// behavior where --guardrail X --json prints just the matching
// guardrail with no extra enrichment.
func inspectGuardrailDetailToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[format.GuardrailOutput](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[rat.Rat](): {Type: "string"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building inspect_guardrail_detail tool output schema: %w", err)
	}
	return schema, nil
}

// MCPDoctorInput is the input shape for the `doctor` MCP tool. It
// deliberately omits the CLI's --fix flag: auto-remediation is
// destructive and the agent doesn't have the per-fix user-confirmation
// context the CLI assumes. Agents who want to remediate should suggest
// the user run `infracost doctor --fix` rather than have the tool
// perform the change implicitly.
type MCPDoctorInput struct {
	Verbose     bool `json:"verbose,omitempty" jsonschema:"Include diagnostic detail for every check (otherwise hints only appear on failing ones)."`
	Bundle      bool `json:"bundle,omitempty" jsonschema:"Attach a system / environment / cache snapshot to the response. Implies verbose + check_agents + check_ide."`
	CheckAgents bool `json:"check_agents,omitempty" jsonschema:"Include AI coding agent integrations in the checks."`
	CheckIDE    bool `json:"check_ide,omitempty" jsonschema:"Include IDE integrations in the checks."`
}

// doctorToolOutputSchema returns the JSON schema describing
// [DoctorOutput]. No rat.Rat involved — doctor reports are text and
// counts only. doctor.Status is a Go int enum but marshals to its
// lowercase string name (see doctor.Status.MarshalJSON), so we override
// the reflected "integer" type with the string enum the wire format
// actually carries — otherwise MCP output validation rejects "pass".
func doctorToolOutputSchema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[DoctorOutput](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[doctor.Status](): {
				Type: "string",
				Enum: []any{"pass", "warning", "fail", "skipped"},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building doctor tool output schema: %w", err)
	}
	return schema, nil
}
