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
// the mock. cfg.OrgID and cfg.AgentsEnabled are pre-set so the pure functions
// skip the auth / resolveOrg path (and its Agents-access gate) that the cobra
// wrapper handles.
func agentsConfig(mockClient *agentsmocks.MockClient) *config.Config {
	cfg := &config.Config{OrgID: "org-1", AgentsEnabled: true}
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
			Pagination: agents.PageMeta{Page: 1, PerPage: 2, Total: 5, TotalPages: 3},
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{
		Status: "open",
	})

	require.NoError(t, err)
	assert.Len(t, result.Findings, 2)
	assert.Equal(t, "f-1", result.Findings[0].ID)
	assert.Equal(t, 2, result.Findings[0].TaskTotal)
	// Savings annualize at the CLI boundary: floor(monthly × 12), matching
	// the dashboard. 120.50 → 1446, 80.25 → 963.
	assert.InDelta(t, 1446, result.Findings[0].EstimatedYearlySavings, 0.001)
	assert.InDelta(t, 2409, result.TotalYearlySavings, 0.001)
	assert.True(t, result.HasNextPage)
	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 2, result.NextPage)
	assert.Equal(t, 5, result.TotalFindings)
	assert.Equal(t, 3, result.TotalPages)
}

// The caller passes a page number through to the client as page / per_page —
// the findings list is offset-paginated, not cursor-paginated.
func TestListFindings_ForwardsPaging(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ListFindings(mock.Anything, "org-1", agents.ListFindingsParams{
			Status:  "open",
			Page:    3,
			PerPage: 10,
		}).
		Return(agents.FindingsPage{
			Items:      []agents.Finding{{ID: "f-1", Title: "x"}},
			Pagination: agents.PageMeta{Page: 3, PerPage: 10, Total: 30, TotalPages: 3},
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{
		Status: "open",
		Page:   3,
		Limit:  10,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, result.Page)
	assert.False(t, result.HasNextPage)
}

func TestListFindings_LastPage(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		ListFindings(mock.Anything, "org-1", mock.Anything).
		Return(agents.FindingsPage{
			Items: []agents.Finding{{ID: "f-1", Title: "x"}},
			// Final page — caller should see HasNextPage=false.
			Pagination: agents.PageMeta{Page: 2, PerPage: 50, Total: 51, TotalPages: 2},
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{})
	require.NoError(t, err)
	assert.False(t, result.HasNextPage)
	assert.Zero(t, result.NextPage)
}

func TestListFindings_NoOrg(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	cfg := &config.Config{}
	cfg.Agents.Client = func(_ *http.Client) agents.Client { return mockClient }

	_, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no organization selected")
}

func TestListFindings_AgentsNotEnabled(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	// Org is resolved but Agents isn't enabled for it — the gate should
	// short-circuit with the early-access message before any API call.
	cfg := &config.Config{OrgID: "org-1", OrgSlug: "acme", AgentsEnabled: false}
	cfg.Agents.Client = func(_ *http.Client) agents.Client { return mockClient }

	_, err := cmds.ListFindings(context.Background(), cfg, nil, cmds.FindingsListInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "early access")
	assert.Contains(t, err.Error(), "dashboard.infracost.io/org/acme/agents")
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

// findingWithTask builds the GetFinding payload the type resolver reads,
// carrying one task with the given suggested action.
func findingWithTask(taskID, suggestedAction string) agents.Finding {
	return agents.Finding{
		ID:     "f-1",
		Title:  "Idle EBS volumes",
		Status: "open",
		Tasks: []agents.Task{
			{ID: "other", FindingID: "f-1", SuggestedAction: "manual"},
			{ID: taskID, FindingID: "f-1", SuggestedAction: suggestedAction},
		},
	}
}

func TestPreviewFix(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		GetFinding(mock.Anything, "org-1", "f-1").
		Return(findingWithTask("t-1", "open_pr"), nil)
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

// The whole point of the default: a tagging task Agents downgraded to
// create_ticket must be drafted as a ticket, not as the PR the old
// hardcoded default would have asked for (which 500s server-side because
// the config LLM has no repo to name).
func TestPreviewFix_DefaultsToTaskSuggestedAction(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		GetFinding(mock.Anything, "org-1", "f-1").
		Return(findingWithTask("t-1", "create_ticket"), nil)
	mockClient.EXPECT().
		GenerateAction(mock.Anything, "org-1", "f-1", agents.GenerateActionRequest{
			TaskIDs:    []string{"t-1"},
			ActionType: agents.ActionTypeCreateTicket,
		}).
		Return(agents.GenerateActionResponse{
			Type:   agents.ActionTypeCreateTicket,
			Config: json.RawMessage(`{"title":"Tag the bucket"}`),
		}, nil)

	cfg := agentsConfig(mockClient)
	result, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "create_ticket", result.Type)
}

// An explicit type is the user overriding Agents on purpose — don't spend a
// round-trip second-guessing it. The mock has no GetFinding expectation, so
// the test fails if the resolver looks the finding up anyway.
func TestPreviewFix_ExplicitTypeSkipsLookup(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		GenerateAction(mock.Anything, "org-1", "f-1", agents.GenerateActionRequest{
			TaskIDs:    []string{"t-1"},
			ActionType: agents.ActionTypeOpenPR,
		}).
		Return(agents.GenerateActionResponse{Type: agents.ActionTypeOpenPR}, nil)

	cfg := agentsConfig(mockClient)
	_, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
		Type:      "open_pr",
	})
	require.NoError(t, err)
}

// A manual suggestion can't be drafted at all, so say so instead of
// silently drafting a PR the user didn't ask for.
func TestPreviewFix_RejectsManualSuggestion(t *testing.T) {
	mockClient := agentsmocks.NewMockClient(t)
	mockClient.EXPECT().
		GetFinding(mock.Anything, "org-1", "f-1").
		Return(findingWithTask("t-1", "manual"), nil)

	cfg := agentsConfig(mockClient)
	_, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
		FindingID: "f-1",
		TaskID:    "t-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "suggested action is manual")
}

// The lookup is a convenience, not a precondition: a read failure (or a
// task that carries no suggestion at all) falls back to the historical
// open_pr default rather than failing the command.
func TestPreviewFix_FallsBackWhenSuggestionUnavailable(t *testing.T) {
	for name, setup := range map[string]func(*agentsmocks.MockClient){
		"lookup fails": func(m *agentsmocks.MockClient) {
			m.EXPECT().GetFinding(mock.Anything, "org-1", "f-1").
				Return(agents.Finding{}, assert.AnError)
		},
		"no suggestion on task": func(m *agentsmocks.MockClient) {
			m.EXPECT().GetFinding(mock.Anything, "org-1", "f-1").
				Return(findingWithTask("t-1", ""), nil)
		},
		"task not in finding": func(m *agentsmocks.MockClient) {
			m.EXPECT().GetFinding(mock.Anything, "org-1", "f-1").
				Return(findingWithTask("t-other", "create_ticket"), nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			mockClient := agentsmocks.NewMockClient(t)
			setup(mockClient)
			mockClient.EXPECT().
				GenerateAction(mock.Anything, "org-1", "f-1", agents.GenerateActionRequest{
					TaskIDs:    []string{"t-1"},
					ActionType: agents.ActionTypeOpenPR,
				}).
				Return(agents.GenerateActionResponse{Type: agents.ActionTypeOpenPR}, nil)

			cfg := agentsConfig(mockClient)
			result, err := cmds.PreviewFix(context.Background(), cfg, nil, cmds.PreviewFixInput{
				FindingID: "f-1",
				TaskID:    "t-1",
			})
			require.NoError(t, err)
			assert.Equal(t, "open_pr", result.Type)
		})
	}
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
