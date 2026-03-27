package inspect

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
)

type projectSummary struct {
	Name                  string   `json:"name"`
	Path                  string   `json:"path"`
	Resources             int      `json:"resources"`
	MonthlyCost           *rat.Rat `json:"monthly_cost"`
	FinopsPolicies        int      `json:"finops_policies"`
	FinopsFailingPolicies int      `json:"finops_failing_policies"`
	TaggingPolicies       int      `json:"tagging_policies"`
	HasErrors             bool     `json:"has_errors"`
}

type summaryData struct {
	Projects          int              `json:"projects"`
	ProjectsWithError int              `json:"projects_with_errors"`
	ProjectDetails    []projectSummary `json:"project_details"`
	Resources         int              `json:"resources"`
	CostedResources   int              `json:"costed_resources"`
	FreeResources     int              `json:"free_resources"`
	MonthlyCost       *rat.Rat         `json:"monthly_cost"`
	FinopsPolicies    int              `json:"finops_policies"`
	FailingPolicies   int              `json:"failing_policies"`
	TaggingPolicies   int              `json:"tagging_policies"`
	Guardrails        int              `json:"guardrails"`
	TriggeredGuardrails int            `json:"triggered_guardrails"`
	CriticalDiags     int              `json:"critical_diagnostics"`
	WarningDiags      int              `json:"warning_diagnostics"`
}

func ResourceCost(r *format.ResourceOutput) *rat.Rat {
	total := rat.Zero
	for _, cc := range r.CostComponents {
		if cc.TotalMonthlyCost != nil {
			total = total.Add(cc.TotalMonthlyCost)
		}
	}
	for _, sub := range r.Subresources {
		total = total.Add(ResourceCost(&sub))
	}
	return total
}

func WriteSummary(w io.Writer, data *format.Output, asJSON bool) error {
	s := buildSummary(data)

	if asJSON {
		b, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}

	projectLine := fmt.Sprintf("Projects: %d", s.Projects)
	if s.ProjectsWithError > 0 {
		projectLine += fmt.Sprintf(" (%d with errors)", s.ProjectsWithError)
	}
	_, _ = fmt.Fprintln(w, projectLine)

	_, _ = fmt.Fprintln(w)
	if err := writeTable(w, []string{"Project", "Resources", "Monthly Cost", "FinOps", "Tagging"}, func(add func(row []string)) {
		for _, ps := range s.ProjectDetails {
			name := ps.Name
			if ps.HasErrors {
				name += " (!)"
			}
			finops := fmt.Sprintf("%d", ps.FinopsPolicies)
			if ps.FinopsFailingPolicies > 0 {
				finops += fmt.Sprintf(" (%d failing)", ps.FinopsFailingPolicies)
			}
			add([]string{
				name,
				fmt.Sprintf("%d", ps.Resources),
				"$" + ps.MonthlyCost.StringFixed(2),
				finops,
				fmt.Sprintf("%d", ps.TaggingPolicies),
			})
		}
	}); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w)

	resourceLine := fmt.Sprintf("Resources: %d", s.Resources)
	if s.CostedResources > 0 || s.FreeResources > 0 {
		resourceLine += fmt.Sprintf(" (%d costed, %d free)", s.CostedResources, s.FreeResources)
	}
	_, _ = fmt.Fprintln(w, resourceLine)

	_, _ = fmt.Fprintf(w, "Monthly cost: $%s\n", s.MonthlyCost.StringFixed(2))

	_, _ = fmt.Fprintf(w, "FinOps policies: %d (%d failing)\n", s.FinopsPolicies, s.FailingPolicies)
	_, _ = fmt.Fprintf(w, "Tagging policies: %d\n", s.TaggingPolicies)
	_, _ = fmt.Fprintf(w, "Guardrails: %d (%d triggered)\n", s.Guardrails, s.TriggeredGuardrails)

	if s.CriticalDiags > 0 {
		_, _ = fmt.Fprintf(w, "Diagnostics: %d critical", s.CriticalDiags)
		if s.WarningDiags > 0 {
			_, _ = fmt.Fprintf(w, ", %d warning", s.WarningDiags)
		}
		_, _ = fmt.Fprintln(w)
	} else if s.WarningDiags > 0 {
		_, _ = fmt.Fprintf(w, "Diagnostics: %d warning\n", s.WarningDiags)
	}

	return nil
}

func buildSummary(data *format.Output) summaryData {
	s := summaryData{MonthlyCost: rat.Zero}

	for _, p := range data.Projects {
		s.Projects++
		ps := projectSummary{
			Name:        p.ProjectName,
			Path:        p.Path,
			MonthlyCost: rat.Zero,
		}

		if len(p.Diagnostics) > 0 {
			hasCritical := false
			for _, d := range p.Diagnostics {
				switch d.Severity {
				case "critical":
					hasCritical = true
					s.CriticalDiags++
				case "warning":
					s.WarningDiags++
				}
			}
			if hasCritical {
				s.ProjectsWithError++
				ps.HasErrors = true
			}
		}

		for _, r := range p.Resources {
			s.Resources++
			ps.Resources++
			if r.IsFree {
				s.FreeResources++
			} else {
				s.CostedResources++
			}
			cost := ResourceCost(&r)
			s.MonthlyCost = s.MonthlyCost.Add(cost)
			ps.MonthlyCost = ps.MonthlyCost.Add(cost)
		}

		for _, f := range p.FinopsResults {
			s.FinopsPolicies++
			ps.FinopsPolicies++
			if len(f.FailingResources) > 0 {
				s.FailingPolicies++
				ps.FinopsFailingPolicies++
			}
		}

		ps.TaggingPolicies = len(p.TaggingResults)
		s.TaggingPolicies += ps.TaggingPolicies

		s.ProjectDetails = append(s.ProjectDetails, ps)
	}

	for _, gr := range data.GuardrailResults {
		s.Guardrails++
		if gr.Triggered {
			s.TriggeredGuardrails++
		}
	}

	return s
}
