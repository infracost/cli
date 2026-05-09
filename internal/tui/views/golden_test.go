package views_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
	"github.com/stretchr/testify/require"
)

// updateGolden controls whether tests rewrite their golden files
// instead of comparing against them. Pass `-update` to `go test` to
// refresh every golden in this package, or combine with `-run TestX`
// to refresh just one.
var updateGolden = flag.Bool("update", false, "update view golden files in testdata/")

func TestMain(m *testing.M) {
	// Tests run with color forcibly disabled so golden files contain
	// no ANSI escapes. The init() in internal/ui already disables
	// color when stdout isn't a TTY (which it isn't under `go test`),
	// but forcing it makes the determinism explicit and survives any
	// future change to that init heuristic.
	ui.DisableColor()
	os.Exit(m.Run())
}

// assertGolden compares got against the golden file for the calling
// test. With -update it writes the file instead. Mirrors the helper
// pattern in internal/inspect.
func assertGolden(t *testing.T, got string) {
	t.Helper()

	name := strings.ReplaceAll(t.Name(), "/", "_") + ".golden"
	path := filepath.Join("testdata", name)

	if *updateGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}

	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./internal/tui/views/ -update` to create)", path, err)
	}
	if got != string(wantBytes) {
		t.Errorf(
			"output does not match %s (run `go test ./internal/tui/views/ -update` to refresh)\n--- want ---\n%s--- got ---\n%s",
			path, string(wantBytes), got,
		)
	}
}

// goldenFixture returns the canonical multi-project Output used by
// most golden tests. Crafted to exercise: multiple projects, mixed
// costed/free resources, FinOps + tagging policy failures, a
// triggered guardrail, and an over-budget result. Costs are round
// numbers so the rendered "X / Y" / "$N/mo" strings stay stable.
func goldenFixture() *format.Output {
	return &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "web",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_instance.web",
						Type: "aws_instance",
						CostComponents: []format.CostComponentOutput{
							{Name: "compute", TotalMonthlyCost: parseRatExt("100")},
						},
					},
					{Name: "aws_kms_key.free", Type: "aws_kms_key"},
				},
				FinopsResults: []format.FinopsOutput{
					{
						PolicyName: "Use spot instances",
						FailingResources: []format.FinopsFailingResourceOutput{
							{
								Name: "aws_instance.web",
								Issues: []format.FinopsIssueOutput{
									{Description: "switch to spot"},
								},
							},
						},
					},
				},
			},
			{
				ProjectName: "api",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_lambda.api",
						Type: "aws_lambda",
						CostComponents: []format.CostComponentOutput{
							{Name: "requests", TotalMonthlyCost: parseRatExt("2")},
						},
					},
				},
				TaggingResults: []format.TaggingOutput{
					{
						PolicyName: "Mandatory tags",
						FailingResources: []format.FailingTaggingResourceOutput{
							{
								Address:              "aws_lambda.api",
								MissingMandatoryTags: []string{"env"},
							},
						},
					},
				},
			},
		},
		GuardrailResults: []format.GuardrailOutput{
			{GuardrailName: "monthly_cap", Triggered: true},
		},
		BudgetResults: []format.BudgetOutput{
			{BudgetName: "core_team", OverBudget: true},
		},
	}
}
