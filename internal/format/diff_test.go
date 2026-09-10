package format

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func costedResource(name, resType string, components map[string]int64) ResourceOutput {
	r := ResourceOutput{Name: name, Type: resType, IsSupported: true}
	for cname, cost := range components {
		r.CostComponents = append(r.CostComponents, CostComponentOutput{
			Name:             cname,
			TotalMonthlyCost: rat.New(cost),
		})
	}
	return r
}

func outputWith(resources ...ResourceOutput) *Output {
	return &Output{
		Currency: "USD",
		Projects: []ProjectOutput{{ProjectName: "main", Resources: resources}},
	}
}

func TestBuildScanDiff_ChangedResource(t *testing.T) {
	prev := outputWith(
		costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (Linux/UNIX, on-demand, t2.medium)": 80}),
		costedResource("aws_s3_bucket.assets", "aws_s3_bucket", map[string]int64{"Storage": 20}),
	)
	curr := outputWith(
		costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (Linux/UNIX, on-demand, t2.medium)": 120}),
		costedResource("aws_s3_bucket.assets", "aws_s3_bucket", map[string]int64{"Storage": 20}),
	)

	d := BuildScanDiff(prev, curr)

	assert.Equal(t, "USD", d.Currency)
	assert.Equal(t, "140.00", d.TotalMonthlyCost)
	assert.Equal(t, "100.00", d.PastTotalMonthlyCost)
	assert.Equal(t, "40.00", d.DiffTotalMonthlyCost)
	require.NotNil(t, d.PercentageChangeTotalMonthlyCost)
	assert.InDelta(t, 40.0, *d.PercentageChangeTotalMonthlyCost, 0.001)

	// The unchanged bucket must not appear.
	require.Len(t, d.Diff, 1)
	td := d.Diff["aws_instance"]
	require.NotNil(t, td)
	assert.Equal(t, "120.00", td.CurrentMonthlyCost)
	assert.Equal(t, "80.00", td.PreviousMonthlyCost)
	assert.Equal(t, "40.00", td.DiffMonthlyCost)
	require.NotNil(t, td.PercentageChangeMonthlyCost)
	assert.InDelta(t, 50.0, *td.PercentageChangeMonthlyCost, 0.001)

	require.Len(t, td.Diff, 1)
	entry := td.Diff[0]
	assert.Equal(t, "aws_instance.web", entry.Name)
	assert.Equal(t, "120.00", entry.CurrentMonthlyCost)
	assert.Equal(t, "80.00", entry.PreviousMonthlyCost)
	assert.Equal(t, "40.00", entry.DiffMonthlyCost)
	require.NotNil(t, entry.PercentageChangeMonthlyCost)
	assert.InDelta(t, 50.0, *entry.PercentageChangeMonthlyCost, 0.001)

	require.Len(t, entry.Subresources, 1)
	sub := entry.Subresources["Instance usage (Linux/UNIX, on-demand, t2.medium)"]
	require.NotNil(t, sub)
	assert.Equal(t, "120.00", sub.CurrentMonthlyCost)
	assert.Equal(t, "80.00", sub.PreviousMonthlyCost)
	assert.Equal(t, "40.00", sub.DiffMonthlyCost)
	require.NotNil(t, sub.PercentageChangeMonthlyCost)
	assert.InDelta(t, 50.0, *sub.PercentageChangeMonthlyCost, 0.001)
}

func TestBuildScanDiff_AddedAndRemovedResources(t *testing.T) {
	prev := outputWith(
		costedResource("aws_instance.old", "aws_instance", map[string]int64{"Instance usage": 30}),
	)
	curr := outputWith(
		costedResource("aws_instance.new", "aws_instance", map[string]int64{"Instance usage": 50}),
	)

	d := BuildScanDiff(prev, curr)

	assert.Equal(t, "50.00", d.TotalMonthlyCost)
	assert.Equal(t, "30.00", d.PastTotalMonthlyCost)
	assert.Equal(t, "20.00", d.DiffTotalMonthlyCost)

	td := d.Diff["aws_instance"]
	require.NotNil(t, td)
	require.Len(t, td.Diff, 2)
	// Entries sort by name.
	added, removed := td.Diff[0], td.Diff[1]
	assert.Equal(t, "aws_instance.new", added.Name)
	assert.Equal(t, "50.00", added.CurrentMonthlyCost)
	assert.Equal(t, "0.00", added.PreviousMonthlyCost)
	assert.Equal(t, "50.00", added.DiffMonthlyCost)
	// Percentage change from zero is undefined.
	assert.Nil(t, added.PercentageChangeMonthlyCost)

	assert.Equal(t, "aws_instance.old", removed.Name)
	assert.Equal(t, "0.00", removed.CurrentMonthlyCost)
	assert.Equal(t, "30.00", removed.PreviousMonthlyCost)
	assert.Equal(t, "-30.00", removed.DiffMonthlyCost)
	require.NotNil(t, removed.PercentageChangeMonthlyCost)
	assert.InDelta(t, -100.0, *removed.PercentageChangeMonthlyCost, 0.001)
}

func TestBuildScanDiff_NoChanges(t *testing.T) {
	prev := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 80}))
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 80}))

	d := BuildScanDiff(prev, curr)

	assert.Equal(t, "80.00", d.TotalMonthlyCost)
	assert.Equal(t, "80.00", d.PastTotalMonthlyCost)
	assert.Equal(t, "0.00", d.DiffTotalMonthlyCost)
	require.NotNil(t, d.PercentageChangeTotalMonthlyCost)
	assert.Zero(t, *d.PercentageChangeTotalMonthlyCost)
	assert.Empty(t, d.Diff)
}

func TestBuildScanDiff_EmptyPrior(t *testing.T) {
	prev := &Output{Currency: "USD"}
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 80}))

	d := BuildScanDiff(prev, curr)

	assert.Equal(t, "80.00", d.TotalMonthlyCost)
	assert.Equal(t, "0.00", d.PastTotalMonthlyCost)
	assert.Equal(t, "80.00", d.DiffTotalMonthlyCost)
	assert.Nil(t, d.PercentageChangeTotalMonthlyCost)
	require.Len(t, d.Diff, 1)
}

func TestBuildScanDiff_SubresourceCosts(t *testing.T) {
	prevRoot := costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 80})
	prevRoot.Subresources = []ResourceOutput{
		costedResource("root_block_device", "aws_ebs_volume", map[string]int64{"Storage": 5}),
	}
	currRoot := costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 80})
	currRoot.Subresources = []ResourceOutput{
		costedResource("root_block_device", "aws_ebs_volume", map[string]int64{"Storage": 10}),
	}

	d := BuildScanDiff(outputWith(prevRoot), outputWith(currRoot))

	td := d.Diff["aws_instance"]
	require.NotNil(t, td)
	require.Len(t, td.Diff, 1)
	entry := td.Diff[0]
	assert.Equal(t, "90.00", entry.CurrentMonthlyCost)
	assert.Equal(t, "85.00", entry.PreviousMonthlyCost)

	// Only the changed subresource shows up; the unchanged component doesn't.
	require.Len(t, entry.Subresources, 1)
	sub := entry.Subresources["root_block_device"]
	require.NotNil(t, sub)
	assert.Equal(t, "10.00", sub.CurrentMonthlyCost)
	assert.Equal(t, "5.00", sub.PreviousMonthlyCost)
	assert.Equal(t, "5.00", sub.DiffMonthlyCost)
}

func TestBuildScanDiff_OffsettingComponentChanges(t *testing.T) {
	// One component goes up $10 and another down $10: the resource's total is
	// unchanged, but the composition changed, so it must still appear (as the
	// legacy CLI's component-level diff did) with both components listed.
	prev := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 80, "EBS-optimized usage": 20}))
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage": 70, "EBS-optimized usage": 30}))

	d := BuildScanDiff(prev, curr)

	assert.Equal(t, "0.00", d.DiffTotalMonthlyCost)
	td := d.Diff["aws_instance"]
	require.NotNil(t, td)
	require.Len(t, td.Diff, 1)
	entry := td.Diff[0]
	assert.Equal(t, "0.00", entry.DiffMonthlyCost)

	require.Len(t, entry.Subresources, 2)
	down := entry.Subresources["Instance usage"]
	require.NotNil(t, down)
	assert.Equal(t, "-10.00", down.DiffMonthlyCost)
	up := entry.Subresources["EBS-optimized usage"]
	require.NotNil(t, up)
	assert.Equal(t, "10.00", up.DiffMonthlyCost)
}

func TestBuildScanDiff_PriceChangeSameMonthlyCost(t *testing.T) {
	// Price halves and quantity doubles: the monthly cost is identical, but
	// the component changed — the legacy CLI compared quantity and price too,
	// so the entry must surface, carrying the price/quantity that moved.
	prevRes := ResourceOutput{Name: "aws_lambda_function.fn", Type: "aws_lambda_function", IsSupported: true,
		CostComponents: []CostComponentOutput{{
			Name:             "Requests",
			Price:            rat.New(4),
			Quantity:         rat.New(25),
			TotalMonthlyCost: rat.New(100),
		}}}
	currRes := ResourceOutput{Name: "aws_lambda_function.fn", Type: "aws_lambda_function", IsSupported: true,
		CostComponents: []CostComponentOutput{{
			Name:             "Requests",
			Price:            rat.New(2),
			Quantity:         rat.New(50),
			TotalMonthlyCost: rat.New(100),
		}}}

	d := BuildScanDiff(outputWith(prevRes), outputWith(currRes))

	td := d.Diff["aws_lambda_function"]
	require.NotNil(t, td)
	require.Len(t, td.Diff, 1)
	entry := td.Diff[0]
	assert.Equal(t, "0.00", entry.DiffMonthlyCost)

	require.Len(t, entry.Subresources, 1)
	sub := entry.Subresources["Requests"]
	require.NotNil(t, sub)
	assert.Equal(t, "0.00", sub.DiffMonthlyCost)
	require.NotNil(t, sub.PreviousPrice)
	assert.True(t, sub.PreviousPrice.Equals(rat.New(4)))
	require.NotNil(t, sub.CurrentPrice)
	assert.True(t, sub.CurrentPrice.Equals(rat.New(2)))
	require.NotNil(t, sub.PreviousQuantity)
	assert.True(t, sub.PreviousQuantity.Equals(rat.New(25)))
	require.NotNil(t, sub.CurrentQuantity)
	assert.True(t, sub.CurrentQuantity.Equals(rat.New(50)))
}

func TestScanDiffOutput_JSONShape(t *testing.T) {
	prev := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (Linux/UNIX, on-demand, t2.medium)": 80}))
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (Linux/UNIX, on-demand, t2.medium)": 120}))

	var buf bytes.Buffer
	require.NoError(t, BuildScanDiff(prev, curr).ToJSON(&buf))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))

	assert.Equal(t, "USD", decoded["currency"])
	assert.Equal(t, "120.00", decoded["total_monthly_cost"])
	assert.Equal(t, "80.00", decoded["past_total_monthly_cost"])
	assert.Equal(t, "40.00", decoded["diff_total_monthly_cost"])
	assert.Equal(t, 50.0, decoded["percentage_change_total_monthly_cost"])

	diff, ok := decoded["diff"].(map[string]any)
	require.True(t, ok)
	byType, ok := diff["aws_instance"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "120.00", byType["current_monthly_cost"])
	assert.Equal(t, "80.00", byType["previous_monthly_cost"])
	assert.Equal(t, "40.00", byType["diff_monthly_cost"])
	assert.Equal(t, 50.0, byType["percentage_change_monthly_cost"])

	entries, ok := byType["diff"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	entry, ok := entries[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "aws_instance.web", entry["name"])

	subs, ok := entry["subresources"].(map[string]any)
	require.True(t, ok)
	sub, ok := subs["Instance usage (Linux/UNIX, on-demand, t2.medium)"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "120.00", sub["current_monthly_cost"])
	assert.Equal(t, "80.00", sub["previous_monthly_cost"])
	assert.Equal(t, "40.00", sub["diff_monthly_cost"])
	assert.Equal(t, 50.0, sub["percentage_change_monthly_cost"])
}

func TestBuildScanDiff_ComponentRename(t *testing.T) {
	// An instance-type change renames the cost component. The names pair on
	// the text before the bracket (as in the legacy CLI), so a resize shows
	// as one changed entry under the current name — not two entries bouncing
	// through zero.
	prev := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (t2.small)": 40}))
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (t2.medium)": 80}))

	d := BuildScanDiff(prev, curr)

	entry := d.Diff["aws_instance"].Diff[0]
	require.Len(t, entry.Subresources, 1)
	resized := entry.Subresources["Instance usage (t2.medium)"]
	require.NotNil(t, resized)
	assert.Equal(t, "80.00", resized.CurrentMonthlyCost)
	assert.Equal(t, "40.00", resized.PreviousMonthlyCost)
	assert.Equal(t, "40.00", resized.DiffMonthlyCost)
	require.NotNil(t, resized.PercentageChangeMonthlyCost)
	assert.InDelta(t, 100.0, *resized.PercentageChangeMonthlyCost, 0.001)
}

func TestBuildScanDiff_BareNameDoesNotBracketMatch(t *testing.T) {
	// The bracket fallback needs a bracket on both sides (legacy behavior):
	// a bare "Storage" line must not pair with "Storage (provisioned IOPS)".
	prev := outputWith(costedResource("aws_ebs_volume.data", "aws_ebs_volume", map[string]int64{"Storage": 10}))
	curr := outputWith(costedResource("aws_ebs_volume.data", "aws_ebs_volume", map[string]int64{"Storage (provisioned IOPS)": 25}))

	d := BuildScanDiff(prev, curr)

	entry := d.Diff["aws_ebs_volume"].Diff[0]
	require.Len(t, entry.Subresources, 2)
	removed := entry.Subresources["Storage"]
	require.NotNil(t, removed)
	assert.Equal(t, "0.00", removed.CurrentMonthlyCost)
	assert.Equal(t, "10.00", removed.PreviousMonthlyCost)
	added := entry.Subresources["Storage (provisioned IOPS)"]
	require.NotNil(t, added)
	assert.Equal(t, "25.00", added.CurrentMonthlyCost)
	assert.Equal(t, "0.00", added.PreviousMonthlyCost)
}
