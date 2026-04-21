package inspect

import (
	"strings"

	"github.com/infracost/cli/internal/format"
)

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
