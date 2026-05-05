package inspect

import (
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/format"
)

// ParseFilter translates a --filter expression into Options-shaped
// settings, applying them onto the supplied opts. The grammar is
// deliberately minimal in v1:
//
//	filter := pred ("," pred)*
//	pred   := key "=" value
//	key    := "policy" | "project" | "provider"
//	        | "tag." <ident>     // value must be "missing"
//
// Anything beyond this surface returns an actionable error pointing the
// user at the targeted flags. We also reject conflicting predicates
// against already-set option fields rather than silently overriding.
//
// The raw expression is captured to telemetry separately by the caller
// (see telemetryFlagAllowlist in main.go), so even rejected filters yield
// a usage signal — they tell us which patterns we'd want to support next.
func ParseFilter(expr string, opts *Options) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}
	for _, raw := range strings.Split(expr, ",") {
		pred := strings.TrimSpace(raw)
		if pred == "" {
			continue
		}
		key, value, ok := strings.Cut(pred, "=")
		if !ok {
			return errFilterTooComplex(pred, "predicates must use 'key=value' form")
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "" {
			return errFilterTooComplex(pred, "value cannot be empty")
		}

		switch {
		case key == "policy":
			if opts.Policy != "" && opts.Policy != value {
				return fmt.Errorf("--filter policy=%q conflicts with --policy %q", value, opts.Policy)
			}
			opts.Policy = value
		case key == "project":
			if opts.Project != "" && opts.Project != value {
				return fmt.Errorf("--filter project=%q conflicts with --project %q", value, opts.Project)
			}
			opts.Project = value
		case key == "provider":
			if opts.Provider != "" && opts.Provider != value {
				return fmt.Errorf("--filter provider=%q conflicts with --provider %q", value, opts.Provider)
			}
			opts.Provider = value
		case strings.HasPrefix(key, "tag."):
			tagKey := strings.TrimPrefix(key, "tag.")
			if tagKey == "" {
				return errFilterTooComplex(pred, "tag predicate needs a key, e.g. tag.team=missing")
			}
			if value != "missing" {
				return errFilterTooComplex(pred,
					"only tag.<key>=missing is supported in v1; for other tag predicates use --invalid-tag or open an issue describing the pattern")
			}
			if opts.MissingTag != "" && opts.MissingTag != tagKey {
				return fmt.Errorf("--filter tag.%s=missing conflicts with --missing-tag %q", tagKey, opts.MissingTag)
			}
			opts.MissingTag = tagKey
		default:
			return errFilterTooComplex(pred,
				"supported keys are policy, project, provider, tag.<key>=missing")
		}
	}
	return nil
}

func errFilterTooComplex(pred, hint string) error {
	return fmt.Errorf(
		"--filter expression too complex (%q): %s. "+
			"Use targeted flags (--missing-tag, --invalid-tag, --top-savings, --policy, etc.) or open an issue describing the filter shape you need",
		pred, hint)
}

// InferProvider returns the cloud provider from a resource type prefix.
func InferProvider(resourceType string) string {
	switch {
	case strings.HasPrefix(resourceType, "aws_"):
		return "aws"
	case strings.HasPrefix(resourceType, "google_"):
		return "google"
	case strings.HasPrefix(resourceType, "azurerm_"):
		return "azurerm"
	default:
		return "other"
	}
}

// Filter returns a new Output with resources filtered according to opts.
func Filter(data *format.Output, opts Options) *format.Output {
	out := &format.Output{
		Currency:         data.Currency,
		Projects:         make([]format.ProjectOutput, 0, len(data.Projects)),
		GuardrailResults: data.GuardrailResults,
		BudgetResults:    data.BudgetResults,
	}

	for _, p := range data.Projects {
		if opts.Project != "" && !strings.EqualFold(p.ProjectName, opts.Project) {
			continue
		}

		finops := p.FinopsResults
		tagging := p.TaggingResults

		if opts.Failing {
			finops = filterFailingFinops(finops)
			tagging = filterFailingTagging(tagging)
		}

		filtered := format.ProjectOutput{
			ProjectName:    p.ProjectName,
			Path:           p.Path,
			FinopsResults:  finops,
			TaggingResults: tagging,
			Diagnostics:    p.Diagnostics,
			Resources:      make([]format.ResourceOutput, 0, len(p.Resources)),
		}

		for _, r := range p.Resources {
			if opts.Provider != "" && !strings.EqualFold(InferProvider(r.Type), opts.Provider) {
				continue
			}
			if opts.CostsOnly && r.IsFree {
				continue
			}
			filtered.Resources = append(filtered.Resources, r)
		}

		out.Projects = append(out.Projects, filtered)
	}

	return out
}

func filterFailingFinops(policies []format.FinopsOutput) []format.FinopsOutput {
	var out []format.FinopsOutput
	for _, p := range policies {
		if len(p.FailingResources) > 0 {
			out = append(out, p)
		}
	}
	return out
}

func filterFailingTagging(policies []format.TaggingOutput) []format.TaggingOutput {
	var out []format.TaggingOutput
	for _, p := range policies {
		if len(p.FailingResources) > 0 {
			out = append(out, p)
		}
	}
	return out
}
