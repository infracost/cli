package agents_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/agents"
)

// newTestClient spins up an httptest server that routes by method+path,
// constructs a real Agents client pointed at it, and returns both so each
// test can assert request shape and response handling end-to-end.
func newTestClient(t *testing.T, handler http.HandlerFunc) agents.Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := &agents.Config{Endpoint: srv.URL}
	cfg.Process()
	return cfg.Client(srv.Client())
}

func TestClientListFindings(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/org/org-1/findings", r.URL.Path)
		// Agents accepts single-valued status / effort; the client should
		// forward exactly what the caller passed.
		assert.Equal(t, "open", r.URL.Query().Get("status"))
		assert.Equal(t, "small", r.URL.Query().Get("effort"))
		assert.Equal(t, "abc", r.URL.Query().Get("cursor"))
		assert.Equal(t, "25", r.URL.Query().Get("limit"))

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":"f-1","title":"Hello","status":"open"}],"next_cursor":"next"}`)
	})

	page, err := client.ListFindings(context.Background(), "org-1", agents.ListFindingsParams{
		Status: "open",
		Effort: "small",
		Cursor: "abc",
		Limit:  25,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "f-1", page.Items[0].ID)
	assert.Equal(t, "next", page.NextCursor)
}

func TestClientGetFindingURLEncoding(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// IDs with slashes would otherwise break path routing — confirm
		// the client url-escapes them on the wire.
		assert.Equal(t, "/org/org%2F1/findings/abc", r.URL.EscapedPath())
		assert.Equal(t, "/org/org/1/findings/abc", r.URL.Path)
		_, _ = io.WriteString(w, `{"id":"abc","title":"x","status":"open"}`)
	})

	f, err := client.GetFinding(context.Background(), "org/1", "abc")
	require.NoError(t, err)
	assert.Equal(t, "abc", f.ID)
}

func TestClientGenerateAction(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/org/org-1/findings/f-1/generate-action", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"task_ids":["t-1","t-2"],"action_type":"open_pr"}`, string(body))

		_, _ = io.WriteString(w, `{"type":"open_pr","config":{"branch":"x"}}`)
	})

	resp, err := client.GenerateAction(context.Background(), "org-1", "f-1", agents.GenerateActionRequest{
		TaskIDs:    []string{"t-1", "t-2"},
		ActionType: agents.ActionTypeOpenPR,
	})
	require.NoError(t, err)
	assert.Equal(t, agents.ActionTypeOpenPR, resp.Type)
	assert.JSONEq(t, `{"branch":"x"}`, string(resp.Config))
}

func TestClientCreateAction(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/org/org-1/findings/f-1/create-action", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"type":"open_pr","config":{"branch":"x"},"task_ids":["t-1"]}`, string(body))

		_, _ = io.WriteString(w, `{"ok":true,"action_id":"a-1"}`)
	})

	resp, err := client.CreateAction(context.Background(), "org-1", "f-1", agents.CreateActionRequest{
		Type:    agents.ActionTypeOpenPR,
		Config:  json.RawMessage(`{"branch":"x"}`),
		TaskIDs: []string{"t-1"},
	})
	require.NoError(t, err)
	assert.True(t, resp.OK)
	assert.Equal(t, "a-1", resp.ActionID)
}

func TestClientUpdateFindingStatus(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/org/org-1/findings/f-1", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"status":"dismissed","reason":"not real"}`, string(body))

		_, _ = io.WriteString(w, `{"id":"f-1","title":"Hello","status":"dismissed"}`)
	})

	f, err := client.UpdateFindingStatus(context.Background(), "org-1", "f-1", agents.UpdateFindingStatusRequest{
		Status: agents.FindingStatusDismissed,
		Reason: "not real",
	})
	require.NoError(t, err)
	assert.Equal(t, "f-1", f.ID)
	assert.Equal(t, "dismissed", f.Status)
}

func TestClientUpdateFindingStatusOmitsEmptyReason(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// omitempty on Reason means a plain resolve doesn't ship an empty
		// "reason":"" the API would either ignore or surface back; the JSON
		// is just {status}.
		assert.JSONEq(t, `{"status":"resolved"}`, string(body))

		_, _ = io.WriteString(w, `{"id":"f-1","title":"Hello","status":"resolved"}`)
	})

	_, err := client.UpdateFindingStatus(context.Background(), "org-1", "f-1", agents.UpdateFindingStatusRequest{
		Status: agents.FindingStatusResolved,
	})
	require.NoError(t, err)
}

func TestClientReportTaskConfirmation(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/org/org-1/findings/tasks/t-1/report", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		// Content is omitempty: a confirmation report doesn't carry it.
		assert.JSONEq(t, `{"type":"confirmation"}`, string(body))

		_, _ = io.WriteString(w, `{"ok":true,"task":{"id":"t-1","finding_id":"f-1","index":0,"title":"x","status":"awaiting_resolution"},"confirmed_action_ids":["a-1"]}`)
	})

	resp, err := client.ReportTask(context.Background(), "org-1", "t-1", agents.ReportTaskRequest{
		Type: agents.ReportTypeConfirmation,
	})
	require.NoError(t, err)
	assert.True(t, resp.OK)
	assert.Equal(t, []string{"a-1"}, resp.ConfirmedActionIDs)
	assert.Empty(t, resp.DismissedActionIDs)
}

func TestClientReportTaskCorrection(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"type":"correction","content":"not relevant"}`, string(body))

		_, _ = io.WriteString(w, `{"ok":true,"task":{"id":"t-1","finding_id":"f-1","index":0,"title":"x","status":"dismissed"},"learning_id":"l-1","dismissed_action_ids":["a-1","a-2"]}`)
	})

	resp, err := client.ReportTask(context.Background(), "org-1", "t-1", agents.ReportTaskRequest{
		Type:    agents.ReportTypeCorrection,
		Content: "not relevant",
	})
	require.NoError(t, err)
	assert.Equal(t, "l-1", resp.LearningID)
	assert.Equal(t, []string{"a-1", "a-2"}, resp.DismissedActionIDs)
}

func TestClientUpdateTaskStatus(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/org/org-1/findings/tasks/t-1", r.URL.Path)

		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{"status":"dismissed","dismissed_reason":"not applicable to this account"}`, string(body))

		_, _ = io.WriteString(w, `{"id":"t-1","finding_id":"f-1","index":0,"title":"x","status":"dismissed","dismissed_reason":"not applicable to this account"}`)
	})

	task, err := client.UpdateTaskStatus(context.Background(), "org-1", "t-1", agents.UpdateTaskStatusRequest{
		Status:          agents.TaskStatusDismissed,
		DismissedReason: "not applicable to this account",
	})
	require.NoError(t, err)
	assert.Equal(t, "dismissed", task.Status)
	assert.Equal(t, "not applicable to this account", task.DismissedReason)
}

func TestClientUpdateTaskStatusOmitsEmptyReason(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// omitempty on DismissedReason: a plain reopen ships just {status}.
		assert.JSONEq(t, `{"status":"open"}`, string(body))

		_, _ = io.WriteString(w, `{"id":"t-1","finding_id":"f-1","index":0,"title":"x","status":"open"}`)
	})

	_, err := client.UpdateTaskStatus(context.Background(), "org-1", "t-1", agents.UpdateTaskStatusRequest{
		Status: agents.TaskStatusOpen,
	})
	require.NoError(t, err)
}

func TestClientRetryAction(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/org/org-1/findings/actions/a-1/retry", r.URL.Path)
		// No content even though there's no payload to send; the empty
		// struct keeps the request shape consistent with other POSTs.
		body, _ := io.ReadAll(r.Body)
		assert.JSONEq(t, `{}`, string(body))

		_, _ = io.WriteString(w, `{"ok":true}`)
	})

	resp, err := client.RetryAction(context.Background(), "org-1", "a-1")
	require.NoError(t, err)
	assert.True(t, resp.OK)
}

func TestClientAPIError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"nope"}`)
	})

	_, err := client.ListFindings(context.Background(), "org-1", agents.ListFindingsParams{})
	require.Error(t, err)

	var apiErr *agents.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.Status)
	assert.Contains(t, apiErr.Body, "nope")
}

func TestClientAPIErrorBodyCap(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		// A misbehaving proxy returning a giant HTML page shouldn't be
		// echoed verbatim into the CLI's error output.
		_, _ = io.WriteString(w, strings.Repeat("A", 100*1024))
	})

	_, err := client.ListFindings(context.Background(), "org-1", agents.ListFindingsParams{})
	require.Error(t, err)
	var apiErr *agents.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.LessOrEqual(t, len(apiErr.Body), 8*1024)
}
