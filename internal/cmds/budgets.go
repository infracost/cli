package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/encoding/protojson"
)

// BudgetsInput is the parsed input for `budgets`. Empty for now —
// `budgets` lists every budget configured for the active organization —
// but kept as a struct so the function signature stays stable when (e.g.)
// filter / paging flags are added later.
type BudgetsInput struct{}

// BudgetsResult is the typed output of `budgets`. Used by both CLI
// `--json` / `--llm` rendering and the MCP `budgets` tool. The wire
// shape is a clean Go-defined projection of the underlying event.Budget
// proto so consumers don't have to learn proto-internal field names.
type BudgetsResult struct {
	Budgets []BudgetEntry `json:"budgets"`
}

// BudgetEntry is one cost budget from the org. CurrentCost and OverBudget
// reflect the org's most recent rollup; period bounds (StartedAt /
// EndedAt) are formatted as YYYY-MM-DD strings — matching what the human
// renderer prints — so MCP consumers can read them without re-parsing
// proto timestamps.
type BudgetEntry struct {
	ID                   string                   `json:"id"`
	Name                 string                   `json:"name"`
	CustomOverrunMessage string                   `json:"custom_overrun_message,omitempty"`
	Amount               *rat.Rat                 `json:"amount,omitempty"`
	CurrentCost          *rat.Rat                 `json:"current_cost,omitempty"`
	OverBudget           bool                     `json:"over_budget"`
	StartedAt            string                   `json:"started_at,omitempty"`
	EndedAt              string                   `json:"ended_at,omitempty"`
	Tags                 []format.BudgetTagOutput `json:"tags,omitempty"`
	PrComment            bool                     `json:"pr_comment"`
}

// Budgets fetches every budget configured for the active organization and
// returns the typed result. Authentication and org resolution are the
// caller's responsibility — `source` and `cfg.OrgID` must be populated
// before calling Budgets.
//
// The dashboard's RunParameters endpoint is called with empty repo + branch
// because budgets are an organization-level resource, not per-repo.
func Budgets(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, _ BudgetsInput) (BudgetsResult, error) {
	client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))
	runParameters, err := client.RunParameters(ctx, "", "")
	if err != nil {
		return BudgetsResult{}, fmt.Errorf("fetching budgets: %w", err)
	}
	if cfg.Org == "" {
		cfg.OrgID = runParameters.OrganizationID
	}
	events.RegisterMetadata("orgId", cfg.OrgID)

	pj := protojson.UnmarshalOptions{DiscardUnknown: true}
	result := BudgetsResult{Budgets: []BudgetEntry{}}
	for _, raw := range runParameters.Budgets {
		b := new(event.Budget)
		if err := pj.Unmarshal(raw, b); err != nil {
			return BudgetsResult{}, fmt.Errorf("failed to unmarshal budget: %w", err)
		}
		result.Budgets = append(result.Budgets, toBudgetEntry(b))
	}
	return result, nil
}

// BudgetsCmd builds the cobra command. Authenticates, resolves the active
// org, then calls the pure Budgets function under a spinner and dispatches
// the result.
func BudgetsCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "budgets",
		Short: "List cost budgets for the current organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result BudgetsResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Fetching budgets...", "Budgets loaded", func(ctx context.Context) error {
				var bErr error
				result, bErr = Budgets(ctx, cfg, source, BudgetsInput{})
				return bErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, budgetsRenderers())
		},
	}
}

func budgetsRenderers() Renderers[BudgetsResult] {
	return Renderers[BudgetsResult]{
		Human: renderBudgetsHuman,
		JSON:  renderBudgetsJSON,
		LLM:   renderBudgetsLLM,
	}
}

func renderBudgetsHuman(w io.Writer, r BudgetsResult) error {
	_, _ = fmt.Fprintln(w)

	if len(r.Budgets) == 0 {
		_, _ = fmt.Fprintln(w, ui.Muted("No budgets configured for this organization."))
		_, _ = fmt.Fprintln(w)
		return nil
	}

	ui.Heading("Cost Budgets")
	_, _ = fmt.Fprintln(w)
	for _, b := range r.Budgets {
		writeBudgetEntry(w, b)
	}
	return nil
}

// writeBudgetEntry renders one budget. Matches the previous printBudget's
// layout — bold name + id, custom overrun message, period, applies-to,
// and the configured actions — just sourced from the clean BudgetEntry
// shape instead of the raw event.Budget proto.
func writeBudgetEntry(w io.Writer, b BudgetEntry) {
	_, _ = fmt.Fprintf(w, "%s  %s\n", ui.Bold(ui.Accent(b.Name)), ui.Mutedf("(%s)", b.ID))

	if b.CustomOverrunMessage != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", b.CustomOverrunMessage)
	}

	_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Budget")))
	_, _ = fmt.Fprintf(w, "    - Amount: %s\n", ui.Cautionf("$%s", formatRat(b.Amount)))

	if b.CurrentCost != nil {
		spend := fmt.Sprintf("$%s", formatRat(b.CurrentCost))
		if b.OverBudget {
			_, _ = fmt.Fprintf(w, "    - Current spend: %s %s\n", ui.Danger(spend), ui.Muted("(over budget)"))
		} else {
			_, _ = fmt.Fprintf(w, "    - Current spend: %s\n", ui.Positive(spend))
		}
	}

	if b.StartedAt != "" || b.EndedAt != "" {
		start := b.StartedAt
		if start == "" {
			start = "—"
		}
		end := b.EndedAt
		if end == "" {
			end = "—"
		}
		_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Period")))
		_, _ = fmt.Fprintf(w, "    - %s → %s\n", start, end)
	}

	_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Applies to")))
	if len(b.Tags) == 0 {
		_, _ = fmt.Fprintln(w, "    - All resources")
	} else {
		parts := make([]string, 0, len(b.Tags))
		for _, t := range b.Tags {
			parts = append(parts, fmt.Sprintf("%s=%s", t.Key, t.Value))
		}
		_, _ = fmt.Fprintf(w, "    - Resources tagged %s\n", strings.Join(parts, ", "))
	}

	_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Actions")))
	if b.PrComment {
		_, _ = fmt.Fprintln(w, "    PR comment")
	} else {
		_, _ = fmt.Fprintln(w, "    Alert only")
	}

	_, _ = fmt.Fprintln(w)
}

func renderBudgetsJSON(w io.Writer, r BudgetsResult) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}

func renderBudgetsLLM(w io.Writer, r BudgetsResult) error {
	// Same shape as JSON — the budget list is small, no dedupable
	// tabular payload that TOON would reformat usefully.
	return renderBudgetsJSON(w, r)
}

// toBudgetEntry projects a raw event.Budget proto to the clean MCP/JSON
// shape. Timestamps are formatted as YYYY-MM-DD to match the human
// renderer; the OverBudget flag is computed once here so consumers don't
// duplicate the comparison.
func toBudgetEntry(b *event.Budget) BudgetEntry {
	entry := BudgetEntry{
		ID:                   b.Id,
		Name:                 b.Name,
		CustomOverrunMessage: b.CustomOverrunMessage,
		Amount:               rat.FromProto(b.Amount),
		CurrentCost:          rat.FromProto(b.CurrentCost),
		PrComment:            b.PrComment,
	}
	if b.StartedAt != nil {
		entry.StartedAt = b.StartedAt.AsTime().Format("2006-01-02")
	}
	if b.EndedAt != nil {
		entry.EndedAt = b.EndedAt.AsTime().Format("2006-01-02")
	}
	if entry.Amount != nil && entry.CurrentCost != nil {
		entry.OverBudget = entry.CurrentCost.GreaterThan(entry.Amount)
	}
	if len(b.Tags) > 0 {
		entry.Tags = make([]format.BudgetTagOutput, 0, len(b.Tags))
		for _, t := range b.Tags {
			entry.Tags = append(entry.Tags, format.BudgetTagOutput{Key: t.Key, Value: t.Value})
		}
	}
	return entry
}

// formatRat formats a *rat.Rat with two decimal places for human display,
// or "0" when nil. The proto-flavored formatThreshold guardrails uses
// goes through rat.FromProto first; this one starts from an already-converted
// value.
func formatRat(r *rat.Rat) string {
	if r == nil {
		return "0"
	}
	return r.StringFixed(2)
}