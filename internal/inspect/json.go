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

// budgetDetailJSON is the structured payload for `inspect --budget X --json`.
// Carries the budget itself plus the resources in this scan that match its
// tag scope and any FinOps savings on those resources — mirrors what the
// boxed text view shows.
type budgetDetailJSON struct {
	*format.BudgetOutput
	MatchingResources []budgetMatchingResourceJSON `json:"matching_resources,omitempty"`
	Savings           []budgetSavingJSON           `json:"savings,omitempty"`
}

type budgetMatchingResourceJSON struct {
	Type        string   `json:"type"`
	Count       int      `json:"count"`
	MonthlyCost *rat.Rat `json:"monthly_cost"`
}

type budgetSavingJSON struct {
	PolicyName    string   `json:"policy_name"`
	Savings       *rat.Rat `json:"savings"`
	ResourceCount int      `json:"resource_count"`
}

func buildBudgetDetailJSON(data *format.Output, br format.BudgetOutput) budgetDetailJSON {
	out := budgetDetailJSON{BudgetOutput: &br}
	for _, m := range collectMatchingResources(data, br.Tags) {
		out.MatchingResources = append(out.MatchingResources, budgetMatchingResourceJSON{
			Type:        m.resourceType,
			Count:       m.count,
			MonthlyCost: m.cost,
		})
	}
	for _, s := range collectBudgetSavings(data, br.Tags) {
		out.Savings = append(out.Savings, budgetSavingJSON{
			PolicyName:    s.policyName,
			Savings:       s.savings,
			ResourceCount: s.resourceCount,
		})
	}
	return out
}

// policyDetailJSON is the structured payload for `inspect --policy X --json`.
// Either kind ("finops" or "tagging") populates a different resources slice,
// since the per-resource detail differs (issues vs missing/invalid tags).
//
// For tagging policies, TagSchema carries the per-key schema once (allowed
// values, validation regex, mandatory flag) — invalid_tags entries reference
// keys back into this list rather than repeating the schema per occurrence.
type policyDetailJSON struct {
	Kind      string                  `json:"kind"`
	Name      string                  `json:"name"`
	Slug      string                  `json:"slug,omitempty"`
	Message   string                  `json:"message,omitempty"`
	Resources []policyResourceJSON    `json:"resources"`
	TagSchema []format.TagSchemaEntry `json:"tag_schema,omitempty"`
}

type policyResourceJSON struct {
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

// writePolicyDetailJSON aggregates matching FinOps and Tagging policies
// across all projects and emits one of two shapes — finops kind or tagging
// kind. Returns "policy not found" if neither matches.
func writePolicyDetailJSON(w io.Writer, data *format.Output, opts Options) error {
	// FinOps: aggregate matched resources across projects.
	var (
		finopsName, finopsSlug, finopsMessage string
		finopsResources                       []policyResourceJSON
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
				finopsResources = append(finopsResources, policyResourceJSON{
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
		return writeStructured(w, policyDetailJSON{
			Kind:      "finops",
			Name:      finopsName,
			Slug:      finopsSlug,
			Message:   finopsMessage,
			Resources: finopsResources,
		}, opts)
	}

	// Tagging: same aggregation pattern.
	var (
		tagName, tagMessage string
		tagResources        []policyResourceJSON
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
				tagResources = append(tagResources, policyResourceJSON{
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
		out := policyDetailJSON{
			Kind:      "tagging",
			Name:      tagName,
			Message:   tagMessage,
			Resources: tagResources,
			TagSchema: mergeTagSchemas(tagSchemas),
		}
		return writeStructured(w, out, opts)
	}

	return fmt.Errorf("policy %q not found", opts.Policy)
}

// failingPanoramaJSON is the structured payload for `inspect --failing
// --json`. failing_policies is a flat per-pairing list (mirrors the text
// panorama); guardrails and budgets reuse their format types directly.
type failingPanoramaJSON struct {
	FailingPolicies     []failingPolicyPairingJSON `json:"failing_policies"`
	TriggeredGuardrails []format.GuardrailOutput   `json:"triggered_guardrails"`
	OverBudget          []format.BudgetOutput      `json:"over_budget"`
}

type failingPolicyPairingJSON struct {
	Kind     string `json:"kind"` // "finops" or "tagging"
	Policy   string `json:"policy"`
	Project  string `json:"project"`
	Resource string `json:"resource"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message,omitempty"`
}

func failingPanoramaJSONFor(data *format.Output) failingPanoramaJSON {
	out := failingPanoramaJSON{
		FailingPolicies:     []failingPolicyPairingJSON{},
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
				out.FailingPolicies = append(out.FailingPolicies, failingPolicyPairingJSON{
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
				out.FailingPolicies = append(out.FailingPolicies, failingPolicyPairingJSON{
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
