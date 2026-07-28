// Package agents is the Go client for the Infracost Agents API. Agents is a separate
// service from the dashboard — it hosts "findings" (org-scoped
// investigation results) and their nested tasks / actions. Auth piggybacks
// on the dashboard's Auth0 JWT, so callers thread the same
// `cfg.Auth.Token()` source they use for the dashboard client.
//
// Shapes mirror coast/packages/types. Agents serializes its domain types
// straight to JSON, so response fields are camelCase — the casing the TS
// types declare (coast#515 renamed them from snake_case; a Go tag that
// still says snake_case decodes to the zero value silently, which is how
// suggestedAction / estimatedMonthlySavings went missing for a release).
// Request bodies are the exception: Agents' Zod schemas accept the CLI's
// historical snake_case keys, so those tags stay as they are.
//
// Numeric money fields are float64s because the underlying TS API
// serializes them that way.
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Finding is one org-scoped investigation result. Two shapes share this
// struct: list rows omit the heavy nested fields (Tasks / Actions /
// Events / Investigations / TriggerDetail); the GET-by-id detail
// response includes them. Optional fields use omitempty so the same
// struct round-trips both.
type Finding struct {
	ID                      string  `json:"id"`
	OrgID                   string  `json:"orgId,omitempty"`
	AgentID                 string  `json:"agentId,omitempty"`
	AgentName               string  `json:"agentName,omitempty"`
	AgentIcon               string  `json:"agentIcon,omitempty"`
	Title                   string  `json:"title"`
	Summary                 string  `json:"summary,omitempty"`
	EstimatedMonthlySavings float64 `json:"estimatedMonthlySavings,omitempty"`
	Effort                  string  `json:"effort,omitempty"`
	TaskTotal               int     `json:"taskTotal,omitempty"`
	TaskResolved            int     `json:"taskResolved,omitempty"`
	TaskInProgress          int     `json:"taskInProgress,omitempty"`
	Status                  string  `json:"status"`
	InvestigationStatus     string  `json:"investigationStatus,omitempty"`
	LifecycleState          string  `json:"lifecycleState,omitempty"`
	RemediationState        string  `json:"remediationState,omitempty"`
	TopTaskTitle            string  `json:"topTaskTitle,omitempty"`
	AccountID               string  `json:"accountId,omitempty"`
	AccountAlias            string  `json:"accountAlias,omitempty"`
	CreatedAt               string  `json:"createdAt,omitempty"`
	UpdatedAt               string  `json:"updatedAt,omitempty"`

	// Detail-only fields. The list endpoint leaves these unset.
	DuplicateOfID string          `json:"duplicateOfId,omitempty"`
	TriggerDetail json.RawMessage `json:"triggerDetail,omitempty"`
	Tasks         []Task          `json:"tasks,omitempty"`
	Events        []FindingEvent  `json:"events,omitempty"`
	Actions       []Action        `json:"actions,omitempty"`
	ResolvedAt    string          `json:"resolvedAt,omitempty"`
}

// Task is the unit users interact with when working on a finding. A
// finding groups one or more tasks; each task carries the description of
// what to do, an optional code snippet, and a SuggestedAction
// (open_pr | create_ticket | manual).
type Task struct {
	ID                string             `json:"id"`
	FindingID         string             `json:"findingId"`
	Index             int                `json:"index"`
	Title             string             `json:"title"`
	Detail            string             `json:"detail,omitempty"`
	ActionDescription string             `json:"actionDescription,omitempty"`
	Savings           float64            `json:"savings,omitempty"`
	Code              string             `json:"code,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
	SuggestedAction   string             `json:"suggestedAction,omitempty"`
	ActionContext     json.RawMessage    `json:"actionContext,omitempty"`
	Effort            string             `json:"effort,omitempty"`
	EffortNote        string             `json:"effortNote,omitempty"`
	Status            string             `json:"status"`
	DismissedReason   string             `json:"dismissedReason,omitempty"`
	Events            []FindingTaskEvent `json:"events,omitempty"`
	CreatedAt         string             `json:"createdAt,omitempty"`
	UpdatedAt         string             `json:"updatedAt,omitempty"`
}

// Action is a PR or ticket associated with a finding. The Config blob is
// opaque to the CLI — for PR-type actions it includes things like the
// branch name and draft body; for ticket types it's whatever the upstream
// ticketing integration needs.
type Action struct {
	ID              string          `json:"id"`
	FindingID       string          `json:"findingId"`
	Type            string          `json:"type"`
	ActionStatus    string          `json:"actionStatus"`
	ActionJobStatus string          `json:"actionJobStatus,omitempty"`
	Config          json.RawMessage `json:"config,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	PrURL           string          `json:"prUrl,omitempty"`
	TaskIDs         []string        `json:"taskIds,omitempty"`
	CreatedAt       string          `json:"createdAt,omitempty"`
	UpdatedAt       string          `json:"updatedAt,omitempty"`
}

// FindingEvent is one timeline entry on a finding (detected, investigated,
// resolved, …). Detail is carried opaquely; the CLI surfaces the event
// type + timestamp without parsing the per-event payload.
type FindingEvent struct {
	ID         string          `json:"id"`
	FindingID  string          `json:"findingId"`
	EventType  string          `json:"eventType"`
	Detail     json.RawMessage `json:"detail,omitempty"`
	OccurredAt string          `json:"occurredAt"`
}

// FindingTaskEvent is a per-task timeline entry. Same opacity story as
// FindingEvent.
type FindingTaskEvent struct {
	ID         string          `json:"id"`
	TaskID     string          `json:"taskId"`
	EventType  string          `json:"eventType"`
	Detail     json.RawMessage `json:"detail,omitempty"`
	OccurredAt string          `json:"occurredAt"`
}

// FindingsPage is the response shape for ListFindings. Findings are
// offset-paginated: the server echoes the page it actually applied plus
// the totals, and the caller asks for the next page by number.
type FindingsPage struct {
	Items      []Finding `json:"items"`
	Pagination PageMeta  `json:"pagination"`
}

// PageMeta is the server-applied pagination echoed on a page response.
// Page is 1-based; TotalPages is 0 when there are no items at all, so
// callers should compare against Page rather than assume a floor of 1.
type PageMeta struct {
	Page       int `json:"page"`
	PerPage    int `json:"perPage"`
	Total      int `json:"total"`
	TotalPages int `json:"totalPages"`
}

// HasNextPage reports whether another page follows the one described by
// this meta.
func (p PageMeta) HasNextPage() bool {
	return p.Page > 0 && p.Page < p.TotalPages
}

// ListFindingsParams scopes a ListFindings call. The Agents API accepts
// single values (not arrays) for status / effort, so the CLI's slice-
// shaped flags must collapse to one before calling. Page / PerPage are
// sent only when non-zero, letting the server apply its own defaults.
type ListFindingsParams struct {
	Status  string
	Effort  string
	Page    int
	PerPage int
}

// ActionType is the discriminator for GenerateAction / CreateAction. The
// generate endpoint only accepts the automatable types (open_pr,
// create_ticket); manual is creatable directly but the LLM can't draft
// it for you.
type ActionType string

const (
	ActionTypeOpenPR       ActionType = "open_pr"
	ActionTypeCreateTicket ActionType = "create_ticket"
	ActionTypeManual       ActionType = "manual"
)

// GenerateActionRequest is the POST body for the LLM draft endpoint. The
// API takes an array of task ids because a single PR / ticket can cover
// multiple related tasks; the CLI's preview-fix command wraps a single
// task id in a one-element slice.
type GenerateActionRequest struct {
	TaskIDs    []string   `json:"taskIds"`
	ActionType ActionType `json:"actionType"`
}

// GenerateActionResponse is the drafted action the LLM produced. The
// Config field is the opaque blob a subsequent CreateAction call would
// submit verbatim — the CLI hands it back to the user to review.
type GenerateActionResponse struct {
	Type   ActionType      `json:"type"`
	Config json.RawMessage `json:"config"`
}

// CreateActionRequest is the POST body for the actual create endpoint.
// Mirrors the GenerateAction response — the user runs preview-fix, edits
// the JSON if they want to, then pipes it into create-fix. TaskIDs is
// an array for the same batching reason GenerateAction uses one.
type CreateActionRequest struct {
	Type    ActionType      `json:"type"`
	Config  json.RawMessage `json:"config"`
	TaskIDs []string        `json:"taskIds"`
}

// CreateActionResponse is the lightweight response from POST
// /create-action. The Agents API only echoes the new action's id; the
// full Action row has to be fetched separately if the caller needs it.
type CreateActionResponse struct {
	OK       bool   `json:"ok"`
	ActionID string `json:"action_id"`
}

// FindingStatus is the discriminator accepted by the finding-status PATCH
// endpoint. The API rejects anything outside this set with a 400; we
// constrain at the type level so callers can't accidentally send a
// task-level status (e.g. "in_progress") which is a transition the
// finding lifecycle doesn't recognize.
type FindingStatus string

const (
	FindingStatusOpen      FindingStatus = "open"
	FindingStatusResolved  FindingStatus = "resolved"
	FindingStatusDismissed FindingStatus = "dismissed"
)

// UpdateFindingStatusRequest is the PATCH body. Reason is optional but
// recommended for dismissals — when supplied, the Agents API also creates
// an AgentLearning so the agent doesn't re-raise the same finding.
type UpdateFindingStatusRequest struct {
	Status FindingStatus `json:"status"`
	Reason string        `json:"reason,omitempty"`
}

// ReportType discriminates the two intent-shaped task verbs surfaced by
// POST /findings/tasks/:taskId/report. Confirmation advances any linked
// open manual actions to "done" so the observation cascade can verify;
// correction dismisses the task, dismisses linked draft/open actions, and
// emits an AgentLearning with source "correction".
type ReportType string

const (
	ReportTypeConfirmation ReportType = "confirmation"
	ReportTypeCorrection   ReportType = "correction"
)

// ReportTaskRequest is the POST body for the task-report endpoint.
// Content is required for corrections (the user's reason, persisted as
// dismissed_reason and emitted as the learning content) and ignored for
// confirmations.
type ReportTaskRequest struct {
	Type    ReportType `json:"type"`
	Content string     `json:"content,omitempty"`
}

// TaskStatus is the discriminator accepted by the task-status PATCH
// endpoint. Agents itself recognizes more transient states
// (`in_progress`, `awaiting_resolution`, `auto_*`) which are normally
// derived from the action lifecycle rather than set by hand; the CLI
// exposes only the user-driven targets, with [TaskStatusDismissed]
// being the one the unified `update_task_status` verb routes here for.
type TaskStatus string

const (
	TaskStatusOpen      TaskStatus = "open"
	TaskStatusResolved  TaskStatus = "resolved"
	TaskStatusDismissed TaskStatus = "dismissed"
)

// UpdateTaskStatusRequest is the PATCH body. DismissedReason is only
// honored by Agents when status is `dismissed` — for other targets the
// field is ignored server-side, so omitempty keeps the wire shape clean.
type UpdateTaskStatusRequest struct {
	Status          TaskStatus `json:"status"`
	DismissedReason string     `json:"dismissedReason,omitempty"`
}

// ReportTaskResponse is the response shape for both report types. The
// LearningID / DismissedActionIDs / ConfirmedActionIDs slots are
// type-specific — correction populates the first two, confirmation
// populates the last — and omitempty keeps the wire shape tight on the
// branch that didn't apply.
//
// The snake_case tags here are deliberate: this endpoint hand-builds its
// response literal rather than serializing a domain type, so it kept the
// original casing when coast#515 renamed the types. Same for
// CreateActionResponse.ActionID. The nested Task IS a domain type, so its
// fields are camelCase.
type ReportTaskResponse struct {
	OK                 bool     `json:"ok"`
	Task               Task     `json:"task"`
	LearningID         string   `json:"learning_id,omitempty"`
	DismissedActionIDs []string `json:"dismissed_action_ids,omitempty"`
	ConfirmedActionIDs []string `json:"confirmed_action_ids,omitempty"`
}

// RetryActionResponse is the OK-only shape from POST
// /actions/:actionId/retry. The action's new state has to be re-read via
// GetFinding if the caller wants to inspect it.
type RetryActionResponse struct {
	OK bool `json:"ok"`
}

// Client is the interface the CLI calls into. Methods are scoped by org —
// the caller passes orgID explicitly rather than hiding it in client state
// because some flows (e.g. MCP's set_org) switch orgs mid-session.
type Client interface {
	ListFindings(ctx context.Context, orgID string, params ListFindingsParams) (FindingsPage, error)
	GetFinding(ctx context.Context, orgID, findingID string) (Finding, error)
	GenerateAction(ctx context.Context, orgID, findingID string, req GenerateActionRequest) (GenerateActionResponse, error)
	CreateAction(ctx context.Context, orgID, findingID string, req CreateActionRequest) (CreateActionResponse, error)
	UpdateFindingStatus(ctx context.Context, orgID, findingID string, req UpdateFindingStatusRequest) (Finding, error)
	ReportTask(ctx context.Context, orgID, taskID string, req ReportTaskRequest) (ReportTaskResponse, error)
	UpdateTaskStatus(ctx context.Context, orgID, taskID string, req UpdateTaskStatusRequest) (Task, error)
	RetryAction(ctx context.Context, orgID, actionID string) (RetryActionResponse, error)
}

var _ Client = (*client)(nil)

type client struct {
	client *http.Client
	config *Config
}

func (c *client) ListFindings(ctx context.Context, orgID string, params ListFindingsParams) (FindingsPage, error) {
	q := url.Values{}
	if params.Status != "" {
		q.Set("status", params.Status)
	}
	if params.Effort != "" {
		q.Set("effort", params.Effort)
	}
	if params.Page > 0 {
		q.Set("page", strconv.Itoa(params.Page))
	}
	if params.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(params.PerPage))
	}

	path := fmt.Sprintf("/org/%s/findings", url.PathEscape(orgID))
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var out FindingsPage
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return FindingsPage{}, err
	}
	return out, nil
}

func (c *client) GetFinding(ctx context.Context, orgID, findingID string) (Finding, error) {
	path := fmt.Sprintf("/org/%s/findings/%s", url.PathEscape(orgID), url.PathEscape(findingID))
	var out Finding
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return Finding{}, err
	}
	return out, nil
}

func (c *client) GenerateAction(ctx context.Context, orgID, findingID string, req GenerateActionRequest) (GenerateActionResponse, error) {
	path := fmt.Sprintf("/org/%s/findings/%s/generate-action", url.PathEscape(orgID), url.PathEscape(findingID))
	var out GenerateActionResponse
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return GenerateActionResponse{}, err
	}
	return out, nil
}

func (c *client) CreateAction(ctx context.Context, orgID, findingID string, req CreateActionRequest) (CreateActionResponse, error) {
	path := fmt.Sprintf("/org/%s/findings/%s/create-action", url.PathEscape(orgID), url.PathEscape(findingID))
	var out CreateActionResponse
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return CreateActionResponse{}, err
	}
	return out, nil
}

func (c *client) UpdateFindingStatus(ctx context.Context, orgID, findingID string, req UpdateFindingStatusRequest) (Finding, error) {
	path := fmt.Sprintf("/org/%s/findings/%s", url.PathEscape(orgID), url.PathEscape(findingID))
	var out Finding
	if err := c.do(ctx, http.MethodPatch, path, req, &out); err != nil {
		return Finding{}, err
	}
	return out, nil
}

func (c *client) ReportTask(ctx context.Context, orgID, taskID string, req ReportTaskRequest) (ReportTaskResponse, error) {
	path := fmt.Sprintf("/org/%s/findings/tasks/%s/report", url.PathEscape(orgID), url.PathEscape(taskID))
	var out ReportTaskResponse
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return ReportTaskResponse{}, err
	}
	return out, nil
}

func (c *client) UpdateTaskStatus(ctx context.Context, orgID, taskID string, req UpdateTaskStatusRequest) (Task, error) {
	path := fmt.Sprintf("/org/%s/findings/tasks/%s", url.PathEscape(orgID), url.PathEscape(taskID))
	var out Task
	if err := c.do(ctx, http.MethodPatch, path, req, &out); err != nil {
		return Task{}, err
	}
	return out, nil
}

func (c *client) RetryAction(ctx context.Context, orgID, actionID string) (RetryActionResponse, error) {
	path := fmt.Sprintf("/org/%s/findings/actions/%s/retry", url.PathEscape(orgID), url.PathEscape(actionID))
	var out RetryActionResponse
	if err := c.do(ctx, http.MethodPost, path, struct{}{}, &out); err != nil {
		return RetryActionResponse{}, err
	}
	return out, nil
}

// do is the shared request/response plumbing. The endpoint is treated as a
// prefix (matching the dashboard client's convention); paths are joined
// with a single slash.
func (c *client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reader = buf
	}

	endpoint := c.config.Endpoint + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req) // #nosec G107 -- endpoint origin is config-controlled
	if err != nil {
		return fmt.Errorf("calling Agents API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return &APIError{Status: resp.StatusCode, Body: string(bytes.TrimSpace(respBody))}
	}

	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

// APIError is returned for non-2xx responses. The Body is capped so a
// runaway HTML 502 from a misconfigured proxy doesn't dominate the CLI's
// error output.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("Agents API returned status %d", e.Status)
	}
	return fmt.Sprintf("Agents API returned status %d: %s", e.Status, e.Body)
}
