package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/agents"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// FindingsListInput is the parsed input for `findings list`. Shared by
// the CLI wrapper and the MCP `findings_list` tool.
type FindingsListInput struct {
	Status string `json:"status,omitempty" jsonschema:"Filter by finding status (open, in_progress, resolved, dismissed, duplicate). Single value; the Agents API does not accept multiples. \"open\" matches both open and in_progress."`
	Effort string `json:"effort,omitempty" jsonschema:"Filter by effort level (trivial, small, medium, large). Single value."`
	Page   int    `json:"page,omitempty" jsonschema:"1-based page to fetch. Empty or 0 starts at the first page; the response's next_page tells you what to pass next."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum number of findings to return per page (server caps at 200, default 50)."`
}

// FindingOutput mirrors agents.Finding on the CLI / MCP wire shape, with
// savings quoted per year instead of the per-month figures the Agents
// API reports. The dashboard made the same switch (its
// Format.annualizedDollars floors after ×12); annualizeDollars keeps the
// two surfaces quoting identical numbers.
type FindingOutput struct {
	ID                     string  `json:"id"`
	OrgID                  string  `json:"orgId,omitempty"`
	AgentID                string  `json:"agentId,omitempty"`
	AgentName              string  `json:"agentName,omitempty"`
	AgentIcon              string  `json:"agentIcon,omitempty"`
	Title                  string  `json:"title"`
	Summary                string  `json:"summary,omitempty"`
	EstimatedYearlySavings float64 `json:"estimatedYearlySavings,omitempty" jsonschema:"Estimated savings per year if the finding is resolved, in whole currency units."`
	Effort                 string  `json:"effort,omitempty"`
	TaskTotal              int     `json:"taskTotal,omitempty"`
	TaskResolved           int     `json:"taskResolved,omitempty"`
	TaskInProgress         int     `json:"taskInProgress,omitempty"`
	Status                 string  `json:"status"`
	InvestigationStatus    string  `json:"investigationStatus,omitempty"`
	LifecycleState         string  `json:"lifecycleState,omitempty"`
	RemediationState       string  `json:"remediationState,omitempty"`
	TopTaskTitle           string  `json:"topTaskTitle,omitempty"`
	AccountID              string  `json:"accountId,omitempty"`
	AccountAlias           string  `json:"accountAlias,omitempty"`
	CreatedAt              string  `json:"createdAt,omitempty"`
	UpdatedAt              string  `json:"updatedAt,omitempty"`

	DuplicateOfID string                `json:"duplicateOfId,omitempty"`
	TriggerDetail json.RawMessage       `json:"triggerDetail,omitempty"`
	Tasks         []TaskOutput          `json:"tasks,omitempty"`
	Events        []agents.FindingEvent `json:"events,omitempty"`
	Actions       []agents.Action       `json:"actions,omitempty"`
	ResolvedAt    string                `json:"resolvedAt,omitempty"`
}

// TaskOutput mirrors agents.Task with the same yearly-savings conversion
// as FindingOutput.
type TaskOutput struct {
	ID                string                    `json:"id"`
	FindingID         string                    `json:"findingId"`
	Index             int                       `json:"index"`
	Title             string                    `json:"title"`
	Detail            string                    `json:"detail,omitempty"`
	ActionDescription string                    `json:"actionDescription,omitempty"`
	YearlySavings     float64                   `json:"yearlySavings,omitempty" jsonschema:"Estimated savings per year if the task is done, in whole currency units."`
	Code              string                    `json:"code,omitempty"`
	Warnings          []string                  `json:"warnings,omitempty"`
	SuggestedAction   string                    `json:"suggestedAction,omitempty"`
	ActionContext     json.RawMessage           `json:"actionContext,omitempty"`
	Effort            string                    `json:"effort,omitempty"`
	EffortNote        string                    `json:"effortNote,omitempty"`
	Status            string                    `json:"status"`
	DismissedReason   string                    `json:"dismissedReason,omitempty"`
	Events            []agents.FindingTaskEvent `json:"events,omitempty"`
	CreatedAt         string                    `json:"createdAt,omitempty"`
	UpdatedAt         string                    `json:"updatedAt,omitempty"`
}

// annualizeDollars converts an Agents-API monthly money amount to the
// yearly figure the CLI quotes. Floors after multiplying, matching the
// dashboard's Format.annualizedDollars, so both surfaces show the same
// whole-dollar number.
func annualizeDollars(monthly float64) float64 {
	return math.Floor(monthly * 12)
}

// findingOutput converts an API finding to its wire/render shape,
// annualizing savings on the finding and each nested task.
func findingOutput(f agents.Finding) FindingOutput {
	tasks := make([]TaskOutput, 0, len(f.Tasks))
	for _, t := range f.Tasks {
		tasks = append(tasks, taskOutput(t))
	}
	if len(tasks) == 0 {
		tasks = nil
	}
	return FindingOutput{
		ID:                     f.ID,
		OrgID:                  f.OrgID,
		AgentID:                f.AgentID,
		AgentName:              f.AgentName,
		AgentIcon:              f.AgentIcon,
		Title:                  f.Title,
		Summary:                f.Summary,
		EstimatedYearlySavings: annualizeDollars(f.EstimatedMonthlySavings),
		Effort:                 f.Effort,
		TaskTotal:              f.TaskTotal,
		TaskResolved:           f.TaskResolved,
		TaskInProgress:         f.TaskInProgress,
		Status:                 f.Status,
		InvestigationStatus:    f.InvestigationStatus,
		LifecycleState:         f.LifecycleState,
		RemediationState:       f.RemediationState,
		TopTaskTitle:           f.TopTaskTitle,
		AccountID:              f.AccountID,
		AccountAlias:           f.AccountAlias,
		CreatedAt:              f.CreatedAt,
		UpdatedAt:              f.UpdatedAt,
		DuplicateOfID:          f.DuplicateOfID,
		TriggerDetail:          f.TriggerDetail,
		Tasks:                  tasks,
		Events:                 f.Events,
		Actions:                f.Actions,
		ResolvedAt:             f.ResolvedAt,
	}
}

// taskOutput converts an API task to its wire/render shape.
func taskOutput(t agents.Task) TaskOutput {
	return TaskOutput{
		ID:                t.ID,
		FindingID:         t.FindingID,
		Index:             t.Index,
		Title:             t.Title,
		Detail:            t.Detail,
		ActionDescription: t.ActionDescription,
		YearlySavings:     annualizeDollars(t.Savings),
		Code:              t.Code,
		Warnings:          t.Warnings,
		SuggestedAction:   t.SuggestedAction,
		ActionContext:     t.ActionContext,
		Effort:            t.Effort,
		EffortNote:        t.EffortNote,
		Status:            t.Status,
		DismissedReason:   t.DismissedReason,
		Events:            t.Events,
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
	}
}

// FindingsListResult is the typed output of `findings list`.
type FindingsListResult struct {
	Findings []FindingOutput `json:"findings"`
	// TotalYearlySavings sums EstimatedYearlySavings across the returned
	// page so the human and LLM renderers don't have to recompute it. It's
	// a page total, not an org total — paging would accumulate
	// independently.
	TotalYearlySavings float64 `json:"total_yearly_savings"`
	// Page / TotalFindings / TotalPages echo the server-applied paging so a
	// caller can tell a single-page org from a truncated first page.
	Page          int  `json:"page,omitempty"`
	TotalFindings int  `json:"total_findings,omitempty"`
	TotalPages    int  `json:"total_pages,omitempty"`
	NextPage      int  `json:"next_page,omitempty"`
	HasNextPage   bool `json:"has_next_page,omitempty"`
}

// FindingsGetInput is the parsed input for `findings get`.
type FindingsGetInput struct {
	ID string `json:"id" jsonschema:"Finding ID to fetch. Required."`
}

// FindingsGetResult wraps a single FindingOutput so the typed-result
// shape is consistent with FindingsList.
type FindingsGetResult struct {
	Finding FindingOutput `json:"finding"`
}

// ListFindings calls the Agents API. The pure function returns the
// raw Finding rows the API emitted, plus a TotalSavings convenience
// roll-up and the server-applied paging.
func ListFindings(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in FindingsListInput) (FindingsListResult, error) {
	if cfg.OrgID == "" {
		return FindingsListResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return FindingsListResult{}, err
	}
	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	page, err := client.ListFindings(ctx, cfg.OrgID, agents.ListFindingsParams{
		Status:  in.Status,
		Effort:  in.Effort,
		Page:    in.Page,
		PerPage: in.Limit,
	})
	if err != nil {
		return FindingsListResult{}, fmt.Errorf("listing findings: %w", err)
	}

	findings := make([]FindingOutput, 0, len(page.Items))
	for _, f := range page.Items {
		findings = append(findings, findingOutput(f))
	}
	result := FindingsListResult{
		Findings:      findings,
		Page:          page.Pagination.Page,
		TotalFindings: page.Pagination.Total,
		TotalPages:    page.Pagination.TotalPages,
		HasNextPage:   page.Pagination.HasNextPage(),
	}
	if result.HasNextPage {
		result.NextPage = page.Pagination.Page + 1
	}
	for _, f := range findings {
		result.TotalYearlySavings += f.EstimatedYearlySavings
	}
	return result, nil
}

// GetFinding fetches one finding by id, including its full nested tasks
// / actions / events payload.
func GetFinding(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in FindingsGetInput) (FindingsGetResult, error) {
	if in.ID == "" {
		return FindingsGetResult{}, fmt.Errorf("finding id is required")
	}
	if cfg.OrgID == "" {
		return FindingsGetResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return FindingsGetResult{}, err
	}
	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	f, err := client.GetFinding(ctx, cfg.OrgID, in.ID)
	if err != nil {
		return FindingsGetResult{}, fmt.Errorf("fetching finding: %w", err)
	}
	return FindingsGetResult{Finding: findingOutput(f)}, nil
}

// UpdateFindingStatusInput is the parsed input for `findings update`. The
// CLI flag layer carries the user-supplied strings; UpdateFindingStatus
// validates them against the canonical FindingStatus set before calling
// Agents so the wire-shape stays honest about which values are accepted.
type UpdateFindingStatusInput struct {
	ID     string `json:"id" jsonschema:"Finding ID to update. Required."`
	Status string `json:"status" jsonschema:"New status: open, resolved, or dismissed. Required."`
	Reason string `json:"reason,omitempty" jsonschema:"Optional reason — recommended for dismissals; the text is also emitted as an AgentLearning so the agent doesn't re-raise the same finding."`
}

// UpdateFindingStatusResult wraps the freshly-updated finding so
// renderers and the MCP wire shape match FindingsGetResult.
type UpdateFindingStatusResult struct {
	Finding FindingOutput `json:"finding"`
}

// UpdateFindingStatus calls Agents' PATCH /findings/:id with the
// validated status. Dismissals with a non-empty reason additionally
// create an AgentLearning server-side; the CLI exposes the same shape
// by validating the inputs and forwarding the rest verbatim.
func UpdateFindingStatus(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in UpdateFindingStatusInput) (UpdateFindingStatusResult, error) {
	if in.ID == "" {
		return UpdateFindingStatusResult{}, fmt.Errorf("finding id is required")
	}
	status, err := resolveFindingStatus(in.Status)
	if err != nil {
		return UpdateFindingStatusResult{}, err
	}
	if cfg.OrgID == "" {
		return UpdateFindingStatusResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return UpdateFindingStatusResult{}, err
	}

	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	f, err := client.UpdateFindingStatus(ctx, cfg.OrgID, in.ID, agents.UpdateFindingStatusRequest{
		Status: status,
		Reason: in.Reason,
	})
	if err != nil {
		return UpdateFindingStatusResult{}, fmt.Errorf("updating finding status: %w", err)
	}
	return UpdateFindingStatusResult{Finding: findingOutput(f)}, nil
}

// resolveFindingStatus maps the user-facing CLI / MCP string onto the
// canonical FindingStatus. Rejects anything outside the three values
// Agents' PATCH endpoint accepts so misuse fails locally rather than
// 400ing across the network.
func resolveFindingStatus(s string) (agents.FindingStatus, error) {
	switch s {
	case "open":
		return agents.FindingStatusOpen, nil
	case "resolved":
		return agents.FindingStatusResolved, nil
	case "dismissed":
		return agents.FindingStatusDismissed, nil
	default:
		return "", fmt.Errorf("invalid status %q: must be one of open, resolved, dismissed", s)
	}
}

// FindingsCmd builds the `findings` parent command and its subcommands.
func FindingsCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "findings",
		Short:  "List and inspect FinOps findings for the current organization",
		Hidden: true,
	}
	cmd.AddCommand(findingsListCmd(cfg))
	cmd.AddCommand(findingsGetCmd(cfg))
	cmd.AddCommand(findingsUpdateCmd(cfg))
	return cmd
}

func findingsUpdateCmd(cfg *config.Config) *cobra.Command {
	var in UpdateFindingStatusInput
	cmd := &cobra.Command{
		Use:   "update <finding-id>",
		Short: "Update a finding's status (open / resolved / dismissed)",
		Long: `Set a finding's status. Dismissing with --reason persists the reason on
the finding and also emits an AgentLearning so the agent doesn't re-raise
the same finding on future scans.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.ID = args[0]
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result UpdateFindingStatusResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Updating finding...", "Finding updated", func(ctx context.Context) error {
				var uErr error
				result, uErr = UpdateFindingStatus(ctx, cfg, source, in)
				return uErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, updateFindingStatusRenderers())
		},
	}
	cmd.Flags().StringVar(&in.Status, "status", "", "New status: open, resolved, or dismissed (required)")
	cmd.Flags().StringVar(&in.Reason, "reason", "", "Optional reason (recommended for dismissals)")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

func updateFindingStatusRenderers() Renderers[UpdateFindingStatusResult] {
	return Renderers[UpdateFindingStatusResult]{
		Human: renderUpdateFindingStatusHuman,
		JSON:  renderJSON[UpdateFindingStatusResult],
		LLM:   renderJSON[UpdateFindingStatusResult],
	}
}

func renderUpdateFindingStatusHuman(w io.Writer, r UpdateFindingStatusResult) error {
	f := r.Finding
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "%s %s %s\n",
		ui.Bold(ui.Accent(f.Title)),
		ui.Mutedf("(%s)", f.ID),
		ui.Muted("→"),
	)
	_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Status:"), statusText(f.Status))
	if f.ResolvedAt != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Resolved at:"), f.ResolvedAt)
	}
	_, _ = fmt.Fprintln(w)
	return nil
}

func findingsListCmd(cfg *config.Config) *cobra.Command {
	var in FindingsListInput
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List findings for the current organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result FindingsListResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Loading findings...", "Findings loaded", func(ctx context.Context) error {
				var lErr error
				result, lErr = ListFindings(ctx, cfg, source, in)
				return lErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, findingsListRenderers())
		},
	}
	cmd.Flags().StringVar(&in.Status, "status", "", "Filter by status (open, in_progress, resolved, dismissed, duplicate)")
	cmd.Flags().StringVar(&in.Effort, "effort", "", "Filter by effort (trivial, small, medium, large)")
	cmd.Flags().IntVar(&in.Page, "page", 0, "1-based page to fetch (default the first page)")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "Maximum number of findings to return per page (server caps at 200, default 50)")
	return cmd
}

func findingsGetCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <finding-id>",
		Short: "Show a finding's details, including its tasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result FindingsGetResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Loading finding...", "Finding loaded", func(ctx context.Context) error {
				var gErr error
				result, gErr = GetFinding(ctx, cfg, source, FindingsGetInput{ID: args[0]})
				return gErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, findingsGetRenderers())
		},
	}
	return cmd
}

func findingsListRenderers() Renderers[FindingsListResult] {
	return Renderers[FindingsListResult]{
		Human: renderFindingsListHuman,
		JSON:  renderJSON[FindingsListResult],
		LLM:   renderJSON[FindingsListResult],
	}
}

func findingsGetRenderers() Renderers[FindingsGetResult] {
	return Renderers[FindingsGetResult]{
		Human: renderFindingsGetHuman,
		JSON:  renderJSON[FindingsGetResult],
		LLM:   renderJSON[FindingsGetResult],
	}
}

func renderFindingsListHuman(w io.Writer, r FindingsListResult) error {
	_, _ = fmt.Fprintln(w)

	if len(r.Findings) == 0 {
		_, _ = fmt.Fprintln(w, ui.Muted("No findings for this organization."))
		_, _ = fmt.Fprintln(w)
		return nil
	}

	ui.Heading("Findings")
	_, _ = fmt.Fprintln(w)
	for _, f := range r.Findings {
		writeFindingSummary(w, f)
	}

	if r.TotalYearlySavings > 0 {
		_, _ = fmt.Fprintf(w, "%s $%s/yr across %d findings on this page\n",
			ui.Bold("Estimated savings:"),
			formatFloat(r.TotalYearlySavings),
			len(r.Findings),
		)
	}
	if r.HasNextPage {
		_, _ = fmt.Fprintf(w, "%s pass %s to see the next page.\n",
			ui.Mutedf("Showing %d of %d findings —", len(r.Findings), r.TotalFindings),
			ui.Codef("--page %d", r.NextPage),
		)
	}
	_, _ = fmt.Fprintln(w)
	return nil
}

// writeFindingSummary renders one finding row in the list view. The
// effort badge replaces what would otherwise be a severity tag —
// Agents doesn't model severity, but "trivial → large" carries the same
// "how much work is this" signal.
func writeFindingSummary(w io.Writer, f FindingOutput) {
	badge := effortBadge(f.Effort)
	prefix := ""
	if badge != "" {
		prefix = badge + " "
	}
	_, _ = fmt.Fprintf(w, "%s%s  %s\n",
		prefix,
		ui.Bold(ui.Accent(f.Title)),
		ui.Mutedf("(%s)", f.ID),
	)
	if f.Status != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Status:"), statusText(f.Status))
	}
	if f.Summary != "" {
		_, _ = fmt.Fprintln(w, "  "+f.Summary)
	}
	if f.EstimatedYearlySavings > 0 {
		_, _ = fmt.Fprintf(w, "  %s $%s/yr",
			ui.Muted("Saving:"),
			ui.Positive(formatFloat(f.EstimatedYearlySavings)),
		)
		if f.TaskTotal > 0 {
			_, _ = fmt.Fprintf(w, "    %s %d", ui.Muted("Tasks:"), f.TaskTotal)
		}
		_, _ = fmt.Fprintln(w)
	} else if f.TaskTotal > 0 {
		_, _ = fmt.Fprintf(w, "  %s %d\n", ui.Muted("Tasks:"), f.TaskTotal)
	}
	_, _ = fmt.Fprintln(w)
}

func renderFindingsGetHuman(w io.Writer, r FindingsGetResult) error {
	_, _ = fmt.Fprintln(w)
	f := r.Finding

	badge := effortBadge(f.Effort)
	prefix := ""
	if badge != "" {
		prefix = badge + " "
	}
	_, _ = fmt.Fprintf(w, "%s%s  %s\n",
		prefix,
		ui.Bold(ui.Accent(f.Title)),
		ui.Mutedf("(%s)", f.ID),
	)
	if f.Status != "" {
		_, _ = fmt.Fprintf(w, "%s %s\n", ui.Muted("Status:"), statusText(f.Status))
	}
	if f.EstimatedYearlySavings > 0 {
		_, _ = fmt.Fprintf(w, "%s $%s/yr\n",
			ui.Muted("Estimated saving:"),
			ui.Positive(formatFloat(f.EstimatedYearlySavings)),
		)
	}
	if f.Summary != "" {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, f.Summary)
	}

	if len(f.Tasks) > 0 {
		_, _ = fmt.Fprintln(w)
		ui.Heading("Tasks")
		_, _ = fmt.Fprintln(w)
		for _, t := range f.Tasks {
			writeTaskSummary(w, t)
		}
		_, _ = fmt.Fprintf(w, "Run %s to draft a PR / ticket for a task.\n",
			ui.Code("infracost tasks preview-fix <task-id> --finding-id <finding-id>"),
		)
	}

	if len(f.Actions) > 0 {
		_, _ = fmt.Fprintln(w)
		ui.Heading("Actions")
		_, _ = fmt.Fprintln(w)
		for _, a := range f.Actions {
			_, _ = fmt.Fprintf(w, "  - %s %s", actionTypeLabel(a.Type), ui.Mutedf("(%s)", a.ID))
			if a.ActionStatus != "" {
				_, _ = fmt.Fprintf(w, "  %s %s", ui.Muted("Status:"), statusText(a.ActionStatus))
			}
			_, _ = fmt.Fprintln(w)
		}
	}

	_, _ = fmt.Fprintln(w)
	return nil
}

// writeTaskSummary renders one task inside `findings get`. Surfaces the
// id, status, effort, suggested action, savings, and the
// action_description so the user can decide whether to drill in with
// `tasks preview-fix`.
func writeTaskSummary(w io.Writer, t TaskOutput) {
	_, _ = fmt.Fprintf(w, "%s %s\n",
		ui.Bold(ui.Accent(t.Title)),
		ui.Mutedf("(%s)", t.ID),
	)
	if t.Status != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Status:"), statusText(t.Status))
	}
	if t.SuggestedAction != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Suggested:"), actionTypeLabel(t.SuggestedAction))
	}
	if t.Effort != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Effort:"), t.Effort)
	}
	if t.YearlySavings > 0 {
		_, _ = fmt.Fprintf(w, "  %s $%s/yr\n",
			ui.Muted("Saving:"),
			ui.Positive(formatFloat(t.YearlySavings)),
		)
	}
	if t.ActionDescription != "" {
		_, _ = fmt.Fprintln(w, "  "+t.ActionDescription)
	}
	_, _ = fmt.Fprintln(w)
}

// effortBadge wraps the effort string in a bracketed, color-coded tag.
// Trivial / small are positive, medium is caution, large is danger. Empty
// returns "" so the renderer can collapse the badge prefix.
func effortBadge(effort string) string {
	if effort == "" {
		return ""
	}
	upper := strings.ToUpper(effort)
	switch strings.ToLower(effort) {
	case "trivial", "small":
		return ui.Mutedf("[") + ui.Positive(upper) + ui.Muted("]")
	case "medium":
		return ui.Mutedf("[") + ui.Caution(upper) + ui.Muted("]")
	case "large":
		return ui.Mutedf("[") + ui.Danger(upper) + ui.Muted("]")
	default:
		return ui.Mutedf("[") + ui.Accent(upper) + ui.Muted("]")
	}
}

// statusText colorizes the status string. Open / in_progress are
// neutral-bold; resolved / auto_verified / auto_resolved are positive;
// dismissed / duplicate are muted.
func statusText(s string) string {
	switch strings.ToLower(s) {
	case "resolved", "auto_verified", "auto_resolved":
		return ui.Positive(s)
	case "dismissed", "duplicate":
		return ui.Muted(s)
	case "open", "in_progress", "awaiting_resolution":
		return ui.Bold(s)
	default:
		return s
	}
}

// actionTypeLabel maps the Agents action-type discriminator to a friendlier
// label for the human renderer.
func actionTypeLabel(t string) string {
	switch t {
	case "open_pr":
		return "Open PR"
	case "create_ticket":
		return "Create ticket"
	case "manual":
		return "Manual"
	default:
		return t
	}
}

// formatFloat prints a money-shaped float with two decimal places.
func formatFloat(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// renderJSON is the shared --json / --llm renderer for the findings &
// tasks results. Findings payloads are small and not tabular, so the LLM
// and JSON outputs are identical — TOON would have nothing to dedupe.
func renderJSON[T any](w io.Writer, v T) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}
