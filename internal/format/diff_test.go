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

func TestScanDiffOutput_JSONShape(t *testing.T) {
	prev := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (Linux/UNIX, on-demand, t2.medium)": 80}))
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (Linux/UNIX, on-demand, t2.medium)": 120}))

	var buf bytes.Buffer
	require.NoError(t, BuildScanDiff(prev, curr).ToJSON(&buf))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))

	assert.Equal(t, "USD", decoded["Currency"])
	assert.Equal(t, "120.00", decoded["TotalMonthlyCost"])
	assert.Equal(t, "80.00", decoded["PastTotalMonthlyCost"])
	assert.Equal(t, "40.00", decoded["DiffTotalMonthlyCost"])
	assert.Equal(t, 50.0, decoded["PercentageChangeTotalMonthlyCost"])

	diff, ok := decoded["Diff"].(map[string]any)
	require.True(t, ok)
	byType, ok := diff["aws_instance"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "120.00", byType["CurrentMonthlyCost"])
	assert.Equal(t, "80.00", byType["PreviousMonthlyCost"])
	assert.Equal(t, "40.00", byType["DiffMonthlyCost"])
	assert.Equal(t, 50.0, byType["PercentageChangeMonthlyCost"])

	entries, ok := byType["Diff"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	entry, ok := entries[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "aws_instance.web", entry["Name"])

	subs, ok := entry["Subresources"].(map[string]any)
	require.True(t, ok)
	sub, ok := subs["Instance usage (Linux/UNIX, on-demand, t2.medium)"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "120.00", sub["CurrentMonthlyCost"])
	assert.Equal(t, "80.00", sub["PreviousMonthlyCost"])
	assert.Equal(t, "40.00", sub["DiffMonthlyCost"])
	assert.Equal(t, 50.0, sub["PercentageChangeMonthlyCost"])
}

func TestBuildScanDiff_ComponentRename(t *testing.T) {
	// An instance-type change renames the cost component: both names appear,
	// one ending at zero and one starting from zero.
	prev := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (t2.small)": 40}))
	curr := outputWith(costedResource("aws_instance.web", "aws_instance", map[string]int64{"Instance usage (t2.medium)": 80}))

	d := BuildScanDiff(prev, curr)

	entry := d.Diff["aws_instance"].Diff[0]
	require.Len(t, entry.Subresources, 2)
	old := entry.Subresources["Instance usage (t2.small)"]
	require.NotNil(t, old)
	assert.Equal(t, "0.00", old.CurrentMonthlyCost)
	assert.Equal(t, "40.00", old.PreviousMonthlyCost)
	updated := entry.Subresources["Instance usage (t2.medium)"]
	require.NotNil(t, updated)
	assert.Equal(t, "80.00", updated.CurrentMonthlyCost)
	assert.Equal(t, "0.00", updated.PreviousMonthlyCost)
}
