// Package fixturegen synthesizes representative format.Output values at
// configurable scales. It exists so the TOON token-cost benchmarks (and the
// downstream LLM-evaluation harness in tools/llmbench) share one source of
// truth for "what does a small/medium/large infracost report look like".
package fixturegen

import (
	"fmt"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
)

// Size names the predefined fixture scales.
type Size string

const (
	Small  Size = "small"
	Medium Size = "medium"
	Large  Size = "large"
)

// Spec describes the shape of a fixture: how many projects, how many resources
// each project has, how many cost components per resource, and how many
// policy results to attach. Real infracost outputs vary widely; these are
// representative working sizes.
type Spec struct {
	Projects        int
	ResPerProject   int
	CompsPerRes     int
	FinopsPolicies  int
	TaggingPolicies int
	Guardrails      int
	Budgets         int
}

func SpecFor(s Size) Spec {
	switch s {
	case Small:
		return Spec{Projects: 1, ResPerProject: 5, CompsPerRes: 2, FinopsPolicies: 1, TaggingPolicies: 1, Guardrails: 1, Budgets: 1}
	case Medium:
		return Spec{Projects: 3, ResPerProject: 25, CompsPerRes: 3, FinopsPolicies: 3, TaggingPolicies: 2, Guardrails: 2, Budgets: 2}
	case Large:
		return Spec{Projects: 8, ResPerProject: 60, CompsPerRes: 4, FinopsPolicies: 5, TaggingPolicies: 3, Guardrails: 3, Budgets: 3}
	}
	return Spec{}
}

// Build returns a deterministic format.Output matching the given spec. The
// content is plausible-looking (typical AWS resource types, realistic-looking
// monthly costs) but synthesized — not derived from any real account.
func Build(spec Spec) *format.Output {
	out := &format.Output{Currency: "USD"}

	for p := 0; p < spec.Projects; p++ {
		project := format.ProjectOutput{
			ProjectName: fmt.Sprintf("project-%d", p+1),
			Path:        fmt.Sprintf("/infrastructure/project-%d", p+1),
		}

		for r := 0; r < spec.ResPerProject; r++ {
			rt := resourceType(r)
			res := format.ResourceOutput{
				Name:         fmt.Sprintf("%s.resource_%d", rt, r+1),
				Type:         rt,
				IsSupported:  true,
				IsFree:       r%11 == 0, // ~9% free
				SupportsTags: true,
				Tags: map[string]string{
					"environment": envFor(p),
					"team":        teamFor(r),
					"costCenter":  fmt.Sprintf("cc-%d", 100+(r%7)),
				},
				Metadata: format.ResourceMetadata{
					Filename:  fmt.Sprintf("modules/%s/main.tf", moduleFor(r)),
					StartLine: 10 + (r%50)*3,
					EndLine:   10 + (r%50)*3 + 12,
				},
			}
			if !res.IsFree {
				comps := make([]format.CostComponentOutput, 0, spec.CompsPerRes)
				for c := 0; c < spec.CompsPerRes; c++ {
					comps = append(comps, format.CostComponentOutput{
						Name:             componentName(c),
						Unit:             componentUnit(c),
						Price:            rat.New(0.001 * float64((r+1)*(c+1))),
						Quantity:         rat.New(int64((r + 1) * 100)),
						BaseMonthlyCost:  rat.New(0.5 * float64((r+1)*(c+1))),
						UsageMonthlyCost: rat.New(0.25 * float64((r+1)*(c+1))),
						TotalMonthlyCost: rat.New(0.75 * float64((r+1)*(c+1))),
					})
				}
				res.CostComponents = comps
			}
			project.Resources = append(project.Resources, res)
		}

		for f := 0; f < spec.FinopsPolicies; f++ {
			policy := format.FinopsOutput{
				PolicyID:      fmt.Sprintf("finops-policy-%d", f+1),
				PolicyName:    finopsPolicyName(f),
				PolicySlug:    finopsPolicySlug(f),
				PolicyMessage: "Consider remediation to reduce monthly cost.",
			}
			// Fail every fifth resource against the first policy, fewer for the
			// rest — keeps the data realistic without making everything fail.
			failureMod := 5 + f
			for r := 0; r < spec.ResPerProject; r++ {
				if r%failureMod != 0 {
					continue
				}
				rt := resourceType(r)
				policy.FailingResources = append(policy.FailingResources, format.FinopsFailingResourceOutput{
					Name: fmt.Sprintf("%s.resource_%d", rt, r+1),
					Issues: []format.FinopsIssueOutput{
						{
							Description:    fmt.Sprintf("Suboptimal %s configuration detected.", rt),
							MonthlySavings: rat.New(2.5 * float64(r+1)),
							Address:        fmt.Sprintf("%s.resource_%d", rt, r+1),
							Attribute:      finopsAttribute(f),
						},
					},
				})
			}
			project.FinopsResults = append(project.FinopsResults, policy)
		}

		for tIdx := 0; tIdx < spec.TaggingPolicies; tIdx++ {
			policy := format.TaggingOutput{
				PolicyID:   fmt.Sprintf("tagging-policy-%d", tIdx+1),
				PolicyName: taggingPolicyName(tIdx),
				Message:    "Resources must carry mandatory tags.",
				TagSchema: []format.TagSchemaEntry{
					{Key: "owner", Mandatory: true},
					{Key: "lifecycle", Mandatory: true},
					{
						Key:         "environment",
						ValidValues: []string{"production", "staging", "development", "qa"},
						Message:     "environment must be one of the standard values",
					},
				},
			}
			failureMod := 7 + tIdx
			for r := 0; r < spec.ResPerProject; r++ {
				if r%failureMod != 0 {
					continue
				}
				rt := resourceType(r)
				failing := format.FailingTaggingResourceOutput{
					Address:              fmt.Sprintf("%s.resource_%d", rt, r+1),
					ResourceType:         rt,
					Path:                 fmt.Sprintf("modules/%s/main.tf", moduleFor(r)),
					Line:                 10 + (r%50)*3,
					MissingMandatoryTags: []string{"owner", "lifecycle"},
				}
				// Every other failing resource also has an invalid (present
				// but wrong) `environment` value, exercising the per-instance
				// InvalidTag path that benefits most from schema dedup.
				if r%2 == 0 {
					failing.InvalidTags = []format.InvalidTagOutput{
						{Key: "environment", Value: invalidEnvFor(r), Suggestion: "production"},
					}
				}
				policy.FailingResources = append(policy.FailingResources, failing)
			}
			project.TaggingResults = append(project.TaggingResults, policy)
		}

		out.Projects = append(out.Projects, project)
	}

	for g := 0; g < spec.Guardrails; g++ {
		out.GuardrailResults = append(out.GuardrailResults, format.GuardrailOutput{
			GuardrailID:      fmt.Sprintf("guardrail-%d", g+1),
			GuardrailName:    fmt.Sprintf("Cost increase > $%d", 100*(g+1)),
			Triggered:        g%2 == 0,
			TotalMonthlyCost: rat.New(int64(150 * (g + 1))),
		})
	}

	for b := 0; b < spec.Budgets; b++ {
		amount := int64(1000 * (b + 1))
		current := int64(int(amount/3) * (b + 1))
		out.BudgetResults = append(out.BudgetResults, format.BudgetOutput{
			BudgetID:    fmt.Sprintf("budget-%d", b+1),
			BudgetName:  fmt.Sprintf("Q%d budget", (b%4)+1),
			Tags:        []format.BudgetTagOutput{{Key: "team", Value: teamFor(b)}},
			Amount:      rat.New(amount),
			CurrentCost: rat.New(current),
			OverBudget:  current > amount,
		})
	}

	return out
}

// --- naming helpers --------------------------------------------------------

func resourceType(i int) string {
	types := []string{
		"aws_instance",
		"aws_s3_bucket",
		"aws_ebs_volume",
		"aws_lambda_function",
		"aws_rds_cluster",
		"aws_dynamodb_table",
		"aws_cloudfront_distribution",
		"aws_elasticache_cluster",
		"aws_ecs_service",
		"aws_eks_cluster",
		"google_compute_instance",
		"google_storage_bucket",
		"azurerm_virtual_machine",
	}
	return types[i%len(types)]
}

func componentName(idx int) string {
	common := []string{"Instance usage", "Storage", "Data transfer", "Requests"}
	return common[idx%len(common)]
}

func componentUnit(idx int) string {
	units := []string{"hours", "GB-months", "GB", "1M requests"}
	return units[idx%len(units)]
}

func envFor(p int) string {
	envs := []string{"production", "staging", "development", "qa"}
	return envs[p%len(envs)]
}

func teamFor(i int) string {
	teams := []string{"platform", "frontend", "data", "ml", "infra", "billing"}
	return teams[i%len(teams)]
}

func moduleFor(i int) string {
	modules := []string{"compute", "storage", "network", "data", "edge", "queue"}
	return modules[i%len(modules)]
}

func finopsPolicyName(i int) string {
	names := []string{
		"Right-size EC2 instances",
		"Use GP3 volumes",
		"Move to Spot for non-prod",
		"Delete unattached EBS volumes",
		"Compress S3 lifecycle data",
	}
	return names[i%len(names)]
}

func finopsPolicySlug(i int) string {
	slugs := []string{"right-size-ec2", "use-gp3", "use-spot-non-prod", "delete-unattached-ebs", "s3-lifecycle"}
	return slugs[i%len(slugs)]
}

func finopsAttribute(i int) string {
	attrs := []string{"instance_type", "type", "lifecycle", "size", "transition"}
	return attrs[i%len(attrs)]
}

func taggingPolicyName(i int) string {
	names := []string{"Required Tags", "Cost allocation tags", "Owner tag policy"}
	return names[i%len(names)]
}

func invalidEnvFor(i int) string {
	bad := []string{"prod", "Stage", "DEV", "test", "stg"}
	return bad[i%len(bad)]
}
