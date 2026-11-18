package format

import (
	"context"
	"testing"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/api/events/eventstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackDiffTypesFixedIssueAsFinopsPolicy(t *testing.T) {
	recorder := &eventstest.Recorder{}
	current := &Output{Projects: []ProjectOutput{{ProjectName: "main"}}}

	current.TrackDiff(context.Background(), events.NewTracker(recorder), fixedIssueBaseline("aws-gp2-to-gp3"))

	require.Len(t, recorder.Events, 1)
	assert.Equal(t, "cloud-issue-fixed", recorder.Events[0].Name)
	assertField(t, recorder.Events[0], "type", "finops-policy")
	assertField(t, recorder.Events[0], "policySlug", "aws-gp2-to-gp3")
	assertField(t, recorder.Events[0], "source", "cli")
	assertField(t, recorder.Events[0], "resourceAddress", "aws_instance.web")
	assertNoField(t, recorder.Events[0], "autoFixPullRequest")
}

func TestTrackDiffTypesFixedIssueAsCloudSecurityPolicy(t *testing.T) {
	recorder := &eventstest.Recorder{}
	current := &Output{
		Projects:                 []ProjectOutput{{ProjectName: "main"}},
		cloudSecurityPolicySlugs: map[string]struct{}{"aws-ec2-use-imdsv2": {}},
	}

	current.TrackDiff(context.Background(), events.NewTracker(recorder), fixedIssueBaseline("aws-ec2-use-imdsv2"))

	require.Len(t, recorder.Events, 1)
	assert.Equal(t, "cloud-issue-fixed", recorder.Events[0].Name)
	assertField(t, recorder.Events[0], "type", "cloud-security-policy")
}

func TestTrackDiffSendsBranchID(t *testing.T) {
	snapshot := events.Snapshot()
	t.Cleanup(func() { events.Restore(snapshot) })
	events.RegisterMetadata("branchId", "main")
	recorder := &eventstest.Recorder{}
	current := &Output{Projects: []ProjectOutput{{ProjectName: "main"}}}

	current.TrackDiff(context.Background(), events.NewTracker(recorder), fixedIssueBaseline("aws-gp2-to-gp3"))

	require.Len(t, recorder.Events, 1)
	assertField(t, recorder.Events[0], "branchId", "main")
}

func TestTrackDiffOmitsEmptyBranchID(t *testing.T) {
	snapshot := events.Snapshot()
	t.Cleanup(func() { events.Restore(snapshot) })
	events.RegisterMetadata("branchId", "")
	recorder := &eventstest.Recorder{}
	current := &Output{Projects: []ProjectOutput{{ProjectName: "main"}}}

	current.TrackDiff(context.Background(), events.NewTracker(recorder), fixedIssueBaseline("aws-gp2-to-gp3"))

	require.Len(t, recorder.Events, 1)
	assertNoField(t, recorder.Events[0], "branchId")
}

func TestTrackDiffTypesFixedIssueAsTagPolicy(t *testing.T) {
	recorder := &eventstest.Recorder{}
	current := &Output{Projects: []ProjectOutput{{ProjectName: "main"}}}
	baseline := &Output{Projects: []ProjectOutput{{
		ProjectName: "main",
		TaggingResults: []TaggingOutput{{
			PolicyID:         "tag-policy-1",
			FailingResources: []FailingTaggingResourceOutput{{Address: "aws_s3_bucket.logs"}},
		}},
	}}}

	current.TrackDiff(context.Background(), events.NewTracker(recorder), baseline)

	require.Len(t, recorder.Events, 1)
	assertField(t, recorder.Events[0], "type", "tag-policy")
	assertField(t, recorder.Events[0], "policyId", "tag-policy-1")
	assertField(t, recorder.Events[0], "resourceAddress", "aws_s3_bucket.logs")
	assertNoField(t, recorder.Events[0], "policySlug")
}

func TestTrackRunWithoutUsageFile(t *testing.T) {
	recorder := &eventstest.Recorder{}
	output := &Output{
		Projects: []ProjectOutput{{Resources: []ResourceOutput{
			{Type: "aws_instance", IsSupported: true},
			{Type: "aws_iam_role", IsSupported: true, IsFree: true},
			{Type: "aws_unknown", IsSupported: false},
		}}},
		projectTypes: []string{"terraform"},
	}

	output.TrackRun(context.Background(), events.NewTracker(recorder), 1.5, "json", nil)

	require.Len(t, recorder.Events, 1)
	event := recorder.Events[0]
	assert.Equal(t, "infracost-run", event.Name)
	expected := map[string]interface{}{
		"runSeconds":                1.5,
		"outputFormat":              "json",
		"projectType":               []interface{}{"terraform"},
		"totalResources":            3.0,
		"totalSupportedResources":   1.0,
		"totalNoPriceResources":     1.0,
		"totalUnsupportedResources": 1.0,
		"supportedResourceCounts":   map[string]interface{}{"aws_instance": 1.0},
		"supportedResourcesList":    []interface{}{"aws_instance"},
		"unsupportedResourceCounts": map[string]interface{}{"aws_unknown": 1.0},
		"unsupportedResourcesList":  []interface{}{"aws_unknown"},
		"hasUsageFile":              false,
	}
	for key, want := range expected {
		assertField(t, event, key, want)
	}
	for _, key := range []string{"totalEstimatedUsages", "estimatedUsageCounts", "newResourceCount"} {
		assertNoField(t, event, key)
	}
}

func TestTrackRunWithUsageFileAndPreviousRun(t *testing.T) {
	recorder := &eventstest.Recorder{}
	output := &Output{
		estimatedUsageCounts:   map[string]int{"aws_lambda_function.monthly_requests": 2},
		unestimatedUsageCounts: map[string]int{},
	}

	output.TrackRun(context.Background(), events.NewTracker(recorder), 1, "table", &Output{})

	require.Len(t, recorder.Events, 1)
	event := recorder.Events[0]
	expected := map[string]interface{}{
		"hasUsageFile":           true,
		"totalEstimatedUsages":   2.0,
		"totalUnestimatedUsages": 0.0,
		"estimatedUsageCounts":   map[string]interface{}{"aws_lambda_function.monthly_requests": 2.0},
		"estimatedUsageList":     []interface{}{"aws_lambda_function.monthly_requests", "aws_lambda_function.monthly_requests"},
		"unestimatedUsageCounts": map[string]interface{}{},
		"newResourceCount":       0.0,
		"changedResourceCount":   0.0,
		"newIssueCount":          0.0,
		"existingIssueCount":     0.0,
	}
	for key, want := range expected {
		assertField(t, event, key, want)
	}
}

func fixedIssueBaseline(policySlug string) *Output {
	return &Output{Projects: []ProjectOutput{{
		ProjectName: "main",
		FinopsResults: []FinopsOutput{{
			PolicySlug:       policySlug,
			FailingResources: []FinopsFailingResourceOutput{{Name: "aws_instance.web"}},
		}},
	}}}
}

func assertField(t *testing.T, event eventstest.Event, key string, want interface{}) {
	t.Helper()
	got, ok := event.Field(key)
	if assert.True(t, ok, key) {
		assert.Equal(t, want, got, key)
	}
}

func assertNoField(t *testing.T, event eventstest.Event, key string) {
	t.Helper()
	_, ok := event.Field(key)
	assert.False(t, ok, key)
}
