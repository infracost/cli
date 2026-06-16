package cmds

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// RetryActionInput is the parsed input for `actions retry` and the
// `retry_action` MCP tool. Agents' endpoint requires the action be in
// the `failed` state — the CLI doesn't pre-check (a stale local read
// would race the server anyway); a 409 / 400 from Agents is the canonical
// "not retryable" signal.
type RetryActionInput struct {
	ActionID string `json:"action_id" jsonschema:"Action ID to retry. The action must be in the failed state."`
}

// RetryActionResult is the OK-only typed shape. Agents doesn't echo the
// new action state; callers wanting that fetch the parent finding via
// findings_get.
type RetryActionResult struct {
	OK bool `json:"ok"`
}

// RetryAction asks Agents to reset and re-queue a failed action. The
// per-status validation lives server-side: this wrapper only enforces
// the locally-known invariants (ID + org).
func RetryAction(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in RetryActionInput) (RetryActionResult, error) {
	if in.ActionID == "" {
		return RetryActionResult{}, fmt.Errorf("action id is required")
	}
	if cfg.OrgID == "" {
		return RetryActionResult{}, fmt.Errorf("no organization selected")
	}
	if err := ensureAgentsEnabled(cfg); err != nil {
		return RetryActionResult{}, err
	}

	client := cfg.Agents.Client(api.Client(ctx, source, cfg.OrgID))
	events.RegisterMetadata("orgId", cfg.OrgID)

	resp, err := client.RetryAction(ctx, cfg.OrgID, in.ActionID)
	if err != nil {
		return RetryActionResult{}, fmt.Errorf("retrying action: %w", err)
	}
	return RetryActionResult{OK: resp.OK}, nil
}

// ActionsCmd builds the `actions` parent command. Currently only `retry`
// — dismissal lives in the Agents portal so users can confirm side
// effects (PR closure, ticket archival) before the cascade fires.
func ActionsCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "actions",
		Short:  "Manage actions (PRs, tickets, manual fixes) attached to findings",
		Hidden: true,
	}
	cmd.AddCommand(actionsRetryCmd(cfg))
	return cmd
}

func actionsRetryCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retry <action-id>",
		Short: "Re-queue a failed action for another attempt",
		Long: `Retry a failed action. The action must currently be in the failed state
(check action_status on the parent finding); Agents resets the action's job
state and republishes it to the action worker.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := RetryActionInput{ActionID: args[0]}
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result RetryActionResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Retrying action...", "Action requeued", func(ctx context.Context) error {
				var rErr error
				result, rErr = RetryAction(ctx, cfg, source, in)
				return rErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, retryActionRenderers())
		},
	}
	return cmd
}

func retryActionRenderers() Renderers[RetryActionResult] {
	return Renderers[RetryActionResult]{
		Human: renderRetryActionHuman,
		JSON:  renderJSON[RetryActionResult],
		LLM:   renderJSON[RetryActionResult],
	}
}

func renderRetryActionHuman(w io.Writer, _ RetryActionResult) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, ui.Muted("Action requeued for retry. Poll `infracost findings get <finding-id>` to watch its action_status."))
	_, _ = fmt.Fprintln(w)
	return nil
}
