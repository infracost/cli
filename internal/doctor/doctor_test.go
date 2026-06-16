package doctor_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/infracost/cli/internal/doctor"
)

func pass(label string) doctor.Check {
	return doctor.Check{
		Name: label,
		Run: func(_ context.Context) doctor.Result {
			return doctor.Result{Status: doctor.StatusPass}
		},
	}
}

func fail(label, failLabel string) doctor.Check {
	return doctor.Check{
		Name:     label,
		FailName: failLabel,
		Run: func(_ context.Context) doctor.Result {
			return doctor.Result{
				Status: doctor.StatusFail,
				Hint:   "fix it",
			}
		},
	}
}

func warn(label string) doctor.Check {
	return doctor.Check{
		Name: label,
		Run: func(_ context.Context) doctor.Result {
			return doctor.Result{Status: doctor.StatusWarning, Hint: "heads up"}
		},
	}
}

func dependent(label string, deps []int) doctor.Check {
	return doctor.Check{
		Name:      label,
		DependsOn: deps,
		Run: func(_ context.Context) doctor.Result {
			return doctor.Result{Status: doctor.StatusPass}
		},
	}
}

func TestRunChecks_AllPass(t *testing.T) {
	cats := []doctor.Category{
		{Name: "A", Checks: []doctor.Check{pass("one"), pass("two")}},
	}
	report := doctor.RunChecks(context.Background(), cats)

	assert.Equal(t, 2, report.Passed())
	assert.Equal(t, 0, report.Failed())
	assert.Equal(t, 0, report.Skipped())
	assert.Equal(t, 0, report.Warnings())
}

func TestRunChecks_DependencySkipping(t *testing.T) {
	cats := []doctor.Category{
		{
			Name: "Auth",
			Checks: []doctor.Check{
				fail("Credentials found", "No credentials found"),
				dependent("Token valid", []int{0}),
				dependent("Org accessible", []int{0}),
			},
		},
	}
	report := doctor.RunChecks(context.Background(), cats)

	assert.Equal(t, 0, report.Passed())
	assert.Equal(t, 1, report.Failed())
	assert.Equal(t, 2, report.Skipped())

	results := report.Categories[0].Results
	assert.Equal(t, doctor.StatusFail, results[0].Status)
	assert.Equal(t, "No credentials found", results[0].Label)
	assert.Equal(t, doctor.StatusSkipped, results[1].Status)
	assert.Equal(t, doctor.StatusSkipped, results[2].Status)
	assert.Contains(t, results[1].Hint, "skipped")
}

func TestRunChecks_ChainedDependency(t *testing.T) {
	cats := []doctor.Category{
		{
			Name: "Chain",
			Checks: []doctor.Check{
				pass("A"),
				dependent("B", []int{0}),
				dependent("C", []int{1}),
			},
		},
	}
	report := doctor.RunChecks(context.Background(), cats)

	assert.Equal(t, 3, report.Passed())
	assert.Equal(t, 0, report.Skipped())
}

func TestRunChecks_MixedStatuses(t *testing.T) {
	cats := []doctor.Category{
		{Name: "First", Checks: []doctor.Check{pass("ok"), fail("bad", "bad")}},
		{Name: "Second", Checks: []doctor.Check{warn("meh")}},
	}
	report := doctor.RunChecks(context.Background(), cats)

	assert.Equal(t, 1, report.Passed())
	assert.Equal(t, 1, report.Failed())
	assert.Equal(t, 1, report.Warnings())
	assert.Equal(t, 3, report.Total())
}

func TestRender_AllPass(t *testing.T) {
	cats := []doctor.Category{
		{Name: "A", Checks: []doctor.Check{pass("Check one"), pass("Check two")}},
	}
	report := doctor.RunChecks(context.Background(), cats)

	var buf bytes.Buffer
	doctor.Render(&buf, report, "v1.0.0", false, false)
	out := buf.String()

	assert.Contains(t, out, "Infracost Doctor v1.0.0 - running 2 checks")
	assert.Contains(t, out, "Check one")
	assert.Contains(t, out, "Check two")
	assert.Contains(t, out, "2 passed")
}

func TestRender_FailureWithHint(t *testing.T) {
	cats := []doctor.Category{
		{Name: "Auth", Checks: []doctor.Check{fail("Creds", "No creds")}},
	}
	report := doctor.RunChecks(context.Background(), cats)

	var buf bytes.Buffer
	doctor.Render(&buf, report, "v1.0.0", false, false)
	out := buf.String()

	assert.Contains(t, out, "No creds")
	assert.Contains(t, out, "fix it")
	assert.Contains(t, out, "1 issue")
}

func TestRender_SkippedChecks(t *testing.T) {
	cats := []doctor.Category{
		{
			Name: "Auth",
			Checks: []doctor.Check{
				fail("Credentials found", "No credentials found"),
				dependent("Token valid", []int{0}),
			},
		},
	}
	report := doctor.RunChecks(context.Background(), cats)

	var buf bytes.Buffer
	doctor.Render(&buf, report, "v1.0.0", false, false)
	out := buf.String()

	assert.Contains(t, out, "Token valid")
	assert.Contains(t, out, "skipped")
	assert.Contains(t, out, "1 skipped")
}

func TestRender_VerboseShowsExtraLines(t *testing.T) {
	cats := []doctor.Category{
		{
			Name: "A",
			Checks: []doctor.Check{
				{
					Name: "Check with details",
					Run: func(_ context.Context) doctor.Result {
						return doctor.Result{
							Status:  doctor.StatusPass,
							Verbose: []string{"detail line 1", "detail line 2"},
						}
					},
				},
			},
		},
	}
	report := doctor.RunChecks(context.Background(), cats)

	var buf bytes.Buffer
	doctor.Render(&buf, report, "v1.0.0", true, false)
	out := buf.String()

	assert.Contains(t, out, "Check with details")
	assert.Contains(t, out, "    detail line 1")
	assert.Contains(t, out, "    detail line 2")
}

func TestRender_NonVerboseHidesExtraLines(t *testing.T) {
	cats := []doctor.Category{
		{
			Name: "A",
			Checks: []doctor.Check{
				{
					Name: "Check with details",
					Run: func(_ context.Context) doctor.Result {
						return doctor.Result{
							Status:  doctor.StatusPass,
							Verbose: []string{"secret detail"},
						}
					},
				},
			},
		},
	}
	report := doctor.RunChecks(context.Background(), cats)

	var buf bytes.Buffer
	doctor.Render(&buf, report, "v1.0.0", false, false)
	out := buf.String()

	assert.Contains(t, out, "Check with details")
	assert.NotContains(t, out, "secret detail")
}
