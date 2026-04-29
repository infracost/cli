package cmds

import (
	"context"
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/liamg/tml"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

func Budgets(cfg *config.Config) *cobra.Command {
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

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			var budgets []*event.Budget
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Fetching budgets...", "Budgets loaded", func(ctx context.Context) error {
				runParameters, err := client.RunParameters(ctx, "", "")
				if err != nil {
					return fmt.Errorf("fetching budgets: %w", err)
				}

				if cfg.Org == "" {
					cfg.OrgID = runParameters.OrganizationID
				}

				pj := protojson.UnmarshalOptions{DiscardUnknown: true}
				for _, raw := range runParameters.Budgets {
					b := new(event.Budget)
					if err := pj.Unmarshal(raw, b); err != nil {
						return fmt.Errorf("failed to unmarshal budget: %w", err)
					}
					budgets = append(budgets, b)
				}
				return nil
			}); err != nil {
				return err
			}

			fmt.Println()

			if len(budgets) == 0 {
				tml.Println("<dim>No budgets configured for this organization.</dim>")
				fmt.Println()
				return nil
			}

			tml.Printf("<bold><blue><underline>Cost Budgets</underline></blue></bold>\n\n")

			for _, b := range budgets {
				printBudget(b)
			}

			return nil
		},
	}
}

func printBudget(b *event.Budget) {
	tml.Printf("<bold>%s</bold>  <dim>(%s)</dim>\n", b.Name, b.Id)

	if b.CustomOverrunMessage != "" {
		fmt.Printf("  %s\n", b.CustomOverrunMessage)
	}

	tml.Printf("\n  <underline>Budget</underline>\n")
	tml.Printf("    - Amount: <yellow>$%s</yellow>\n", formatThreshold(b.Amount))

	if b.CurrentCost != nil {
		current := rat.FromProto(b.CurrentCost)
		amount := rat.FromProto(b.Amount)
		spend := fmt.Sprintf("$%s", formatThreshold(b.CurrentCost))
		if current != nil && amount != nil && current.GreaterThan(amount) {
			tml.Printf("    - Current spend: <red>%s</red> <dim>(over budget)</dim>\n", spend)
		} else {
			tml.Printf("    - Current spend: <green>%s</green>\n", spend)
		}
	}

	if b.StartedAt != nil || b.EndedAt != nil {
		tml.Printf("\n  <underline>Period</underline>\n")
		start := "—"
		if b.StartedAt != nil {
			start = b.StartedAt.AsTime().Format("2006-01-02")
		}
		end := "—"
		if b.EndedAt != nil {
			end = b.EndedAt.AsTime().Format("2006-01-02")
		}
		tml.Printf("    - %s → %s\n", start, end)
	}

	tml.Printf("\n  <underline>Applies to</underline>\n")
	if len(b.Tags) == 0 {
		tml.Printf("    - All resources\n")
	} else {
		parts := make([]string, 0, len(b.Tags))
		for _, t := range b.Tags {
			parts = append(parts, fmt.Sprintf("%s=%s", t.Key, t.Value))
		}
		tml.Printf("    - Resources tagged %s\n", strings.Join(parts, ", "))
	}

	tml.Printf("\n  <underline>Actions</underline>\n")
	if b.PrComment {
		tml.Printf("    PR comment\n")
	} else {
		tml.Printf("    Alert only\n")
	}

	fmt.Println()
}
