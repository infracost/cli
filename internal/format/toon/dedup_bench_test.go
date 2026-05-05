package toon_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/format/toon"
	"github.com/pkoukk/tiktoken-go"
)

// TestSchemaDedupBench isolates the impact of the TagSchema dedup change.
//
// We build a single tagging policy where N resources fail the same tag key
// against an M-element valid_values list, then encode the data twice:
//
//   - Current shape: TagSchema declared once, InvalidTag carries only key/value.
//   - Legacy shape: every InvalidTag repeats valid_values + valid_regex + the
//     other schema fields.
//
// Same content, different shape. Tokens and bytes are reported for JSON and
// TOON in both shapes.
func TestSchemaDedupBench(t *testing.T) {
	cl100k, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		t.Fatalf("load cl100k_base: %v", err)
	}

	type scenario struct {
		name         string
		resourceN    int
		validValuesM int
	}
	scenarios := []scenario{
		{"few-resources_short-list", 5, 4},
		{"many-resources_short-list", 50, 4},
		{"many-resources_long-list", 50, 20},
		{"saturating", 200, 30},
	}

	rows := make([]dedupRow, 0, len(scenarios))

	for _, s := range scenarios {
		validValues := makeValidValues(s.validValuesM)

		current := buildCurrentShape(s.resourceN, validValues)
		legacy := buildLegacyShape(s.resourceN, validValues)

		currentJSON, _ := json.MarshalIndent(current, "", "  ")
		legacyJSON, _ := json.MarshalIndent(legacy, "", "  ")
		currentTOON, _ := toon.Marshal(current)
		legacyTOON, _ := toon.Marshal(legacy)

		rows = append(rows, dedupRow{
			name:            s.name,
			legacyJSON:      len(legacyJSON),
			currentJSON:     len(currentJSON),
			legacyJSONTok:   len(cl100k.Encode(string(legacyJSON), nil, nil)),
			currentJSONTok:  len(cl100k.Encode(string(currentJSON), nil, nil)),
			legacyTOON:      len(legacyTOON),
			currentTOON:     len(currentTOON),
			legacyTOONTok:   len(cl100k.Encode(string(legacyTOON), nil, nil)),
			currentTOONTok:  len(cl100k.Encode(string(currentTOON), nil, nil)),
		})
	}

	t.Log("\n" + renderDedupTable(rows))
}

// buildCurrentShape constructs a TaggingOutput in the current (post-dedup)
// shape: schema declared once, per-instance InvalidTag carries only Key/Value.
func buildCurrentShape(resourceN int, validValues []string) format.TaggingOutput {
	out := format.TaggingOutput{
		PolicyID:   "tag-policy",
		PolicyName: "Required environment tag",
		Message:    "environment must be one of the standard values",
		TagSchema: []format.TagSchemaEntry{
			{
				Key:         "environment",
				ValidValues: validValues,
				ValidRegex:  "^(production|staging|development|qa)$",
				Message:     "environment must be one of the standard values",
				Mandatory:   true,
			},
		},
	}
	for i := 0; i < resourceN; i++ {
		out.FailingResources = append(out.FailingResources, format.FailingTaggingResourceOutput{
			Address:      fmt.Sprintf("aws_instance.web_%d", i),
			ResourceType: "aws_instance",
			Path:         "modules/compute/main.tf",
			Line:         42,
			InvalidTags: []format.InvalidTagOutput{
				{Key: "environment", Value: badValueFor(i), Suggestion: "production"},
			},
		})
	}
	return out
}

// legacyTaggingOutput mirrors the pre-dedup shape so we can serialize it for
// the same content. Field tags match the original (no omitempty, no
// tag_schema). Used only by this benchmark.
type legacyTaggingOutput struct {
	PolicyID         string                              `json:"policy_id"`
	PolicyName       string                              `json:"policy_name"`
	Message          string                              `json:"message"`
	FailingResources []legacyFailingTaggingResourceOutput `json:"failing_resources"`
}

type legacyFailingTaggingResourceOutput struct {
	Address              string                    `json:"address"`
	ResourceType         string                    `json:"resource_type"`
	InvalidTags          []legacyInvalidTagOutput  `json:"invalid_tags"`
	Path                 string                    `json:"path"`
	Line                 int                       `json:"line"`
	MissingMandatoryTags []string                  `json:"missing_mandatory_tags"`
	PropagationProblems  []any                     `json:"propagation_problems"`
}

type legacyInvalidTagOutput struct {
	Key                  string   `json:"key"`
	Value                string   `json:"value"`
	ValidRegex           string   `json:"valid_regex"`
	Suggestion           string   `json:"suggestion"`
	Message              string   `json:"message"`
	ValidValues          []string `json:"valid_values"`
	ValidValueCount      int      `json:"valid_value_count"`
	ValidValuesTruncated bool     `json:"valid_values_truncated"`
	FromDefaultTags      bool     `json:"from_default_tags"`
	MissingMandatory     bool     `json:"missing_mandatory"`
}

func buildLegacyShape(resourceN int, validValues []string) legacyTaggingOutput {
	out := legacyTaggingOutput{
		PolicyID:   "tag-policy",
		PolicyName: "Required environment tag",
		Message:    "environment must be one of the standard values",
	}
	for i := 0; i < resourceN; i++ {
		out.FailingResources = append(out.FailingResources, legacyFailingTaggingResourceOutput{
			Address:      fmt.Sprintf("aws_instance.web_%d", i),
			ResourceType: "aws_instance",
			Path:         "modules/compute/main.tf",
			Line:         42,
			InvalidTags: []legacyInvalidTagOutput{
				{
					Key:              "environment",
					Value:            badValueFor(i),
					ValidRegex:       "^(production|staging|development|qa)$",
					Suggestion:       "production",
					Message:          "environment must be one of the standard values",
					ValidValues:      validValues,
					ValidValueCount:  len(validValues),
					MissingMandatory: true,
				},
			},
		})
	}
	return out
}

func makeValidValues(n int) []string {
	base := []string{"production", "staging", "development", "qa", "perf", "preview", "canary", "shadow"}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if i < len(base) {
			out = append(out, base[i])
		} else {
			out = append(out, fmt.Sprintf("env-%02d", i))
		}
	}
	return out
}

func badValueFor(i int) string {
	bad := []string{"prod", "Stage", "DEV", "test", "stg"}
	return bad[i%len(bad)]
}

type dedupRow struct {
	name                          string
	legacyJSON, currentJSON       int
	legacyJSONTok, currentJSONTok int
	legacyTOON, currentTOON       int
	legacyTOONTok, currentTOONTok int
}

func renderDedupTable(rows []dedupRow) string {
	var b strings.Builder
	fmt.Fprintln(&b, "TagSchema dedup: legacy (per-instance) vs current (per-policy) shape")
	fmt.Fprintln(&b, "Same content, different shape. cl100k_base tokenization.")
	fmt.Fprintln(&b, "")
	fmt.Fprintf(&b, "%-30s | %-26s | %-26s\n", "scenario", "JSON tokens (legacy→current)", "--llm tokens (legacy→current)")
	fmt.Fprintln(&b, strings.Repeat("-", 90))
	for _, r := range rows {
		jsonShift := pctChange(r.legacyJSONTok, r.currentJSONTok)
		toonShift := pctChange(r.legacyTOONTok, r.currentTOONTok)
		fmt.Fprintf(&b, "%-30s | %6d → %-6d (%6s) | %6d → %-6d (%6s)\n",
			r.name,
			r.legacyJSONTok, r.currentJSONTok, jsonShift,
			r.legacyTOONTok, r.currentTOONTok, toonShift,
		)
	}
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "Negative percentages mean current shape uses fewer tokens than legacy.")
	return b.String()
}

func pctChange(from, to int) string {
	if from == 0 {
		return "—"
	}
	d := 100 * float64(to-from) / float64(from)
	return fmt.Sprintf("%+.1f%%", d)
}

