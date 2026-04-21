package format

import (
	"testing"
	"time"

	repoconfig "github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToOutput_BudgetResults(t *testing.T) {
	result := &Result{
		Config: &repoconfig.Config{Currency: "USD"},
		BudgetResults: []event.BudgetResult{
			{
				BudgetID:    "b-1",
				Tags:        []event.BudgetTag{{Key: "env", Value: "production"}},
				StartDate:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				EndDate:     time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
				Amount:      rat.New(1000),
				CurrentCost: rat.New(500),
			},
			{
				BudgetID:             "b-2",
				Tags:                 []event.BudgetTag{{Key: "team", Value: "frontend"}},
				StartDate:            time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
				EndDate:              time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
				Amount:               rat.New(300),
				CurrentCost:          rat.New(400),
				CustomOverrunMessage: "Contact FinOps",
			},
		},
	}

	output := ToOutput(result)

	require.Len(t, output.BudgetResults, 2)

	assert.Equal(t, "b-1", output.BudgetResults[0].BudgetID)
	assert.Equal(t, "env", output.BudgetResults[0].Tags[0].Key)
	assert.Equal(t, "production", output.BudgetResults[0].Tags[0].Value)
	assert.True(t, output.BudgetResults[0].Amount.Equals(rat.New(1000)))
	assert.True(t, output.BudgetResults[0].CurrentCost.Equals(rat.New(500)))
	assert.False(t, output.BudgetResults[0].OverBudget)
	assert.Empty(t, output.BudgetResults[0].CustomOverrunMessage)

	assert.Equal(t, "b-2", output.BudgetResults[1].BudgetID)
	assert.True(t, output.BudgetResults[1].OverBudget)
	assert.Equal(t, "Contact FinOps", output.BudgetResults[1].CustomOverrunMessage)
}

func TestToOutput_NoBudgets(t *testing.T) {
	result := &Result{
		Config: &repoconfig.Config{Currency: "USD"},
	}

	output := ToOutput(result)
	assert.Empty(t, output.BudgetResults)
}
