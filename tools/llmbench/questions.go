package main

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/format/fixturegen"
	"github.com/infracost/go-proto/pkg/rat"
)

// Question is a single benchmarkable prompt with a way to score the answer
// against ground truth derived from the fixture. The ID is stable across
// runs so we can correlate cells.
type Question struct {
	ID       string
	Category string // retrieval | aggregation | filtering | structure
	Prompt   string
	// Verify takes the model's free-text answer and returns whether it's
	// correct. Implementations should be tolerant to formatting (commas,
	// dollar signs, "approximately", etc.) but strict on the underlying
	// value.
	Verify func(answer string, fixture *format.Output) Verdict
}

type Verdict string

const (
	Correct   Verdict = "correct"
	Incorrect Verdict = "incorrect"
	Ambiguous Verdict = "ambiguous"
)

// Questions returns the canonical question set. Each fixture size shares the
// same set; verification computes ground truth from the fixture at run time.
func Questions() []Question {
	return []Question{
		{
			ID:       "q1-total-cost",
			Category: "aggregation",
			Prompt:   "What is the total monthly cost across all projects? Reply with just the dollar amount, no explanation.",
			Verify: func(ans string, f *format.Output) Verdict {
				want := totalMonthlyCost(f)
				return verdictNumeric(ans, want, 0.05) // 5¢ tolerance
			},
		},
		{
			ID:       "q2-resource-count",
			Category: "aggregation",
			Prompt:   "How many resources are there in total across all projects? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				want := totalResources(f)
				return verdictInt(ans, want)
			},
		},
		{
			ID:       "q3-most-expensive-resource",
			Category: "filtering",
			Prompt:   "Which single resource has the highest total monthly cost? Reply with just its address (the value of the `name` field), no other text.",
			Verify: func(ans string, f *format.Output) Verdict {
				want := mostExpensiveResource(f)
				return verdictStringContains(ans, want)
			},
		},
		{
			ID:       "q4-project-count",
			Category: "structure",
			Prompt:   "How many projects are in this scan? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictInt(ans, len(f.Projects))
			},
		},
		{
			ID:       "q5-failing-finops-policies",
			Category: "filtering",
			Prompt:   "How many distinct FinOps policies have at least one failing resource (counted across all projects, deduplicated by policy name)? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictInt(ans, distinctFailingFinopsPolicies(f))
			},
		},
		{
			ID:       "q6-triggered-guardrails",
			Category: "retrieval",
			Prompt:   "How many guardrails are currently triggered? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				n := 0
				for _, g := range f.GuardrailResults {
					if g.Triggered {
						n++
					}
				}
				return verdictInt(ans, n)
			},
		},
		{
			ID:       "q7-resources-missing-team-tag",
			Category: "filtering",
			Prompt:   "How many resources are missing the `team` tag? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				n := 0
				for _, p := range f.Projects {
					for _, r := range p.Resources {
						if _, ok := r.Tags["team"]; !ok {
							n++
						}
					}
				}
				return verdictInt(ans, n)
			},
		},
		{
			ID:       "q8-most-common-resource-type",
			Category: "aggregation",
			Prompt:   "Which resource type appears most frequently across all projects? Reply with just the type name.",
			Verify: func(ans string, f *format.Output) Verdict {
				want := mostCommonResourceType(f)
				return verdictStringContains(ans, want)
			},
		},
		{
			ID:       "q9-over-budget-count",
			Category: "filtering",
			Prompt:   "How many budgets are over their limit? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				n := 0
				for _, b := range f.BudgetResults {
					if b.OverBudget {
						n++
					}
				}
				return verdictInt(ans, n)
			},
		},
		{
			ID:       "q10-largest-savings-resource",
			Category: "aggregation",
			Prompt:   "Across all FinOps issues, which resource has the largest total `monthly_savings` value? Reply with just the resource address (the `address` field on the issue).",
			Verify: func(ans string, f *format.Output) Verdict {
				want := largestSavingsResource(f)
				return verdictStringContains(ans, want)
			},
		},
	}
}

// FilterQuestions returns the subset whose ID contains substring (or all if
// substring is empty).
func FilterQuestions(questions []Question, substring string) []Question {
	if substring == "" {
		return questions
	}
	out := make([]Question, 0)
	for _, q := range questions {
		if strings.Contains(q.ID, substring) {
			out = append(out, q)
		}
	}
	return out
}

// --- ground-truth computations ---------------------------------------------

func totalMonthlyCost(f *format.Output) float64 {
	total := rat.Zero
	for _, p := range f.Projects {
		for _, r := range p.Resources {
			total = total.Add(resourceTotal(&r))
		}
	}
	return total.Float64()
}

func resourceTotal(r *format.ResourceOutput) *rat.Rat {
	total := rat.Zero
	for _, c := range r.CostComponents {
		if c.TotalMonthlyCost != nil {
			total = total.Add(c.TotalMonthlyCost)
		}
	}
	for _, sr := range r.Subresources {
		total = total.Add(resourceTotal(&sr))
	}
	return total
}

func totalResources(f *format.Output) int {
	n := 0
	for _, p := range f.Projects {
		n += len(p.Resources)
	}
	return n
}

func mostExpensiveResource(f *format.Output) string {
	var bestAddr string
	bestCost := math.Inf(-1)
	for _, p := range f.Projects {
		for _, r := range p.Resources {
			c := resourceTotal(&r).Float64()
			if c > bestCost {
				bestCost = c
				bestAddr = r.Name
			}
		}
	}
	return bestAddr
}

func distinctFailingFinopsPolicies(f *format.Output) int {
	seen := map[string]struct{}{}
	for _, p := range f.Projects {
		for _, fp := range p.FinopsResults {
			if len(fp.FailingResources) > 0 {
				seen[fp.PolicyName] = struct{}{}
			}
		}
	}
	return len(seen)
}

func mostCommonResourceType(f *format.Output) string {
	counts := map[string]int{}
	for _, p := range f.Projects {
		for _, r := range p.Resources {
			counts[r.Type]++
		}
	}
	type tc struct {
		t string
		n int
	}
	rows := make([]tc, 0, len(counts))
	for t, n := range counts {
		rows = append(rows, tc{t, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].t < rows[j].t
	})
	if len(rows) == 0 {
		return ""
	}
	return rows[0].t
}

func largestSavingsResource(f *format.Output) string {
	totals := map[string]float64{}
	for _, p := range f.Projects {
		for _, fp := range p.FinopsResults {
			for _, fr := range fp.FailingResources {
				for _, iss := range fr.Issues {
					if iss.MonthlySavings == nil {
						continue
					}
					totals[iss.Address] += iss.MonthlySavings.Float64()
				}
			}
		}
	}
	var bestAddr string
	bestVal := math.Inf(-1)
	for addr, v := range totals {
		if v > bestVal {
			bestVal = v
			bestAddr = addr
		}
	}
	return bestAddr
}

// --- verdict helpers -------------------------------------------------------

// verdictNumeric extracts the first floating-point number from ans and
// compares to want with the given absolute tolerance.
func verdictNumeric(ans string, want, tol float64) Verdict {
	n, ok := extractFloat(ans)
	if !ok {
		return Ambiguous
	}
	if math.Abs(n-want) <= tol {
		return Correct
	}
	return Incorrect
}

func verdictInt(ans string, want int) Verdict {
	n, ok := extractInt(ans)
	if !ok {
		return Ambiguous
	}
	if n == want {
		return Correct
	}
	return Incorrect
}

// verdictStringContains checks whether ans contains the want token. We use
// contains rather than exact match because models often pad with quotes,
// surrounding prose, or backticks.
func verdictStringContains(ans, want string) Verdict {
	if want == "" {
		return Ambiguous
	}
	if strings.Contains(strings.TrimSpace(ans), want) {
		return Correct
	}
	return Incorrect
}

func extractInt(s string) (int, bool) {
	// Strip non-digit/sign characters from each token until we find one that
	// parses cleanly. Catches "12 resources", "There are 12.", "12,345", etc.
	cleaned := strings.NewReplacer(",", "", ".", " ").Replace(s)
	for _, tok := range strings.Fields(cleaned) {
		if n, err := strconv.Atoi(tok); err == nil {
			return n, true
		}
	}
	return 0, false
}

func extractFloat(s string) (float64, bool) {
	cleaned := strings.NewReplacer("$", " ", ",", "").Replace(s)
	for _, tok := range strings.Fields(cleaned) {
		// trim trailing punctuation
		tok = strings.TrimRight(tok, ".:;)")
		if f, err := strconv.ParseFloat(tok, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// SizeFixture returns the format.Output for a given size, computed once and
// cached so verifiers can reuse the same instance.
type SizeFixture = struct {
	Size    fixturegen.Size
	Fixture *format.Output
}

func BuildFixtures(sizes []fixturegen.Size) []SizeFixture {
	out := make([]SizeFixture, 0, len(sizes))
	for _, s := range sizes {
		out = append(out, SizeFixture{
			Size:    s,
			Fixture: fixturegen.Build(fixturegen.SpecFor(s)),
		})
	}
	return out
}

// QuestionLabel returns "size/id" for use in result keys.
func QuestionLabel(size fixturegen.Size, id string) string {
	return fmt.Sprintf("%s/%s", size, id)
}
