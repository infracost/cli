package cmds_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/agents"
	agentsmocks "github.com/infracost/cli/internal/api/agents/mocks"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
)

// agentsConfig builds a config with the Agents client factory swapped for
// the mock. cfg.OrgID is pre-set so the pure functions skip the auth /
// resolveOrg path the cobra wrapper handles.
func agentsConfig(mockClient *agentsmocks.MockClient) *config.Config {
	cfg := &config.Config{OrgID: "org-1"}
	cfg.Agents.Client = func(_ *http.Client) agents.Client {
		return mockClient
	}
	return cfg
}

func TestListFindings(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ListFindings(mock.Anything, "org-1", agents.ListFindingsParams{Status: "open"}).
		Return(agents.FindingsPage{
			Items: []agents.Finding{
				{
					ID:                      "f-1",
					Title:                   "Idle EBS volumes",
					Effort:                  "small",
					Status:                  "open",
					EstimatedMonthlySavings: 120.50,
					TaskTotal:               2,
				},
				{
					ID:                      "f-2",
					Title:                   "Oversized RDS",
					Effort:                  "medium",
					Status:                  "open",
					EstimatedMonthlySavings: 80.25,
				},
			},
			NextCursor: "next-cursor",
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{
		Status: "open",
	})

	require.NoError(t, err)
	assert.Len(t, result.Findings, 2)
	assert.Equal(t, "f-1", result.Findings[0].ID)
	assert.Equal(t, 2, result.Findings[0].TaskTotal)
	assert.InDelta(t, 200.75, result.TotalSavings, 0.001)
	assert.True(t, result.HasNextPage)
	assert.Equal(t, "next-cursor", result.NextCursor)
}

func TestListFindings_LastPage(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ListFindings(mock.Anything, "org-1", mock.Anything).
		Return(agents.FindingsPage{
			Items: []agents.Finding{{ID: "f-1", Title: "x"}},
			// No NextCursor — caller should see HasNextPage=false.
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{})
	require.NoError(t, err)
	assert.False(t, result.HasNextPage)
	assert.Empty(t, result.NextCursor)
}

func TestListFindings_NoOrg(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := &config.Config{}
	cfg.Agents.Client = func(_ *http.Client) agents.Client { return mockClient }

	_, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no organization selected")
}

func TestListFindings_APIError(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ListFindings(mock.Anything, "org-1", mock.Anything).
		Return(agents.FindingsPage{}, errors.New("boom"))

	cfg := agentsConfig(mockClient)
	_, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing findings: boom")
}

func TestGetFinding(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		GetFinding(mock.Anything, "org-1", "f-1").
		Return(agents.Finding{
			ID:    "f-1",
			Title: "Idle EBS volumes",
			Tasks: []agents.Task{{ID: "t-1", Title: "Delete volume"}},
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.GetFinding(context.Background(), cfg, nil, cmds.FindingsGetInput{ID: "f-1"})
	require.NoError(t, err)
	assert.Equal(t, "f-1", result.Finding.ID)
	assert.Len(t, result.Finding.Tasks, 1)
}

func TestGetFinding_MissingID(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.GetFinding(context.Background(), cfg, nil, cmds.FindingsGetInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding id is required")
}

func TestPreviewFix(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		GenerateAction(mock.Anything, "org-1", "f-1", agents.GenerateActionRequest{
			TaskIDs:    []string{"t-1"},
			ActionType: agents.ActionTypeOpenPR,
		}).
		Return(agents.GenerateActionResponse{
			Type:   agents.ActionTypeOpenPR,
			Config: json.RawMessage(`{"branch":"finops/idle-ebs"}`),
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "open_pr", result.Type)
	assert.JSONEq(t, `{"branch":"finops/idle-ebs"}`, string(result.Config))
}

func TestPreviewFix_RejectsManual(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
		Type:      "manual",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support type=manual")
}

func TestPreviewFix_InvalidType(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
		Type:      "delete_account",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --type")
}

func TestCreateFix(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	configBlob := json.RawMessage(`{"branch":"finops/idle-ebs","body":"…"}`)
	mockClient.EXPECT().
		CreateAction(mock.Anything, "org-1", "f-1", agents.CreateActionRequest{
			Type:    agents.ActionTypeOpenPR,
			Config:  configBlob,
			TaskIDs: []string{"t-1"},
		}).
		Return(agents.CreateActionResponse{OK: true, ActionID: "a-1"}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.CreateFix(context.Background(), cfg, nil, cmds.CreateFixInput{
		FindingID:  "f-1",
		TaskID:     "t-1",
		ConfigJSON: configBlob,
	})
	require.NoError(t, err)
	assert.Equal(t, "a-1", result.ActionID)
}

func TestCreateFix_ManualAllowed(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	configBlob := json.RawMessage(`{"instructions":"do the thing"}`)
	mockClient.EXPECT().
		CreateAction(mock.Anything, "org-1", "f-1", agents.CreateActionRequest{
			Type:    agents.ActionTypeManual,
			Config:  configBlob,
			TaskIDs: []string{"t-1"},
		}).
		Return(agents.CreateActionResponse{OK: true, ActionID: "a-2"}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.CreateFix(context.Background(), cfg, nil, cmds.CreateFixInput{
		FindingID:  "f-1",
		TaskID:     "t-1",
		Type:       "manual",
		ConfigJSON: configBlob,
	})
	require.NoError(t, err)
	assert.Equal(t, "a-2", result.ActionID)
}

func TestCreateFix_EmptyConfig(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.CreateFix(context.Background(), cfg, nil, cmds.CreateFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestUpdateFindingStatus(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		UpdateFindingStatus(mock.Anything, "org-1", "f-1", agents.UpdateFindingStatusRequest{
			Status: agents.FindingStatusDismissed,
			Reason: "false positive",
		}).
		Return(agents.Finding{ID: "f-1", Title: "x", Status: "dismissed"}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.UpdateFindingStatus(context.Background(), cfg, nil, cmds.UpdateFindingStatusInput{
		ID:     "f-1",
		Status: "dismissed",
		Reason: "false positive",
	})
	require.NoError(t, err)
	assert.Equal(t, "dismissed", result.Finding.Status)
}

func TestUpdateFindingStatus_InvalidStatus(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.UpdateFindingStatus(context.Background(), cfg, nil, cmds.UpdateFindingStatusInput{
		ID:     "f-1",
		Status: "in_progress", // valid task status but not a finding status
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid status "in_progress"`)
}

func TestUpdateFindingStatus_MissingID(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.UpdateFindingStatus(context.Background(), cfg, nil, cmds.UpdateFindingStatusInput{
		Status: "resolved",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finding id is required")
}

func TestUpdateTaskStatus_Confirm(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ReportTask(mock.Anything, "org-1", "t-1", agents.ReportTaskRequest{
			Type: agents.ReportTypeConfirmation,
		}).
		Return(agents.ReportTaskResponse{
			OK:                 true,
			Task:               agents.Task{ID: "t-1", Status: "awaiting_resolution"},
			ConfirmedActionIDs: []string{"a-1"},
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.UpdateTaskStatus(context.Background(), cfg, nil, cmds.UpdateTaskStatusInput{
		TaskID: "t-1",
		Status: "confirm",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a-1"}, result.ConfirmedActionIDs)
	assert.Empty(t, result.DismissedActionIDs)
}

func TestUpdateTaskStatus_Correct(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ReportTask(mock.Anything, "org-1", "t-1", agents.ReportTaskRequest{
			Type:    agents.ReportTypeCorrection,
			Content: "accepted the cost in the budget",
		}).
		Return(agents.ReportTaskResponse{
			OK:                 true,
			Task:               agents.Task{ID: "t-1", Status: "dismissed"},
			LearningID:         "l-1",
			DismissedActionIDs: []string{"a-2"},
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.UpdateTaskStatus(context.Background(), cfg, nil, cmds.UpdateTaskStatusInput{
		TaskID: "t-1",
		Status: "correct",
		Reason: "accepted the cost in the budget",
	})
	require.NoError(t, err)
	assert.Equal(t, "l-1", result.LearningID)
	assert.Equal(t, []string{"a-2"}, result.DismissedActionIDs)
}

func TestUpdateTaskStatus_CorrectRequiresReason(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.UpdateTaskStatus(context.Background(), cfg, nil, cmds.UpdateTaskStatusInput{
		TaskID: "t-1",
		Status: "correct",
		// reason blank — whitespace also rejected
		Reason: "   ",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--reason is required for status=correct")
}

// TestUpdateTaskStatus_Dismiss exercises the PATCH route the dismiss
// verb maps to. The point is the source: dismiss goes through
// PATCH /tasks/:id so the AgentLearning ends up with source=task_dismissed,
// not source=correction.
func TestUpdateTaskStatus_Dismiss(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		UpdateTaskStatus(mock.Anything, "org-1", "t-1", agents.UpdateTaskStatusRequest{
			Status:          agents.TaskStatusDismissed,
			DismissedReason: "scope drift — not actioning this quarter",
		}).
		Return(agents.Task{ID: "t-1", Status: "dismissed", DismissedReason: "scope drift — not actioning this quarter"}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.UpdateTaskStatus(context.Background(), cfg, nil, cmds.UpdateTaskStatusInput{
		TaskID: "t-1",
		Status: "dismiss",
		Reason: "scope drift — not actioning this quarter",
	})
	require.NoError(t, err)
	assert.Equal(t, "dismissed", result.Task.Status)
	// The PATCH endpoint doesn't echo cascade IDs back; the result's
	// DismissedActionIDs slot is intentionally empty.
	assert.Empty(t, result.DismissedActionIDs)
	assert.Empty(t, result.LearningID)
}

func TestUpdateTaskStatus_DismissAllowsEmptyReason(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		UpdateTaskStatus(mock.Anything, "org-1", "t-1", agents.UpdateTaskStatusRequest{
			Status: agents.TaskStatusDismissed,
		}).
		Return(agents.Task{ID: "t-1", Status: "dismissed"}, nil)

	cfg := agentsConfig(mockClient)
	// Empty reason is allowed for dismiss (the server simply won't emit a
	// learning). The CLI surfaces this as a recommendation, not a hard
	// requirement, so the pure function lets it through.
	_, err := cmds.UpdateTaskStatus(context.Background(), cfg, nil, cmds.UpdateTaskStatusInput{
		TaskID: "t-1",
		Status: "dismiss",
	})
	require.NoError(t, err)
}

func TestUpdateTaskStatus_InvalidStatus(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.UpdateTaskStatus(context.Background(), cfg, nil, cmds.UpdateTaskStatusInput{
		TaskID: "t-1",
		Status: "rejection",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid --status "rejection"`)
}

func TestRetryAction(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		RetryAction(mock.Anything, "org-1", "a-1").
		Return(agents.RetryActionResponse{OK: true}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.RetryAction(context.Background(), cfg, nil, cmds.RetryActionInput{
		ActionID: "a-1",
	})
	require.NoError(t, err)
	assert.True(t, result.OK)
}

func TestRetryAction_MissingID(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := agentsConfig(mockClient)

	_, err := cmds.RetryAction(context.Background(), cfg, nil, cmds.RetryActionInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "action id is required")
}
