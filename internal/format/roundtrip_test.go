package format

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An unchanged scan compared against its own saved JSON must show no changes.
// rat.Rat marshals through float64, so the two sides are not bit-identical.
func TestDiffIgnoresJSONRoundTripRounding(t *testing.T) {
	price, err := rat.NewFromString("0.0000166667")
	require.NoError(t, err)
	cost, err := rat.NewFromString("70.08")
	require.NoError(t, err)

	live := &Output{Currency: "USD", Projects: []ProjectOutput{{
		ProjectName: "main",
		Resources: []ResourceOutput{{
			Name: "aws_lambda_function.fn",
			CostComponents: []CostComponentOutput{{
				Name: "Duration", Unit: "GB-seconds",
				Price: price, Quantity: rat.New(4204800), TotalMonthlyCost: cost,
			}},
		}},
	}}}

	var b bytes.Buffer
	require.NoError(t, json.NewEncoder(&b).Encode(live))
	var baseline Output
	require.NoError(t, json.NewDecoder(&b).Decode(&baseline))

	d := Diff(&baseline, live)
	assert.False(t, d.HasChanges(), "same scan, saved and reloaded, is not a change")
	assert.Empty(t, d.Projects[0].Resources)
}
