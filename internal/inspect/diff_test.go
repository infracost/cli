package inspect

import (
	"strings"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
)

func diffCC(name string, cost float64) format.CostComponentOutput {
	return format.CostComponentOutput{
		Name:             name,
		Unit:             "hours",
		Price:            rat.New(cost),
		Quantity:         rat.New(1),
		TotalMonthlyCost: rat.New(cost),
	}
}

func diffOutput(projectName string, resources ...format.ResourceOutput) *format.Output {
	return &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{{ProjectName: projectName, Resources: resources}},
	}
}

func TestWriteDiff(t *testing.T) {
	past := diffOutput("main",
		format.ResourceOutput{Name: "aws_instance.web_app", CostComponents: []format.CostComponentOutput{
			diffCC("Instance usage (Linux/UNIX, on-demand, m5.large)", 70),
		}},
		format.ResourceOutput{Name: "aws_db_instance.old", CostComponents: []format.CostComponentOutput{
			diffCC("Database instance (on-demand, db.t3.micro)", 120),
		}},
	)

	current := diffOutput("main",
		format.ResourceOutput{
			Name:           "aws_instance.web_app",
			CostComponents: []format.CostComponentOutput{diffCC("Instance usage (Linux/UNIX, on-demand, m5.4xlarge)", 561)},
			Subresources: []format.ResourceOutput{{
				Name:           "root_block_device",
				CostComponents: []format.CostComponentOutput{diffCC("Storage (general purpose SSD, gp2)", 5)},
			}},
		},
		format.ResourceOutput{Name: "aws_lambda_function.hello", CostComponents: []format.CostComponentOutput{
			{Name: "Requests", Unit: "1M requests", Price: rat.New(0.2)},
		}},
	)

	var b strings.Builder
	assert.NoError(t, WriteDiff(&b, format.Diff(past, current)))
	assertGolden(t, b.String())
}

// Resources can change while the totals cancel out; the summary must still
// appear, otherwise a real change has no tail summary at all.
func TestWriteDiffZeroNetChange(t *testing.T) {
	past := diffOutput("main", format.ResourceOutput{
		Name:           "aws_instance.old",
		CostComponents: []format.CostComponentOutput{diffCC("Instance usage", 100)},
	})
	current := diffOutput("main", format.ResourceOutput{
		Name:           "aws_instance.new",
		CostComponents: []format.CostComponentOutput{diffCC("Instance usage", 100)},
	})

	var b strings.Builder
	assert.NoError(t, WriteDiff(&b, format.Diff(past, current)))

	got := b.String()
	assert.Contains(t, got, "Monthly estimate unchanged")
	assert.Contains(t, got, "Changed project")
}

func TestWriteDiffNoChanges(t *testing.T) {
	out := diffOutput("main", format.ResourceOutput{
		Name:           "aws_instance.web_app",
		CostComponents: []format.CostComponentOutput{diffCC("Instance usage", 70)},
	})

	var b strings.Builder
	assert.NoError(t, WriteDiff(&b, format.Diff(out, out)))
	assertGolden(t, b.String())
}
