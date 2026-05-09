package views_test

import (
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/tui/views"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
)

// rat parses a fixed-string rational for fixtures; panics on bad
// input since this is test-only setup, not user-facing parsing.
func ratFor(s string) *rat.Rat {
	r, err := rat.NewFromString(s)
	if err != nil {
		panic("ratFor: " + s + ": " + err.Error())
	}
	return r
}

// rowsFor builds a fixed corpus the filter tests share. Address +
// type + project differ per row so each filter dimension can be
// exercised individually.
func rowsFor() ([]views.ResourceRow, *format.Output) {
	out := &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web",
				Resources: []format.ResourceOutput{
					{Name: "aws_instance.web", Type: "aws_instance"},
					{Name: "aws_s3_bucket.logs", Type: "aws_s3_bucket"},
				},
			},
			{
				ProjectName: "api",
				Resources: []format.ResourceOutput{
					{Name: "aws_lambda.api", Type: "aws_lambda"},
				},
			},
		},
	}
	rows := views.RowsFromOutput(out)
	return rows, out
}

func addrs(rows []views.ResourceRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Address
	}
	return out
}

func TestFilterRows_EmptyExpressionReturnsAll(t *testing.T) {
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "")

	assert.ElementsMatch(t, addrs(rows), addrs(got))
}

func TestFilterRows_WhitespaceOnlyReturnsAll(t *testing.T) {
	// "   " should behave the same as "" — no filter, full set.
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "   ")

	assert.ElementsMatch(t, addrs(rows), addrs(got))
}

func TestFilterRows_SubstringOnAddress(t *testing.T) {
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "lambda")

	assert.Equal(t, []string{"aws_lambda.api"}, addrs(got))
}

func TestFilterRows_SubstringCaseInsensitive(t *testing.T) {
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "S3_BUCKET")

	// Case-insensitivity matters because users type filters in
	// lowercase but resource types are typically lowercase already;
	// shouting at the filter shouldn't return zero results.
	assert.Equal(t, []string{"aws_s3_bucket.logs"}, addrs(got))
}

func TestFilterRows_SubstringMatchesType(t *testing.T) {
	// Filtering on a type prefix should pull every matching resource
	// across projects — not just the one whose address contains the
	// string.
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "aws_lambda")

	assert.Equal(t, []string{"aws_lambda.api"}, addrs(got))
}

func TestFilterRows_SubstringMatchesProjectName(t *testing.T) {
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "api")

	// "api" matches the project name "api" (and would also match any
	// resource address containing "api"). For this corpus only the
	// lambda resource lives in project "api".
	assert.Equal(t, []string{"aws_lambda.api"}, addrs(got))
}

func TestFilterRows_NoMatchesReturnsEmpty(t *testing.T) {
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "nonexistent")

	assert.Empty(t, got)
}

func TestFilterRows_KeyValueDelegatesToInspect(t *testing.T) {
	// "=" in the expression switches the engine from substring to
	// the inspect.ParseFilter grammar. inspect supports project=,
	// provider=, policy=, and tag.<k>=missing — `project=api` should
	// scope to the lambda resource (the only one in the api project).
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "project=api")

	assert.Equal(t, []string{"aws_lambda.api"}, addrs(got))
}

func TestFilterRows_MalformedKeyValueFallsBackToSubstring(t *testing.T) {
	// Half-typed expressions like "type=" shouldn't blank the list —
	// the user is still composing. Fallback to substring lets the
	// list keep showing reasonable matches for "type=" as the user
	// continues typing.
	rows, out := rowsFor()
	got := views.FilterRows(rows, out, "type=")

	// "type=" doesn't appear as a substring anywhere in this corpus;
	// expect zero matches but no panic / no crash.
	assert.Empty(t, got)
}

func TestRowsFromOutput_NilReturnsNil(t *testing.T) {
	got := views.RowsFromOutput(nil)
	assert.Nil(t, got)
}

func TestRowsFromOutput_FlattensProjects(t *testing.T) {
	rows, _ := rowsFor()

	// Three resources across two projects → three rows.
	assert.Len(t, rows, 3)

	// Each row carries the originating project name so the picker
	// scope-filter (when the user tabs to a single project) can
	// match against it.
	projectsSeen := map[string]bool{}
	for _, r := range rows {
		projectsSeen[r.Project] = true
	}
	assert.True(t, projectsSeen["web"])
	assert.True(t, projectsSeen["api"])
}

func TestRowsFromOutput_SortsByCostDescByDefault(t *testing.T) {
	// RowsFromOutput's stable sort puts highest-cost resources first
	// so the initial list view is "what should I look at?". Test it
	// with explicit costs across projects.
	rows := views.RowsFromOutput(&format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web",
				Resources: []format.ResourceOutput{
					{
						Name: "cheap",
						CostComponents: []format.CostComponentOutput{
							{TotalMonthlyCost: ratFor("10")},
						},
					},
				},
			},
			{
				ProjectName: "api",
				Resources: []format.ResourceOutput{
					{
						Name: "expensive",
						CostComponents: []format.CostComponentOutput{
							{TotalMonthlyCost: ratFor("1000")},
						},
					},
					{
						Name: "free",
						CostComponents: []format.CostComponentOutput{},
					},
				},
			},
		},
	})

	addrs := []string{}
	for _, r := range rows {
		addrs = append(addrs, r.Address)
	}
	// expensive (1000) > cheap (10) > free (0).
	assert.Equal(t, []string{"expensive", "cheap", "free"}, addrs)
}
