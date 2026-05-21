package inspect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/format/toon"
	"github.com/infracost/go-proto/pkg/rat"
)

// orderedFields is a JSON-marshalable list of (key, value) pairs that
// preserves insertion order. Used by projection sites (--fields) so the
// caller's field order survives encoding — Go's encoding/json sorts map
// keys alphabetically, and the toon encoder explicitly sorts them too,
// which would otherwise drop the user's intent. The toon encoder honors
// json.Marshaler, so implementing it once gets us order preservation in
// both --json and --llm output.
type orderedFields []orderedField

// orderedField is one (key, value) pair in an orderedFields. Both sides
// are strings because every projection site renders to strings before
// emitting; if a future projection needs a non-string value it can swap
// Value for any.
type orderedField struct {
	Key   string
	Value string
}

func (o orderedFields) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(f.Key)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// orderedFromMap converts a (rendered string) map into an orderedFields
// projection, taking values in the order the keys slice specifies. Used
// where a projection helper still returns map[string]string for the TSV
// path's lookups.
func orderedFromMap(m map[string]string, keys []string) orderedFields {
	out := make(orderedFields, 0, len(keys))
	for _, k := range keys {
		out = append(out, orderedField{Key: k, Value: m[k]})
	}
	return out
}

// writeStructured marshals v in the structured format selected by opts (TOON
// when opts.LLM is set, otherwise indented JSON) and writes it to w with a
// trailing newline. Used by every inspect view's structured early-return so
// the encoding behavior stays consistent.
func writeStructured(w io.Writer, v any, opts Options) error {
	if opts.LLM {
		b, err := toon.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// BudgetDetail is the structured payload for `inspect --budget X --json`.
// Carries the budget itself plus the resources in this scan that match its
// tag scope and any FinOps savings on those resources — mirrors what the
// boxed text view shows.
type BudgetDetail struct {
	*format.BudgetOutput
	MatchingResources []BudgetMatchingResource `json:"matching_resources,omitempty"`
	Savings           []BudgetSaving           `json:"savings,omitempty"`
}

type BudgetMatchingResource struct {
	Type        string   `json:"type"`
	Count       int      `json:"count"`
	MonthlyCost *rat.Rat `json:"monthly_cost"`
}

type BudgetSaving struct {
	PolicyName    string   `json:"policy_name"`
	Savings       *rat.Rat `json:"savings"`
	ResourceCount int      `json:"resource_count"`
}

func BuildBudgetDetail(data *format.Output, br format.BudgetOutput) BudgetDetail {
	out := BudgetDetail{BudgetOutput: &br}
	for _, m := range collectMatchingResources(data, br.Tags) {
		out.MatchingResources = append(out.MatchingResources, BudgetMatchingResource{
			Type:        m.resourceType,
			Count:       m.count,
			MonthlyCost: m.cost,
		})
	}
	for _, s := range collectBudgetSavings(data, br.Tags) {
		out.Savings = append(out.Savings, BudgetSaving{
			PolicyName:    s.policyName,
			Savings:       s.savings,
			ResourceCount: s.resourceCount,
		})
	}
	return out
}

// PolicyDetail is the structured payload for `inspect --policy X --json`.
// Either kind ("finops" or "tagging") populates a different resources slice,
// since the per-resource detail differs (issues vs missing/invalid tags).
//
// For tagging policies, TagSchema carries the per-key schema once (allowed
// values, validation regex, mandatory flag) — invalid_tags entries reference
// keys back into this list rather than repeating the schema per occurrence.
type PolicyDetail struct {
	Kind      string                  `json:"kind"`
	Name      string                  `json:"name"`
	Slug      string                  `json:"slug,omitempty"`
	Message   string                  `json:"message,omitempty"`
	Resources []PolicyResource    `json:"resources"`
	TagSchema []format.TagSchemaEntry `json:"tag_schema,omitempty"`
}

type PolicyResource struct {
	Project string `json:"project"`
	Address string `json:"address"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	// FinOps-only.
	Issues []format.FinopsIssueOutput `json:"issues,omitempty"`
	// Tagging-only.
	MissingMandatoryTags []string                  `json:"missing_mandatory_tags,omitempty"`
	InvalidTags          []format.InvalidTagOutput `json:"invalid_tags,omitempty"`
}

// BuildPolicyDetail aggregates matching FinOps and Tagging policies
// across all projects and returns one of two shapes — finops kind or
// tagging kind — along with whether either matched. opts.Resource
// narrows the per-resource list. Used by writePolicyDetailJSON for the
// CLI's --json output and by PolicyDetailFor for the MCP path.
func BuildPolicyDetail(data *format.Output, opts Options) (PolicyDetail, bool) {
	// FinOps: aggregate matched resources across projects.
	var (
		finopsName, finopsSlug, finopsMessage string
		finopsResources                       []PolicyResource
		finopsMatched                         bool
	)
	for _, p := range data.Projects {
		for _, f := range p.FinopsResults {
			if !matchesPolicy(f.PolicyName, f.PolicySlug, opts.Policy) {
				continue
			}
			finopsMatched = true
			finopsName, finopsSlug, finopsMessage = f.PolicyName, f.PolicySlug, f.PolicyMessage
			metaByName := make(map[string]format.ResourceMetadata, len(p.Resources))
			for _, r := range p.Resources {
				metaByName[r.Name] = r.Metadata
			}
			for _, fr := range f.FailingResources {
				if opts.Resource != "" && fr.Name != opts.Resource {
					continue
				}
				meta := metaByName[fr.Name]
				finopsResources = append(finopsResources, PolicyResource{
					Project: p.ProjectName,
					Address: fr.Name,
					File:    meta.Filename,
					Line:    meta.StartLine,
					Issues:  fr.Issues,
				})
			}
		}
	}
	if finopsMatched {
		return PolicyDetail{
			Kind:      "finops",
			Name:      finopsName,
			Slug:      finopsSlug,
			Message:   finopsMessage,
			Resources: finopsResources,
		}, true
	}

	// Tagging: same aggregation pattern.
	var (
		tagName, tagMessage string
		tagResources        []PolicyResource
		tagMatched          bool
		tagSchemas          []format.TagSchemaEntry
	)
	for _, p := range data.Projects {
		for _, t := range p.TaggingResults {
			if !matchesPolicy(t.PolicyName, "", opts.Policy) {
				continue
			}
			tagMatched = true
			tagName, tagMessage = t.PolicyName, t.Message
			tagSchemas = append(tagSchemas, t.TagSchema...)
			for _, tr := range t.FailingResources {
				if opts.Resource != "" && tr.Address != opts.Resource {
					continue
				}
				tagResources = append(tagResources, PolicyResource{
					Project:              p.ProjectName,
					Address:              tr.Address,
					File:                 tr.Path,
					Line:                 tr.Line,
					MissingMandatoryTags: tr.MissingMandatoryTags,
					InvalidTags:          tr.InvalidTags,
				})
			}
		}
	}
	if tagMatched {
		return PolicyDetail{
			Kind:      "tagging",
			Name:      tagName,
			Message:   tagMessage,
			Resources: tagResources,
			TagSchema: mergeTagSchemas(tagSchemas),
		}, true
	}

	return PolicyDetail{}, false
}

// PolicyDetailFor applies the inspect filter pipeline and returns the
// detail for opts.Policy. Pairs with the `inspect_policy_detail` MCP
// tool. Returns an actionable "policy not found" error so an agent
// can relay the typo or suggest calling `policies` to list valid names.
func PolicyDetailFor(data *format.Output, opts Options) (PolicyDetail, error) {
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return PolicyDetail{}, err
	}
	data = Filter(data, opts)
	detail, ok := BuildPolicyDetail(data, opts)
	if !ok {
		return PolicyDetail{}, fmt.Errorf("policy %q not found", opts.Policy)
	}
	return detail, nil
}

// BudgetDetailFor applies the inspect filter pipeline and returns the
// detail block for opts.Budget — the budget plus the resources in the
// latest scan whose tags match its scope and any FinOps savings on those
// resources. Pairs with the `inspect_budget_detail` MCP tool. Returns
// an actionable "budget not found" error when the name doesn't match.
func BudgetDetailFor(data *format.Output, opts Options) (BudgetDetail, error) {
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return BudgetDetail{}, err
	}
	data = Filter(data, opts)
	for _, br := range data.BudgetResults {
		if matchesPolicy(br.BudgetName, br.BudgetID, opts.Budget) {
			return BuildBudgetDetail(data, br), nil
		}
	}
	return BudgetDetail{}, fmt.Errorf("budget %q not found", opts.Budget)
}

// GuardrailDetailFor applies the inspect filter pipeline and returns
// the matching guardrail from the latest scan's results — including
// the triggered flag and total monthly cost the scan rolled up.
// Mirrors CLI behavior: the human / --json renderers print just the
// guardrail itself with no additional aggregation. Pairs with the
// `inspect_guardrail_detail` MCP tool.
func GuardrailDetailFor(data *format.Output, opts Options) (format.GuardrailOutput, error) {
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return format.GuardrailOutput{}, err
	}
	data = Filter(data, opts)
	for _, gr := range data.GuardrailResults {
		if matchesPolicy(gr.GuardrailName, gr.GuardrailID, opts.Guardrail) {
			return gr, nil
		}
	}
	return format.GuardrailOutput{}, fmt.Errorf("guardrail %q not found", opts.Guardrail)
}

// writePolicyDetailJSON now delegates to BuildPolicyDetail so the CLI's
// --json dispatch and the MCP tool path share the same aggregation
// pipeline. Behavior preserved: "policy not found" is still the error
// when nothing matches.
func writePolicyDetailJSON(w io.Writer, data *format.Output, opts Options) error {
	detail, ok := BuildPolicyDetail(data, opts)
	if !ok {
		return fmt.Errorf("policy %q not found", opts.Policy)
	}
	return writeStructured(w, detail, opts)
}

// FailingPanorama is the structured payload for `inspect --failing
// --json`. failing_policies is a flat per-pairing list (mirrors the text
// panorama); guardrails and budgets reuse their format types directly.
type FailingPanorama struct {
	FailingPolicies     []FailingPolicyPairing `json:"failing_policies"`
	TriggeredGuardrails []format.GuardrailOutput   `json:"triggered_guardrails"`
	OverBudget          []format.BudgetOutput      `json:"over_budget"`
}

type FailingPolicyPairing struct {
	Kind     string `json:"kind"` // "finops" or "tagging"
	Policy   string `json:"policy"`
	Project  string `json:"project"`
	Resource string `json:"resource"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message,omitempty"`
}

func BuildFailingPanorama(data *format.Output) FailingPanorama {
	out := FailingPanorama{
		FailingPolicies:     []FailingPolicyPairing{},
		TriggeredGuardrails: []format.GuardrailOutput{},
		OverBudget:          []format.BudgetOutput{},
	}
	for _, p := range data.Projects {
		metaByName := make(map[string]format.ResourceMetadata, len(p.Resources))
		for _, r := range p.Resources {
			metaByName[r.Name] = r.Metadata
		}
		for _, f := range p.FinopsResults {
			for _, fr := range f.FailingResources {
				meta := metaByName[fr.Name]
				out.FailingPolicies = append(out.FailingPolicies, FailingPolicyPairing{
					Kind:     "finops",
					Policy:   f.PolicyName,
					Project:  p.ProjectName,
					Resource: fr.Name,
					File:     meta.Filename,
					Line:     meta.StartLine,
					Message:  f.PolicyMessage,
				})
			}
		}
		for _, t := range p.TaggingResults {
			for _, tr := range t.FailingResources {
				out.FailingPolicies = append(out.FailingPolicies, FailingPolicyPairing{
					Kind:     "tagging",
					Policy:   t.PolicyName,
					Project:  p.ProjectName,
					Resource: tr.Address,
					File:     tr.Path,
					Line:     tr.Line,
					Message:  t.Message,
				})
			}
		}
	}
	for _, gr := range data.GuardrailResults {
		if gr.Triggered {
			out.TriggeredGuardrails = append(out.TriggeredGuardrails, gr)
		}
	}
	for _, br := range data.BudgetResults {
		if br.OverBudget {
			out.OverBudget = append(out.OverBudget, br)
		}
	}
	return out
}
