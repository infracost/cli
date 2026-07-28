package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// PreviewFixInput is the parsed input for `tasks preview-fix`. The Agents
// API takes an array of task ids so one PR / ticket can cover multiple
// related tasks; the CLI wraps a single id in a one-element slice for
// the common case.
type PreviewFixInput struct {
	FindingID string `json:"finding_id" jsonschema:"Finding ID the task belongs to. Required."`
	TaskID    string `json:"task_id" jsonschema:"Task ID to draft a fix for. Required."`
	Type      string `json:"type,omitempty" jsonschema:"Action type to draft (open_pr or create_ticket). Omit to use the task's own suggested_action, which is what you normally want — Agents already decided which action is safe for the task. The 'manual' type is not supported by generate-action — the LLM can't draft a manual action."`
}

// PreviewFixResult wraps the LLM's drafted action so the JSON / LLM
// renderer can emit it directly to stdout for piping into create-fix.
type PreviewFixResult struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// CreateFixInput is the parsed input for `tasks create-fix` and the
// `create_fix` MCP tool. ConfigJSON is the raw bytes the user piped in
// from `preview-fix` (possibly after editing); the CLI / MCP handler
// forwards it verbatim to the Agents API.
type CreateFixInput struct {
	FindingID  string          `json:"finding_id" jsonschema:"Finding ID the task belongs to. Required."`
	TaskID     string          `json:"task_id" jsonschema:"Task ID to create the action for. Required."`
	Type       string          `json:"type,omitempty" jsonschema:"Action type to create (open_pr, create_ticket, manual). Pass the type preview_fix returned, unless the user explicitly switched it."`
	ConfigJSON json.RawMessage `json:"config" jsonschema:"The opaque action config blob returned by preview_fix, possibly edited by the user. Required."`
}

// CreateFixResult is the typed output of `tasks create-fix`. Agents'
// create endpoint only returns the new action's id, so the result is
// just that — the caller can fetch the action separately if more
// detail is needed.
type CreateFixResult struct {
	ActionID string `json:"action_id"`
}

// PreviewFix asks Agents to draft an action (PR or ticket) for the task,
// without creating it. The returned config blob is what `tasks create-fix`
// would submit if called immediately — letting users review (and edit)
// the drafted action before it goes live is the whole reason this is two
// commands instead of one.
func PreviewFix(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in PreviewFixInput) (PreviewFixResult, error) {
	if in.FindingID == "" {
		return PreviewFixResult{}, fmt.Errorf("finding id is required")
	}
	if in.TaskID == "" {
		return PreviewFixResult{}, fmt.Errorf("task id is required")
	}
	if cfg.OrgID == "" {
		return PreviewFixResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return PreviewFixResult{}, err
	}

	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	actionType, err := resolvePreviewActionType(ctx, client, cfg.OrgID, in)
	if err != nil {
		return PreviewFixResult{}, err
	}

	resp, err := client.GenerateAction(ctx, cfg.OrgID, in.FindingID, agents.GenerateActionRequest{
		TaskIDs:    []string{in.TaskID},
		ActionType: actionType,
	})
	if err != nil {
		return PreviewFixResult{}, fmt.Errorf("generating action: %w", err)
	}
	return PreviewFixResult{
		Type:   string(resp.Type),
		Config: resp.Config,
	}, nil
}

// CreateFix submits the (possibly-edited) draft config to Agents and
// creates the PR / ticket / manual action. Callers are expected to pass
// the type the draft was generated as — the CLI reads it back off the
// piped envelope, and the MCP tool asks the agent for it — with open_pr
// as the last-resort default for a bare config that names none.
func CreateFix(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in CreateFixInput) (CreateFixResult, error) {
	if in.FindingID == "" {
		return CreateFixResult{}, fmt.Errorf("finding id is required")
	}
	if in.TaskID == "" {
		return CreateFixResult{}, fmt.Errorf("task id is required")
	}
	if len(in.ConfigJSON) == 0 {
		return CreateFixResult{}, fmt.Errorf("config is required (pipe `tasks preview-fix` output or pass --config <file>)")
	}
	if cfg.OrgID == "" {
		return CreateFixResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return CreateFixResult{}, err
	}
	actionType, err := resolveAnyActionType(in.Type)
	if err != nil {
		return CreateFixResult{}, err
	}

	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	resp, err := client.CreateAction(ctx, cfg.OrgID, in.FindingID, agents.CreateActionRequest{
		Type:    actionType,
		Config:  in.ConfigJSON,
		TaskIDs: []string{in.TaskID},
	})
	if err != nil {
		return CreateFixResult{}, fmt.Errorf("creating action: %w", err)
	}
	return CreateFixResult{ActionID: resp.ActionID}, nil
}

// UpdateTaskStatusInput is the parsed input for the unified
// `tasks update` CLI command and the `update_task_status` MCP tool.
// Status takes one of three user-facing verbs the CLI routes to the
// right Agents endpoint internally:
//
//   - "confirm" — you did Agents' suggested change. Routes to
//     POST /report type=confirmation; advances linked open manual
//     actions to `done`.
//   - "correct" — you solved the problem differently than Agents
//     suggested. Routes to POST /report type=correction with reason as
//     content; dismisses linked draft/open actions and emits an
//     AgentLearning(source=correction).
//   - "dismiss" — the user declined this task without doing anything.
//     Routes to PATCH /tasks/:id status=dismissed with reason as
//     dismissed_reason; emits an AgentLearning(source=task_dismissed)
//     when a reason is supplied. This is the "we don't need to do
//     this" verb; use it instead of "correct" when nothing was
//     actually done.
//
// Reason is required for "correct" (the correction is meaningless
// without explaining what was done instead) and strongly recommended
// for "dismiss" (the AgentLearning is only emitted when reason is
// non-empty). Ignored for "confirm".
type UpdateTaskStatusInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ID to update. Required."`
	Status string `json:"status" jsonschema:"User-facing verb — one of: confirm (you did Agents' suggested change), correct (you solved the problem a different way; reason required), dismiss (you decided not to do anything; reason recommended). Required."`
	Reason string `json:"reason,omitempty" jsonschema:"Reason text. Required for correct (what you did instead). Recommended for dismiss (why you're declining — also feeds the agent's learning so it doesn't re-raise). Ignored for confirm."`
}

// UpdateTaskStatusResult is the unified return shape. The cascade slots
// (LearningID, DismissedActionIDs, ConfirmedActionIDs) only populate
// for the verbs whose underlying Agents endpoint returns them — see the
// per-verb behavior on UpdateTaskStatus.
type UpdateTaskStatusResult struct {
	Task               agents.Task `json:"task"`
	LearningID         string      `json:"learning_id,omitempty"`
	DismissedActionIDs []string    `json:"dismissed_action_ids,omitempty"`
	ConfirmedActionIDs []string    `json:"confirmed_action_ids,omitempty"`
}

// taskUpdateVerb is the internal discriminator. Kept distinct from
// agents.ReportType / agents.TaskStatus because the CLI's verb set ("confirm"
// / "correct" / "dismiss") deliberately differs from Agents' enums.
type taskUpdateVerb string

const (
	taskUpdateVerbConfirm taskUpdateVerb = "confirm"
	taskUpdateVerbCorrect taskUpdateVerb = "correct"
	taskUpdateVerbDismiss taskUpdateVerb = "dismiss"
)

// UpdateTaskStatus is the unified task-mutation entry point. It validates
// the verb + reason combination, then routes to whichever Agents endpoint
// matches the user's intent. The two Agents endpoints have different wire
// shapes; the function normalizes them into UpdateTaskStatusResult so
// the CLI / MCP layers only need one renderer.
//
// Confirm and correct go through POST /report (so the AgentLearning
// source is `correction` for correct). Dismiss goes through
// PATCH /tasks/:id (so the AgentLearning source is `task_dismissed`).
// Picking the right endpoint per verb is what makes the agent's learning
// signal meaningful — using `correction` for a no-op decline would mis-
// train the agent.
func UpdateTaskStatus(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in UpdateTaskStatusInput) (UpdateTaskStatusResult, error) {
	if in.TaskID == "" {
		return UpdateTaskStatusResult{}, fmt.Errorf("task id is required")
	}
	verb, err := resolveTaskUpdateVerb(in.Status)
	if err != nil {
		return UpdateTaskStatusResult{}, err
	}
	reason := strings.TrimSpace(in.Reason)
	if verb == taskUpdateVerbCorrect && reason == "" {
		return UpdateTaskStatusResult{}, fmt.Errorf("--reason is required for status=correct (what was done instead — recorded as dismissed_reason and emitted as the AgentLearning content)")
	}
	if cfg.OrgID == "" {
		return UpdateTaskStatusResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return UpdateTaskStatusResult{}, err
	}

	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	switch verb {
	case taskUpdateVerbConfirm:
		resp, err := client.ReportTask(ctx, cfg.OrgID, in.TaskID, agents.ReportTaskRequest{
			Type: agents.ReportTypeConfirmation,
		})
		if err != nil {
			return UpdateTaskStatusResult{}, fmt.Errorf("confirming task: %w", err)
		}
		return UpdateTaskStatusResult{
			Task:               resp.Task,
			ConfirmedActionIDs: resp.ConfirmedActionIDs,
		}, nil

	case taskUpdateVerbCorrect:
		resp, err := client.ReportTask(ctx, cfg.OrgID, in.TaskID, agents.ReportTaskRequest{
			Type:    agents.ReportTypeCorrection,
			Content: reason,
		})
		if err != nil {
			return UpdateTaskStatusResult{}, fmt.Errorf("correcting task: %w", err)
		}
		return UpdateTaskStatusResult{
			Task:               resp.Task,
			LearningID:         resp.LearningID,
			DismissedActionIDs: resp.DismissedActionIDs,
		}, nil

	case taskUpdateVerbDismiss:
		task, err := client.UpdateTaskStatus(ctx, cfg.OrgID, in.TaskID, agents.UpdateTaskStatusRequest{
			Status:          agents.TaskStatusDismissed,
			DismissedReason: reason,
		})
		if err != nil {
			return UpdateTaskStatusResult{}, fmt.Errorf("dismissing task: %w", err)
		}
		// The PATCH endpoint doesn't echo the cascade lists back on the
		// wire (the store dismisses linked actions but the route returns
		// just the task), so DismissedActionIDs is intentionally empty
		// here even though server-side actions may have moved.
		return UpdateTaskStatusResult{Task: task}, nil
	}
	return UpdateTaskStatusResult{}, fmt.Errorf("unreachable: unhandled task update verb %q", verb)
}

// resolveTaskUpdateVerb maps the user-facing CLI / MCP string onto the
// internal verb enum. The error message lists the supported verbs in
// the same order the help text does so the diagnostic matches what the
// user just read.
func resolveTaskUpdateVerb(s string) (taskUpdateVerb, error) {
	switch s {
	case "confirm":
		return taskUpdateVerbConfirm, nil
	case "correct":
		return taskUpdateVerbCorrect, nil
	case "dismiss":
		return taskUpdateVerbDismiss, nil
	default:
		return "", fmt.Errorf("invalid --status %q: must be one of confirm, correct, dismiss", s)
	}
}

// TasksCmd builds the `tasks` parent command and its subcommands. Only
// the mutating commands (preview-fix, create-fix, update) live here —
// `tasks get` would be redundant because `findings get` already returns
// the full task payload inline.
func TasksCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "tasks",
		Short:  "Draft and submit fixes for FinOps task work",
		Hidden: true,
	}
	cmd.AddCommand(tasksPreviewFixCmd(cfg))
	cmd.AddCommand(tasksCreateFixCmd(cfg))
	cmd.AddCommand(tasksUpdateCmd(cfg))
	return cmd
}

func tasksUpdateCmd(cfg *config.Config) *cobra.Command {
	var in UpdateTaskStatusInput
	cmd := &cobra.Command{
		Use:   "update <task-id>",
		Short: "Confirm, correct, or dismiss a task",
		Long: `Update a task's resolution state. The verbs correspond to three different
user intents — and route to two different Agents endpoints so the agent
learns the right signal:

  confirm   You did Agents' suggested change. Advances the linked manual
            action to done so the observation cascade can verify.
  correct   You solved the problem a different way. --reason describes
            what you did; the task is dismissed, linked draft/open
            actions are dismissed, and Agents learns the alternative.
  dismiss   You decided not to do this task. --reason is recommended —
            it's what Agents learns from so the same task isn't raised
            again next scan.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.TaskID = args[0]
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result UpdateTaskStatusResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Updating task...", "Task updated", func(ctx context.Context) error {
				var rErr error
				result, rErr = UpdateTaskStatus(ctx, cfg, source, in)
				return rErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, updateTaskStatusRenderers())
		},
	}
	cmd.Flags().StringVar(&in.Status, "status", "", "One of: confirm, correct, dismiss (required)")
	cmd.Flags().StringVar(&in.Reason, "reason", "", "Reason — required for correct, recommended for dismiss, ignored for confirm")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

func updateTaskStatusRenderers() Renderers[UpdateTaskStatusResult] {
	return Renderers[UpdateTaskStatusResult]{
		Human: renderUpdateTaskStatusHuman,
		JSON:  renderJSON[UpdateTaskStatusResult],
		LLM:   renderJSON[UpdateTaskStatusResult],
	}
}

func renderUpdateTaskStatusHuman(w io.Writer, r UpdateTaskStatusResult) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "%s %s\n",
		ui.Bold(ui.Accent(r.Task.Title)),
		ui.Mutedf("(%s)", r.Task.ID),
	)
	_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Status:"), statusText(r.Task.Status))
	if r.Task.DismissedReason != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Reason:"), r.Task.DismissedReason)
	}
	if len(r.ConfirmedActionIDs) > 0 {
		_, _ = fmt.Fprintf(w, "  %s %s\n",
			ui.Muted("Confirmed actions:"),
			strings.Join(r.ConfirmedActionIDs, ", "),
		)
	}
	if len(r.DismissedActionIDs) > 0 {
		_, _ = fmt.Fprintf(w, "  %s %s\n",
			ui.Muted("Dismissed actions:"),
			strings.Join(r.DismissedActionIDs, ", "),
		)
	}
	if r.LearningID != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Learning emitted:"), r.LearningID)
	}
	_, _ = fmt.Fprintln(w)
	return nil
}

func tasksPreviewFixCmd(cfg *config.Config) *cobra.Command {
	var in PreviewFixInput
	cmd := &cobra.Command{
		Use:   "preview-fix <task-id>",
		Short: "Draft a PR or ticket for a task without creating it",
		Long: `Ask Agents to draft a PR or ticket for a task and print the result to stdout.

Without --type the draft matches the task's own suggested action: Agents only
suggests a PR where the code location and the change are both trustworthy, and
suggests a ticket otherwise (e.g. a tagging fix on resources that aren't managed
by Terraform). Passing --type open_pr for such a task will fail.

No side effects — the draft can be reviewed (and edited) before submission.
Pipe the output into ` + "`tasks create-fix --config -`" + ` to actually create it:

  infracost tasks preview-fix <task-id> --finding-id <finding-id> > fix.json
  # review / edit fix.json
  infracost tasks create-fix <task-id> --finding-id <finding-id> --config fix.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.TaskID = args[0]
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result PreviewFixResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Drafting fix...", "Draft ready", func(ctx context.Context) error {
				var pErr error
				result, pErr = PreviewFix(ctx, cfg, source, in)
				return pErr
			}); err != nil {
				return err
			}
			// preview-fix always emits JSON to stdout regardless of the
			// --json / --llm flags — the whole point of the command is to
			// produce a config blob the next command can consume.
			return renderJSON(os.Stdout, result)
		},
	}
	cmd.Flags().StringVar(&in.FindingID, "finding-id", "", "Finding ID the task belongs to (required)")
	cmd.Flags().StringVar(&in.Type, "type", "", "Action type to draft (open_pr or create_ticket); defaults to the task's own suggested action")
	_ = cmd.MarkFlagRequired("finding-id")
	return cmd
}

func tasksCreateFixCmd(cfg *config.Config) *cobra.Command {
	var (
		in         CreateFixInput
		configPath string
	)
	cmd := &cobra.Command{
		Use:   "create-fix <task-id>",
		Short: "Submit a previously-drafted fix to Agents",
		Long: `Submit a drafted fix to Agents, creating the PR / ticket / manual action.

Reads the config blob from --config <file>, or from stdin when --config is "-"
or omitted. The blob is the JSON ` + "`tasks preview-fix`" + ` printed; this
command does not regenerate it — that keeps the destructive call honest about
exactly what's being submitted.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.TaskID = args[0]

			raw, draftedType, err := readConfigBlob(configPath, cmd.InOrStdin())
			if err != nil {
				return err
			}
			in.ConfigJSON = raw
			// An explicit --type wins; otherwise take the type the draft was
			// generated as, so a ticket draft isn't submitted as a PR.
			if in.Type == "" {
				in.Type = draftedType
			}

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result CreateFixResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Creating fix...", "Fix created", func(ctx context.Context) error {
				var cErr error
				result, cErr = CreateFix(ctx, cfg, source, in)
				return cErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, tasksCreateFixRenderers())
		},
	}
	cmd.Flags().StringVar(&in.FindingID, "finding-id", "", "Finding ID the task belongs to (required)")
	cmd.Flags().StringVar(&in.Type, "type", "", "Action type being created (open_pr, create_ticket, manual); defaults to the drafted config's own type")
	cmd.Flags().StringVar(&configPath, "config", "-", `Path to the drafted config JSON ("-" for stdin)`)
	_ = cmd.MarkFlagRequired("finding-id")
	return cmd
}

// readConfigBlob reads the drafted action JSON from the requested
// source. "-" / "" means stdin; otherwise the path is read whole and
// returned verbatim. The bytes are JSON-validated so a malformed file
// fails fast with a clear error rather than a server-side 400.
// The envelope's own `type` is returned alongside the config so the caller
// can honor the drafted type when the user didn't name one — piping a
// create_ticket draft into create-fix must not silently create an open_pr.
func readConfigBlob(path string, stdin io.Reader) (json.RawMessage, string, error) {
	var raw []byte
	var err error
	switch path {
	case "", "-":
		raw, err = io.ReadAll(stdin)
		if err != nil {
			return nil, "", fmt.Errorf("reading config from stdin: %w", err)
		}
	default:
		raw, err = os.ReadFile(path) // #nosec G304 -- user-supplied config path
		if err != nil {
			return nil, "", fmt.Errorf("reading config from %s: %w", path, err)
		}
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("config is empty — pipe `tasks preview-fix` output or pass --config <file>")
	}
	if !json.Valid(raw) {
		return nil, "", fmt.Errorf("config is not valid JSON")
	}
	// Unwrap a preview-fix-shaped envelope: that command writes
	// {"type": ..., "config": {...}} so the user can pipe directly. The
	// API only wants the inner config blob.
	var envelope struct {
		Type   string          `json:"type"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Config) > 0 {
		return envelope.Config, envelope.Type, nil
	}
	return raw, "", nil
}

// resolvePreviewActionType decides which action type preview-fix should
// ask Agents to draft. An explicit --type always wins; otherwise we take
// the task's own SuggestedAction, because Agents has already decided what
// is safe for that task — a tagging fix on resources with no trustworthy
// IaC location is deliberately downgraded to create_ticket server-side,
// and asking for open_pr anyway just fails (the config LLM has no repo to
// name). Only when the task carries no suggestion do we fall back to
// open_pr, preserving the historical default.
func resolvePreviewActionType(ctx context.Context, client agents.Client, orgID string, in PreviewFixInput) (agents.ActionType, error) {
	if in.Type != "" {
		return resolveAutomatableActionType(in.Type)
	}

	// A lookup failure is not fatal: the caller asked to draft a fix, not to
	// read the finding, so fall back to the historical default rather than
	// failing the whole command on a transient read error.
	finding, err := client.GetFinding(ctx, orgID, in.FindingID)
	if err != nil {
		return agents.ActionTypeOpenPR, nil
	}
	for _, t := range finding.Tasks {
		if t.ID != in.TaskID {
			continue
		}
		switch agents.ActionType(t.SuggestedAction) {
		case agents.ActionTypeOpenPR, agents.ActionTypeCreateTicket:
			return agents.ActionType(t.SuggestedAction), nil
		case agents.ActionTypeManual:
			return "", fmt.Errorf("this task's suggested action is manual, which the LLM can't draft — apply it yourself and record it with `infracost tasks update %s --status confirm`, or pass --type create_ticket to raise a ticket instead", in.TaskID)
		}
		break
	}
	return agents.ActionTypeOpenPR, nil
}

// resolveAutomatableActionType validates an explicit --type string for the
// generate-action endpoint, which only accepts the automatable types
// (open_pr, create_ticket). Manual actions can't be drafted by the LLM.
func resolveAutomatableActionType(s string) (agents.ActionType, error) {
	if s == "" {
		return agents.ActionTypeOpenPR, nil
	}
	switch agents.ActionType(s) {
	case agents.ActionTypeOpenPR, agents.ActionTypeCreateTicket:
		return agents.ActionType(s), nil
	case agents.ActionTypeManual:
		return "", fmt.Errorf("preview-fix does not support type=manual (the LLM can't draft a manual action). Use `tasks create-fix --type manual` directly")
	}
	return "", fmt.Errorf("invalid --type %q (must be one of: open_pr, create_ticket)", s)
}

// resolveAnyActionType validates the --type string for the create-action
// endpoint, which accepts the full set including manual.
func resolveAnyActionType(s string) (agents.ActionType, error) {
	if s == "" {
		return agents.ActionTypeOpenPR, nil
	}
	switch agents.ActionType(s) {
	case agents.ActionTypeOpenPR, agents.ActionTypeCreateTicket, agents.ActionTypeManual:
		return agents.ActionType(s), nil
	}
	return "", fmt.Errorf("invalid --type %q (must be one of: open_pr, create_ticket, manual)", s)
}

func tasksCreateFixRenderers() Renderers[CreateFixResult] {
	return Renderers[CreateFixResult]{
		Human: renderCreateFixHuman,
		JSON:  renderJSON[CreateFixResult],
		LLM:   renderJSON[CreateFixResult],
	}
}

func renderCreateFixHuman(w io.Writer, r CreateFixResult) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "%s Action created %s\n",
		ui.Positive("✔"),
		ui.Mutedf("(%s)", r.ActionID),
	)
	_, _ = fmt.Fprintln(w)
	return nil
}
