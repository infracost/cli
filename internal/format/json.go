package format

import (
	"encoding/json"
	"io"
	"sort"

	"github.com/infracost/cli/internal/format/toon"
	"github.com/infracost/go-proto/pkg/diagnostic"
	"github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/provider"
)

// Output is the top-level JSON structure produced by the scan command.
type Output struct {
	Currency         string             `json:"currency"`
	Projects         []ProjectOutput    `json:"projects"`
	GuardrailResults []GuardrailOutput  `json:"guardrail_results,omitempty"`
	BudgetResults    []BudgetOutput     `json:"budget_results,omitempty"`

	// Fields below are not serialized to JSON but carried through for event
	// metadata.
	projectTypes           []string
	estimatedUsageCounts   map[string]int // nil means no usage file was loaded
	unestimatedUsageCounts map[string]int
}

type ProjectOutput struct {
	ProjectName    string             `json:"project_name"`
	Path           string             `json:"path"`
	FinopsResults  []FinopsOutput     `json:"finops_results"`
	TaggingResults []TaggingOutput    `json:"tagging_results,omitempty"`
	Resources      []ResourceOutput   `json:"resources"`
	Diagnostics    []DiagnosticOutput `json:"diagnostics,omitempty"`
}

type ResourceMetadata struct {
	Filename     string `json:"filename,omitempty"`
	StartLine    int    `json:"start_line,omitempty"`
	EndLine      int    `json:"end_line,omitempty"`
	DeepChecksum string `json:"deep_checksum,omitempty"`
}

type ResourceOutput struct {
	Name                string                `json:"name"`
	Type                string                `json:"type"`
	IsSupported         bool                  `json:"is_supported"`
	IsFree              bool                  `json:"is_free"`
	CostComponents      []CostComponentOutput `json:"cost_components,omitempty"`
	Subresources        []ResourceOutput      `json:"subresources,omitempty"`
	Tags                map[string]string     `json:"tags,omitempty"`
	SupportsTags        bool                  `json:"supports_tags,omitempty"`
	SupportsDefaultTags bool                  `json:"supports_default_tags,omitempty"`
	Metadata            ResourceMetadata      `json:"metadata"`
}

type CostComponentOutput struct {
	Name                          string   `json:"name"`
	Unit                          string   `json:"unit"`
	Price                         *rat.Rat `json:"price,omitempty"`
	Quantity                      *rat.Rat `json:"quantity,omitempty"`
	BaseMonthlyCost               *rat.Rat `json:"base_monthly_cost,omitempty"`
	UsageMonthlyCost              *rat.Rat `json:"usage_monthly_cost,omitempty"`
	TotalMonthlyCost              *rat.Rat `json:"total_monthly_cost,omitempty"`
	MonthlyCarbonSavingsGramsCo2E *rat.Rat `json:"monthly_carbon_savings_grams_co2e,omitempty"`
	MonthlyWaterSavingsLiters     *rat.Rat `json:"monthly_water_savings_liters,omitempty"`
}

type DiagnosticOutput struct {
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type FinopsOutput struct {
	PolicyID         string                        `json:"policy_id"`
	PolicyName       string                        `json:"policy_name"`
	PolicySlug       string                        `json:"policy_slug"`
	PolicyMessage    string                        `json:"policy_message"`
	FailingResources []FinopsFailingResourceOutput `json:"failing_resources"`
}

type FinopsFailingResourceOutput struct {
	Name   string              `json:"name"`
	Issues []FinopsIssueOutput `json:"issues"`
}

type FinopsIssueOutput struct {
	Description                   string   `json:"description"`
	MonthlySavings                *rat.Rat `json:"monthly_savings,omitempty"`
	MonthlyCarbonSavingsGramsCo2E *rat.Rat `json:"monthly_carbon_savings_grams_co2e,omitempty"`
	MonthlyWaterSavingsLiters     *rat.Rat `json:"monthly_water_savings_liters,omitempty"`
	Address                       string   `json:"address,omitempty"`
	Attribute                     string   `json:"attribute,omitempty"`
}

type TaggingOutput struct {
	PolicyID         string                         `json:"policy_id"`
	PolicyName       string                         `json:"policy_name"`
	Message          string                         `json:"message"`
	// TagSchema describes the policy's per-key requirements (allowed values,
	// validation regex, mandatory flag) once per tag key, instead of repeating
	// them on every failing-resource invalid-tag entry.
	TagSchema        []TagSchemaEntry               `json:"tag_schema,omitempty"`
	FailingResources []FailingTaggingResourceOutput `json:"failing_resources"`
}

// TagSchemaEntry is the canonical, schema-level description of a single tag
// key the policy cares about. Per-resource InvalidTagOutput entries reference
// it by Key.
type TagSchemaEntry struct {
	Key         string   `json:"key"`
	ValidRegex  string   `json:"valid_regex,omitempty"`
	ValidValues []string `json:"valid_values,omitempty"`
	Message     string   `json:"message,omitempty"`
	Mandatory   bool     `json:"mandatory,omitempty"`
}

type FailingTaggingResourceOutput struct {
	Address              string                        `json:"address"`
	ResourceType         string                        `json:"resource_type"`
	InvalidTags          []InvalidTagOutput            `json:"invalid_tags"`
	Path                 string                        `json:"path"`
	Line                 int                           `json:"line"`
	MissingMandatoryTags []string                      `json:"missing_mandatory_tags"`
	PropagationProblems  []TagPropagationProblemOutput `json:"propagation_problems"`
}

// InvalidTagOutput carries only the per-instance facts about a single failing
// tag. Schema-level metadata (allowed values, validation regex, validation
// message, mandatory flag) lives on TaggingOutput.TagSchema; look it up by Key.
type InvalidTagOutput struct {
	Key             string `json:"key"`
	Value           string `json:"value"`
	Suggestion      string `json:"suggestion,omitempty"`
	FromDefaultTags bool   `json:"from_default_tags,omitempty"`
}

type TagPropagationProblemOutput struct {
	Attribute    string   `json:"attribute"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	ValidSources []string `json:"valid_sources"`
	AffectedTags []string `json:"affected_tags"`
}

type GuardrailOutput struct {
	GuardrailID      string   `json:"guardrail_id"`
	GuardrailName    string   `json:"guardrail_name"`
	Triggered        bool     `json:"triggered"`
	TotalMonthlyCost *rat.Rat `json:"total_monthly_cost,omitempty"`
}

type BudgetTagOutput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type BudgetOutput struct {
	BudgetID             string            `json:"budget_id"`
	BudgetName           string            `json:"budget_name"`
	Tags                 []BudgetTagOutput `json:"tags"`
	Amount               *rat.Rat          `json:"amount"`
	CurrentCost          *rat.Rat          `json:"current_cost"`
	OverBudget           bool              `json:"over_budget"`
	CustomOverrunMessage string            `json:"custom_overrun_message,omitempty"`
}

// ToOutput converts a Result into an Output suitable for JSON serialization.
func ToOutput(result *Result) Output {
	projects := make([]ProjectOutput, 0, len(result.Projects))
	projectTypes := make([]string, 0, len(result.Projects))
	for _, pr := range result.Projects {
		projects = append(projects, convertProjectResult(pr))
		projectTypes = append(projectTypes, string(pr.Config.Type))
	}
	guardrailResults := make([]GuardrailOutput, 0, len(result.GuardrailResults))
	for _, gr := range result.GuardrailResults {
		guardrailResults = append(guardrailResults, GuardrailOutput{
			GuardrailID:      gr.GuardrailID,
			GuardrailName:    gr.GuardrailName,
			Triggered:        gr.Triggered,
			TotalMonthlyCost: gr.TotalMonthlyCost,
		})
	}

	budgetResults := make([]BudgetOutput, 0, len(result.BudgetResults))
	for _, br := range result.BudgetResults {
		tags := make([]BudgetTagOutput, 0, len(br.Tags))
		for _, t := range br.Tags {
			tags = append(tags, BudgetTagOutput{Key: t.Key, Value: t.Value})
		}
		budgetResults = append(budgetResults, BudgetOutput{
			BudgetID:             br.BudgetID,
			BudgetName:           br.BudgetName,
			Tags:                 tags,
			Amount:               br.Amount,
			CurrentCost:          br.CurrentCost,
			OverBudget:           br.CurrentCost.GreaterThan(br.Amount),
			CustomOverrunMessage: br.CustomOverrunMessage,
		})
	}

	return Output{
		Currency:               result.Config.Currency,
		Projects:               projects,
		GuardrailResults:       guardrailResults,
		BudgetResults:          budgetResults,
		projectTypes:           projectTypes,
		estimatedUsageCounts:   result.EstimatedUsageCounts,
		unestimatedUsageCounts: result.UnestimatedUsageCounts,
	}
}

func convertProjectResult(pr *ProjectResult) ProjectOutput {
	resources := make([]ResourceOutput, 0, len(pr.Resources))
	for _, r := range pr.Resources {
		resources = append(resources, convertResource(r))
	}

	taggingResults := make([]TaggingOutput, 0, len(pr.TagPolicyResults))
	for _, tr := range pr.TagPolicyResults {
		taggingResults = append(taggingResults, convertTaggingResult(tr))
	}

	finopsResults := make([]FinopsOutput, 0, len(pr.FinopsResults))
	for _, r := range pr.FinopsResults {
		failingResources := make([]FinopsFailingResourceOutput, 0, len(r.FailingResources))
		for _, fr := range r.FailingResources {
			issues := make([]FinopsIssueOutput, 0, len(fr.Issues))
			for _, iss := range fr.Issues {
				issues = append(issues, FinopsIssueOutput{
					Description:                   iss.Description,
					MonthlySavings:                rat.FromProto(iss.MonthlySavings),
					MonthlyCarbonSavingsGramsCo2E: rat.FromProto(iss.MonthlyCarbonSavingsGramsCo2E),
					MonthlyWaterSavingsLiters:     rat.FromProto(iss.MonthlyWaterSavingsLiters),
					Address:                       iss.Address,
					Attribute:                     iss.Attribute,
				})
			}
			failingResources = append(failingResources, FinopsFailingResourceOutput{
				Name:   fr.CauseAddress,
				Issues: issues,
			})
		}
		finopsResults = append(finopsResults, FinopsOutput{
			PolicyID:         r.PolicyId,
			PolicyName:       r.PolicyName,
			PolicySlug:       r.PolicySlug,
			PolicyMessage:    r.PolicyMessage,
			FailingResources: failingResources,
		})
	}

	diagnostics := make([]DiagnosticOutput, 0, len(pr.Diagnostics))
	for _, d := range pr.Diagnostics {
		diagnostics = append(diagnostics, convertDiagnostic(d))
	}

	return ProjectOutput{
		ProjectName:    pr.Config.Name,
		Path:           pr.Config.Path,
		FinopsResults:  finopsResults,
		TaggingResults: taggingResults,
		Resources:      resources,
		Diagnostics:    diagnostics,
	}
}

// ToJSON writes an Output as JSON to w.
func (o *Output) ToJSON(w io.Writer) error {
	outJSON, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(outJSON)
	return err
}

// ToTOON writes an Output as TOON (Token-Oriented Object Notation) to w. The
// representation carries the same data model as ToJSON but uses TOON's compact,
// indentation-based syntax intended for LLM consumption.
func (o *Output) ToTOON(w io.Writer) error {
	return toon.MarshalTo(w, o)
}

func convertResource(r *provider.Resource) ResourceOutput {
	subs := make([]ResourceOutput, 0, len(r.ChildResources))
	for _, sr := range r.ChildResources {
		subs = append(subs, convertResource(sr))
	}

	var costs []CostComponentOutput
	if r.Costs != nil {
		costs = make([]CostComponentOutput, 0, len(r.Costs.Components))
		for _, c := range r.Costs.Components {
			costs = append(costs, convertCostComponent(c))
		}
	} else {
		costs = []CostComponentOutput{}
	}

	var metadata ResourceMetadata
	if r.Metadata != nil {
		metadata = ResourceMetadata{
			Filename:     r.Metadata.Filename,
			StartLine:    int(r.Metadata.StartLine),
			EndLine:      int(r.Metadata.EndLine),
			DeepChecksum: r.Metadata.DeepChecksum,
		}
	}

	var tags map[string]string
	if r.Tagging != nil && len(r.Tagging.Tags) > 0 {
		tags = make(map[string]string, len(r.Tagging.Tags))
		for _, t := range r.Tagging.Tags {
			tags[t.Key] = t.Value
		}
	}

	return ResourceOutput{
		Name:                r.Name,
		Type:                r.Type,
		CostComponents:      costs,
		Subresources:        subs,
		Tags:                tags,
		IsSupported:         r.IsSupported,
		IsFree:              r.IsFree,
		SupportsTags:        r.Tagging != nil && r.Tagging.SupportsTags,
		SupportsDefaultTags: r.Tagging != nil && r.Tagging.SupportsDefaultTags,
		Metadata:            metadata,
	}
}

var hoursInMonth = rat.New(730)

func convertQuantityToMonthly(qty *rat.Rat, period provider.Period) *rat.Rat {
	switch period {
	case provider.Period_MONTH:
		return qty
	case provider.Period_HOUR:
		return qty.Mul(hoursInMonth)
	default:
		return rat.Zero
	}
}

// applyDiscount applies a discount rate to a price if the rate is greater than zero.
func applyDiscount(price *rat.Rat, discountRate *rat.Rat) *rat.Rat {
	if discountRate != nil && discountRate.GreaterThan(rat.Zero) {
		return price.Mul(rat.New(1).Sub(discountRate))
	}
	return price
}

func convertCostComponent(c *provider.CostComponent) CostComponentOutput {
	monthlyQty := rat.Zero
	monthlyUsageCost := rat.Zero
	monthlyBaseCost := rat.Zero

	monthlyCarbonGramsCo2e := rat.Zero
	monthlyWaterLitres := rat.Zero

	price := rat.Zero

	if c.PeriodPrice != nil {
		price = applyDiscount(rat.FromProto(c.PeriodPrice.Price), rat.FromProto(c.DiscountRate))
		if c.Quantity != nil {
			monthlyQty = convertQuantityToMonthly(rat.FromProto(c.Quantity), c.PeriodPrice.Period)
			if c.UsageBased {
				monthlyUsageCost = price.Mul(monthlyQty)
			} else {
				monthlyBaseCost = price.Mul(monthlyQty)
			}
		}
	}

	if c.EnvironmentalMetrics != nil && c.Quantity != nil {
		envQty := rat.FromProto(c.Quantity)
		if c.EnvironmentalMetrics.CarbonGramsCo2E != nil {
			monthlyEnvQty := convertQuantityToMonthly(envQty, c.EnvironmentalMetrics.Period)
			monthlyCarbonGramsCo2e = monthlyEnvQty.Mul(rat.FromProto(c.EnvironmentalMetrics.CarbonGramsCo2E))
		}
		if c.EnvironmentalMetrics.WaterLiters != nil {
			monthlyEnvQty := convertQuantityToMonthly(envQty, c.EnvironmentalMetrics.Period)
			monthlyWaterLitres = monthlyEnvQty.Mul(rat.FromProto(c.EnvironmentalMetrics.WaterLiters))
		}
	}

	return CostComponentOutput{
		Name:                          c.Name,
		Unit:                          c.Unit,
		Price:                         price,
		Quantity:                      monthlyQty,
		BaseMonthlyCost:               monthlyBaseCost,
		UsageMonthlyCost:              monthlyUsageCost,
		TotalMonthlyCost:              monthlyBaseCost.Add(monthlyUsageCost),
		MonthlyCarbonSavingsGramsCo2E: monthlyCarbonGramsCo2e,
		MonthlyWaterSavingsLiters:     monthlyWaterLitres,
	}
}

func convertDiagnostic(d *diagnostic.Diagnostic) DiagnosticOutput {
	severity := "info"
	switch {
	case d.Critical:
		severity = "critical"
	case d.Warning:
		severity = "warning"
	}
	return DiagnosticOutput{
		Message:  d.String(),
		Severity: severity,
	}
}

func convertTaggingResult(tr event.TaggingPolicyResult) TaggingOutput {
	failingResources := make([]FailingTaggingResourceOutput, 0, len(tr.FailingResources))
	for _, r := range tr.FailingResources {
		failingResources = append(failingResources, convertFailingTaggingResource(r))
	}
	return TaggingOutput{
		PolicyID:         tr.TagPolicyID,
		PolicyName:       tr.Name,
		Message:          tr.Message,
		TagSchema:        buildTagSchema(tr.FailingResources),
		FailingResources: failingResources,
	}
}

// buildTagSchema collapses the per-instance schema metadata that upstream
// repeats on every InvalidTag (ValidValues, ValidRegex, Message, Mandatory)
// into a single per-key entry. Allowed-value lists are unioned across all
// occurrences so we converge on the policy's full vocabulary even when
// upstream produced narrowed/suggestion-mode lists for individual instances.
// Keys that only appear via MissingMandatoryTags (never present, so no
// InvalidTag) get a Mandatory:true entry with no other metadata.
func buildTagSchema(resources []event.TagPolicyResultResource) []TagSchemaEntry {
	type acc struct {
		regex     string
		message   string
		mandatory bool
		values    map[string]struct{}
	}
	byKey := map[string]*acc{}
	var order []string
	get := func(key string) *acc {
		a, ok := byKey[key]
		if !ok {
			a = &acc{values: map[string]struct{}{}}
			byKey[key] = a
			order = append(order, key)
		}
		return a
	}
	for _, r := range resources {
		for _, t := range r.InvalidTags {
			a := get(t.Key)
			if a.regex == "" {
				a.regex = t.ValidRegex
			}
			if a.message == "" {
				a.message = t.Message
			}
			if t.MissingMandatory {
				a.mandatory = true
			}
			for _, v := range t.ValidValues {
				a.values[v] = struct{}{}
			}
		}
		for _, k := range r.MissingMandatoryTags {
			a := get(k)
			a.mandatory = true
		}
	}
	out := make([]TagSchemaEntry, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		entry := TagSchemaEntry{
			Key:        k,
			ValidRegex: a.regex,
			Message:    a.message,
			Mandatory:  a.mandatory,
		}
		if len(a.values) > 0 {
			vals := make([]string, 0, len(a.values))
			for v := range a.values {
				vals = append(vals, v)
			}
			sort.Strings(vals)
			entry.ValidValues = vals
		}
		out = append(out, entry)
	}
	return out
}

func convertFailingTaggingResource(r event.TagPolicyResultResource) FailingTaggingResourceOutput {
	invalidTags := make([]InvalidTagOutput, 0, len(r.InvalidTags))
	for _, t := range r.InvalidTags {
		invalidTags = append(invalidTags, InvalidTagOutput{
			Key:             t.Key,
			Value:           t.Value,
			Suggestion:      t.Suggestion,
			FromDefaultTags: t.FromDefaultTags,
		})
	}

	propagationProblems := make([]TagPropagationProblemOutput, 0, len(r.PropagationProblems))
	for _, p := range r.PropagationProblems {
		propagationProblems = append(propagationProblems, TagPropagationProblemOutput{
			Attribute:    p.Attribute,
			From:         p.From,
			To:           p.To,
			ValidSources: p.ValidSources,
			AffectedTags: p.AffectedTags,
		})
	}

	return FailingTaggingResourceOutput{
		Address:              r.Address,
		ResourceType:         r.ResourceType,
		InvalidTags:          invalidTags,
		Path:                 r.Path,
		Line:                 r.Line,
		MissingMandatoryTags: r.MissingMandatoryTags,
		PropagationProblems:  propagationProblems,
	}
}
