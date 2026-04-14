package health

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Status represents the outcome of a health check.
type Status int

const (
	StatusPass    Status = iota
	StatusWarning
	StatusFail
	StatusSkipped
)

// Result holds the outcome of a single check.
type Result struct {
	Status  Status
	Label   string   // display label (e.g. "Credentials found")
	Detail  string   // extra info appended after the label (e.g. "(142 ms)")
	Hint    string   // remediation shown on the next line with →
	Verbose []string // extra diagnostic lines, shown only with --verbose
	Fixable bool     // true if auto-remediation is available for this check
}

// Check defines a single health check.
type Check struct {
	Name     string // display label when passing
	FailName string // display label when failing (falls back to Name)

	// DependsOn lists indices (within the same category) of checks that must
	// pass before this one runs. If any dependency did not pass, this check
	// is automatically skipped.
	DependsOn []int

	// Run executes the check. It is only called when all dependencies passed.
	Run func(ctx context.Context) Result

	// Fix attempts to auto-remediate a failed or warning check. If nil, the
	// check is not fixable. Fix is only called when --fix is set and the
	// check did not pass.
	Fix func(ctx context.Context) error
}

// Category groups related checks under a heading.
type Category struct {
	Name   string
	Checks []Check
}

// CategoryResult holds the results for a single category.
type CategoryResult struct {
	Name    string
	Results []Result
}

// Report holds the full health check output.
type Report struct {
	Categories []CategoryResult
}

func (r *Report) Total() int {
	n := 0
	for _, c := range r.Categories {
		n += len(c.Results)
	}
	return n
}

func (r *Report) count(s Status) int {
	n := 0
	for _, c := range r.Categories {
		for _, res := range c.Results {
			if res.Status == s {
				n++
			}
		}
	}
	return n
}

func (r *Report) Passed() int   { return r.count(StatusPass) }
func (r *Report) Warnings() int { return r.count(StatusWarning) }
func (r *Report) Failed() int   { return r.count(StatusFail) }
func (r *Report) Skipped() int  { return r.count(StatusSkipped) }

func (r *Report) HasFixable() bool {
	for _, c := range r.Categories {
		for _, res := range c.Results {
			if res.Fixable {
				return true
			}
		}
	}
	return false
}

// RunChecks executes all categories and their checks, respecting dependencies.
func RunChecks(ctx context.Context, categories []Category) *Report {
	report := &Report{}
	for _, cat := range categories {
		catResult := CategoryResult{Name: cat.Name}
		results := make([]Result, len(cat.Checks))

		for i, check := range cat.Checks {
			skip := false
			skipReason := ""
			for _, dep := range check.DependsOn {
				if dep < i && results[dep].Status != StatusPass {
					skip = true
					skipReason = results[dep].Label
					break
				}
			}

			if skip {
				results[i] = Result{
					Status: StatusSkipped,
					Label:  check.Name,
					Hint:   fmt.Sprintf("skipped (%s)", skipReason),
				}
			} else {
				result := check.Run(ctx)
				if result.Label == "" {
					if result.Status == StatusFail && check.FailName != "" {
						result.Label = check.FailName
					} else {
						result.Label = check.Name
					}
				}
				if result.Status != StatusPass && check.Fix != nil {
					result.Fixable = true
				}
				results[i] = result
			}
		}

		catResult.Results = results
		report.Categories = append(report.Categories, catResult)
	}
	return report
}

// RunFixes attempts auto-remediation for fixable checks, then re-runs all
// checks and returns a new report.
func RunFixes(ctx context.Context, w io.Writer, categories []Category, report *Report) *Report {
	_, _ = fmt.Fprintf(w, "\nAttempting auto-fix...\n")

	for ci, cat := range categories {
		for i, check := range cat.Checks {
			res := report.Categories[ci].Results[i]
			if !res.Fixable || check.Fix == nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "  Fixing: %s...\n", check.Name)
			if err := check.Fix(ctx); err != nil {
				_, _ = fmt.Fprintf(w, "  ✗ Failed to fix %s: %s\n", check.Name, err)
			} else {
				_, _ = fmt.Fprintf(w, "  ✓ Fixed: %s\n", check.Name)
			}
		}
	}

	_, _ = fmt.Fprintf(w, "\nRe-running checks...\n")
	return RunChecks(ctx, categories)
}

func statusIcon(s Status) string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusWarning:
		return "!"
	case StatusFail:
		return "✗"
	case StatusSkipped:
		return "⊘"
	default:
		return "?"
	}
}

// Render writes the formatted health check report to w.
func Render(w io.Writer, report *Report, ver string, verbose bool, fix bool) {
	_, _ = fmt.Fprintf(w, "Infracost Health %s - running %d checks\n", ver, report.Total())

	for _, cat := range report.Categories {
		_, _ = fmt.Fprintf(w, "\n%s\n", cat.Name)
		for _, r := range cat.Results {
			label := r.Label
			if r.Detail != "" {
				label += " " + r.Detail
			}
			_, _ = fmt.Fprintf(w, "  %s %s\n", statusIcon(r.Status), label)
			if r.Hint != "" {
				_, _ = fmt.Fprintf(w, "    → %s\n", r.Hint)
			}
			if verbose {
				for _, line := range r.Verbose {
					_, _ = fmt.Fprintf(w, "    %s\n", line)
				}
			}
		}
	}

	_, _ = fmt.Fprintln(w)
	var parts []string
	if n := report.Passed(); n > 0 {
		parts = append(parts, fmt.Sprintf("✓ %d passed", n))
	}
	if n := report.Warnings(); n > 0 {
		parts = append(parts, fmt.Sprintf("! %d warning", n))
	}
	if n := report.Failed(); n > 0 {
		parts = append(parts, fmt.Sprintf("✗ %d issue", n))
	}
	if n := report.Skipped(); n > 0 {
		parts = append(parts, fmt.Sprintf("⊘ %d skipped", n))
	}
	_, _ = fmt.Fprintf(w, "  %s\n", strings.Join(parts, "  "))

	if !fix && report.HasFixable() {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Run infracost health --fix to attempt auto-remediation")
	}
}
