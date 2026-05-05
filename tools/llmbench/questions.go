package main

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/infracost/cli/internal/format"
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
//
// The set is weighted toward detection (find the failures) and remediation
// (propose fixes) — the two task shapes where the agent skill matters most
// to real users. Trivia like "how many projects" was dropped intentionally.
func Questions() []Question {
	return []Question{
		// --- aggregation / lookup baseline ---
		{
			ID:       "q1-total-cost",
			Category: "aggregation",
			Prompt:   "What is the total monthly cost across all projects? Reply with just the dollar amount, no explanation.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictNumeric(ans, totalMonthlyCost(f), 500)
			},
		},
		{
			// Companion to q1-total-cost that explicitly forbids using
			// infracost or any cost-estimation tool. Used to measure how
			// well the model can estimate AWS spend purely from its training
			// knowledge of the resources defined in the .tf files. We give
			// the verifier a wider tolerance ($5k) since the answer is an
			// estimate, not a precise figure.
			ID:       "q1-total-cost-estimated",
			Category: "aggregation",
			Prompt:   "What is the estimated total monthly cost across all projects? You do NOT have access to `infracost` or any pricing API. Use your knowledge of typical AWS pricing for the resources defined in the .tf files in your current working directory. Reply with just a single dollar amount, no explanation.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictNumeric(ans, totalMonthlyCost(f), 5000)
			},
		},
		{
			ID:       "q3-most-expensive-resource",
			Category: "filtering",
			Prompt:   "Which single resource has the highest total monthly cost? Reply with just its address (the value of the `name` field), no other text.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictStringContains(ans, mostExpensiveResource(f))
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
				// Multiple resources can tie for the top savings value. Accept
				// any of them — picking one arbitrarily would make the verdict
				// depend on Go map iteration order in the helper.
				return verdictContainsAny(ans, largestSavingsResources(f))
			},
		},
		// --- detection: find the issues ---
		{
			ID:       "d1-resources-failing-tagging",
			Category: "detection",
			Prompt:   "How many distinct resources fail at least one tagging policy (deduplicated by resource address across all tagging policies and projects)? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictInt(ans, distinctFailingTaggingResources(f))
			},
		},
		{
			ID:       "d2-resources-failing-finops",
			Category: "detection",
			Prompt:   "How many distinct resources fail at least one FinOps policy (deduplicated by resource address across all FinOps policies and projects)? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictInt(ans, distinctFailingFinopsResources(f))
			},
		},
		{
			ID:       "d3-tag-key-missing-most",
			Category: "detection",
			Prompt:   "Across all failing tagging-policy results, which mandatory tag key is missing from the most resources? Reply with just the tag key.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictStringContains(ans, mostMissingMandatoryTag(f))
			},
		},
		{
			ID:       "d4-list-resources-failing-tagging",
			Category: "detection",
			Prompt:   "List every resource address that fails any tagging policy. Reply with one address per line, nothing else.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictListMatch(ans, allFailingTaggingAddresses(f))
			},
		},
		{
			ID:       "q7-resources-failing-team-tag",
			Category: "detection",
			Prompt:   "According to the tagging policy, how many resources fail the `team` tag requirement — either missing the `team` tag entirely, or having a value outside the allowed list (frontend, platform, prodsec, networkops, dataops)? Reply with just the integer.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictInt(ans, resourcesFailingTeamTag(f))
			},
		},
		{
			ID:       "d5-finops-total-savings",
			Category: "detection",
			Prompt:   "What is the total potential monthly savings if every FinOps issue were resolved (sum of `monthly_savings` across all issues)? Reply with just the dollar amount.",
			Verify: func(ans string, f *format.Output) Verdict {
				return verdictNumeric(ans, totalFinopsSavings(f), 500)
			},
		},
		// --- fix tasks: scored on token usage only, not quality ---
		{
			ID:       "f1-fix-all-tagging",
			Category: "fix",
			Prompt:   "Propose Terraform source-code changes that would make every failing tagging policy pass. Reply with a unified diff (`diff --git ...` blocks).",
			Verify:   verifyTokenOnly,
		},
		{
			ID:       "f2-fix-top-finops",
			Category: "fix",
			Prompt:   "Propose Terraform source-code changes for the top 3 highest-savings FinOps issues. Reply with a unified diff (`diff --git ...` blocks).",
			Verify:   verifyTokenOnly,
		},
		{
			ID:       "f3-add-missing-tags",
			Category: "fix",
			Prompt:   "For each resource that is missing required tags, write the tag block to add. Group output by resource address.",
			Verify:   verifyTokenOnly,
		},
		{
			ID:       "q11-propose-cost-fixes",
			Category: "fix",
			Prompt:   "Propose the top 3 cost-saving changes for this infrastructure as a unified diff (`diff --git ...` blocks). Focus on the highest-impact wins only.",
			Verify:   verifyTokenOnly,
		},
		{
			ID:       "q12-propose-tag-fixes",
			Category: "fix",
			Prompt:   "Propose changes to make every resource compliant with the project's tagging policy. Reply with a unified diff (`diff --git ...` blocks).",
			Verify:   verifyTokenOnly,
		},
	}
}

// verifyTokenOnly is the no-op verifier for fix-task questions. We measure
// how many tokens the model spends attempting a fix, not whether the fix is
// correct. Returning Ambiguous keeps the row in the report without skewing
// accuracy aggregates.
func verifyTokenOnly(_ string, _ *format.Output) Verdict { return Ambiguous }

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

// teamTagAllowedValues mirrors the tagging policy supplied via
// --policy-context-file. Hardcoded here so the q7 verifier can score
// against the *real* policy rather than just "tag absent". If the policy
// changes upstream, update this list to match (or extract to config).
var teamTagAllowedValues = map[string]bool{
	"frontend":   true,
	"platform":   true,
	"prodsec":    true,
	"networkops": true,
	"dataops":    true,
}

// resourcesFailingTeamTag counts resources that either lack a `team` tag
// entirely or carry a value outside the allowed-values list.
func resourcesFailingTeamTag(f *format.Output) int {
	n := 0
	for _, p := range f.Projects {
		for _, r := range p.Resources {
			v, ok := r.Tags["team"]
			if !ok || !teamTagAllowedValues[v] {
				n++
			}
		}
	}
	return n
}

// distinctFailingTaggingResources counts resource addresses appearing in at
// least one tagging policy's FailingResources, across all projects, with
// resource addresses deduplicated.
func distinctFailingTaggingResources(f *format.Output) int {
	seen := map[string]struct{}{}
	for _, p := range f.Projects {
		for _, t := range p.TaggingResults {
			for _, fr := range t.FailingResources {
				seen[fr.Address] = struct{}{}
			}
		}
	}
	return len(seen)
}

// distinctFailingFinopsResources is the FinOps-side equivalent of the
// tagging counter — distinct resource names failing any FinOps policy.
func distinctFailingFinopsResources(f *format.Output) int {
	seen := map[string]struct{}{}
	for _, p := range f.Projects {
		for _, fp := range p.FinopsResults {
			for _, fr := range fp.FailingResources {
				seen[fr.Name] = struct{}{}
			}
		}
	}
	return len(seen)
}

// mostMissingMandatoryTag returns the tag key that appears most frequently
// in any FailingTaggingResourceOutput.MissingMandatoryTags. Ties broken
// alphabetically for determinism.
func mostMissingMandatoryTag(f *format.Output) string {
	counts := map[string]int{}
	for _, p := range f.Projects {
		for _, t := range p.TaggingResults {
			for _, fr := range t.FailingResources {
				for _, key := range fr.MissingMandatoryTags {
					counts[key]++
				}
			}
		}
	}
	type kv struct {
		k string
		n int
	}
	rows := make([]kv, 0, len(counts))
	for k, n := range counts {
		rows = append(rows, kv{k, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].k < rows[j].k
	})
	if len(rows) == 0 {
		return ""
	}
	return rows[0].k
}

// allFailingTaggingAddresses returns the deduplicated set of resource
// addresses failing any tagging policy. Used by verdictListMatch.
func allFailingTaggingAddresses(f *format.Output) []string {
	seen := map[string]struct{}{}
	for _, p := range f.Projects {
		for _, t := range p.TaggingResults {
			for _, fr := range t.FailingResources {
				seen[fr.Address] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// totalFinopsSavings sums MonthlySavings across every FinOps issue.
func totalFinopsSavings(f *format.Output) float64 {
	total := rat.Zero
	for _, p := range f.Projects {
		for _, fp := range p.FinopsResults {
			for _, fr := range fp.FailingResources {
				for _, iss := range fr.Issues {
					if iss.MonthlySavings == nil {
						continue
					}
					total = total.Add(iss.MonthlySavings)
				}
			}
		}
	}
	return total.Float64()
}

// largestSavingsResources returns every address tied for the highest total
// monthly_savings across all FinOps issues. We return a list (not a single
// pick) because callers verifying free-form model answers should accept any
// of the tied winners — otherwise the verdict depends on Go map iteration
// order, which is randomized.
func largestSavingsResources(f *format.Output) []string {
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
	bestVal := math.Inf(-1)
	for _, v := range totals {
		if v > bestVal {
			bestVal = v
		}
	}
	var winners []string
	for addr, v := range totals {
		if v == bestVal {
			winners = append(winners, addr)
		}
	}
	sort.Strings(winners)
	return winners
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

// verdictContainsAny passes if ans contains at least one of the wants.
// Used for questions with multiple valid answers (e.g. ties).
func verdictContainsAny(ans string, wants []string) Verdict {
	if len(wants) == 0 {
		return Ambiguous
	}
	trimmed := strings.TrimSpace(ans)
	for _, w := range wants {
		if w != "" && strings.Contains(trimmed, w) {
			return Correct
		}
	}
	return Incorrect
}

// addressLikeRe roughly matches Terraform resource addresses (`type.name`,
// `module.foo.type.name`, plus optional `[...]` for_each / count indices on
// any segment, e.g. `aws_eks_cluster.main["prod"]`). Permissive enough to
// catch backticked or quoted variants in free-form replies.
var addressLikeRe = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*(?:\[[^\]]*\])?(?:\.[a-zA-Z_][a-zA-Z0-9_]*(?:\[[^\]]*\])?)+`)

// verdictListMatch checks that every expected address appears somewhere in
// the answer. Order is irrelevant; surrounding prose is tolerated. Extra
// addresses in the answer (false positives) are tolerated too — we'd
// rather count partial credit toward the model than fail the verifier on
// formatting noise. If you want a strict superset check, switch to set
// equality once we have a better grasp of how chatty replies actually are.
func verdictListMatch(ans string, want []string) Verdict {
	if len(want) == 0 {
		return Ambiguous
	}
	got := map[string]struct{}{}
	for _, m := range addressLikeRe.FindAllString(ans, -1) {
		got[m] = struct{}{}
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			return Incorrect
		}
	}
	return Correct
}

func extractInt(s string) (int, bool) {
	// Strip non-digit/sign characters from each token until we find one that
	// parses cleanly. Catches "12 resources", "There are 12.", "12,345",
	// "`field: 12`" (backticks/quotes/colons/parens), etc.
	cleaned := strings.NewReplacer(",", "", ".", " ").Replace(s)
	for _, tok := range strings.Fields(cleaned) {
		tok = strings.Trim(tok, "`\"'():;[]{}")
		if n, err := strconv.Atoi(tok); err == nil {
			return n, true
		}
	}
	return 0, false
}

func extractFloat(s string) (float64, bool) {
	cleaned := strings.NewReplacer("$", " ", ",", "").Replace(s)
	for _, tok := range strings.Fields(cleaned) {
		// strip surrounding punctuation/quoting (backticks, quotes, parens, colons…)
		tok = strings.Trim(tok, "`\"'():;[]{}")
		tok = strings.TrimRight(tok, ".")
		if f, err := strconv.ParseFloat(tok, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

