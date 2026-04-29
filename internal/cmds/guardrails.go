package cmds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	rational "github.com/infracost/proto/gen/go/infracost/rational"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

func Guardrails(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "guardrails [path]",
		Short: "List cost guardrails for the current repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}

			target := "."
			if len(args) > 0 {
				target = args[0]
			}

			absoluteDirectory, err := filepath.Abs(filepath.Clean(target))
			if err != nil {
				return fmt.Errorf("failed to get absolute path to target: %w", err)
			}

			if info, err := os.Stat(absoluteDirectory); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("target directory does not exist")
				}
				return fmt.Errorf("failed to get info for target directory: %w", err)
			} else if !info.IsDir() {
				return fmt.Errorf("target is not a directory")
			}

			repositoryURL := vcs.GetRemoteURL(absoluteDirectory)
			branchName := vcs.GetCurrentBranch(absoluteDirectory)

			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))

			var guardrails []*event.Guardrail
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Fetching guardrails...", "Guardrails loaded", func(ctx context.Context) error {
				runParameters, err := client.RunParameters(ctx, repositoryURL, branchName)
				if err != nil {
					return fmt.Errorf("fetching guardrails: %w", err)
				}

				if cfg.Org == "" {
					cfg.OrgID = runParameters.OrganizationID
				}

				pj := protojson.UnmarshalOptions{DiscardUnknown: true}
				for _, raw := range runParameters.Guardrails {
					g := new(event.Guardrail)
					if err := pj.Unmarshal(raw, g); err != nil {
						return fmt.Errorf("failed to unmarshal guardrail: %w", err)
					}
					guardrails = append(guardrails, g)
				}
				return nil
			}); err != nil {
				return err
			}

			fmt.Println()

			if len(guardrails) == 0 {
				fmt.Println(ui.Muted("No guardrails configured for this repository."))
				fmt.Println()
				return nil
			}

			ui.Heading("Cost Guardrails")
			fmt.Println()

			for _, g := range guardrails {
				printGuardrail(g)
			}

			return nil
		},
	}
}

func printGuardrail(g *event.Guardrail) {
	scope := "repo"
	if g.Scope == event.Guardrail_PROJECT {
		scope = "project"
	}

	fmt.Printf("%s  %s\n", ui.Bold(ui.Accent(g.Name)), ui.Mutedf("(%s, %s-level)", g.Id, scope))

	if g.Message != "" {
		fmt.Printf("  %s\n", g.Message)
	}

	fmt.Printf("\n  %s\n", ui.Bold(ui.Muted("Thresholds")))
	if g.TotalThreshold != nil {
		fmt.Printf("    - Total monthly cost exceeds %s\n", ui.Cautionf("$%s", formatThreshold(g.TotalThreshold)))
	}
	if g.IncreaseThreshold != nil {
		fmt.Printf("    - Cost increase exceeds %s\n", ui.Cautionf("$%s", formatThreshold(g.IncreaseThreshold)))
	}
	if g.IncreasePercentThreshold != nil {
		fmt.Printf("    - Cost increase exceeds %s\n", ui.Cautionf("%s%%", formatThreshold(g.IncreasePercentThreshold)))
	}

	fmt.Printf("\n  %s\n", ui.Bold(ui.Muted("Actions")))
	var actions []string
	if g.PrComment {
		actions = append(actions, "PR comment")
	}
	if g.BlockPr {
		actions = append(actions, "Block PR")
	}
	if len(actions) == 0 {
		actions = append(actions, "Alert only")
	}
	fmt.Printf("    %s\n", strings.Join(actions, ", "))

	if g.Scope == event.Guardrail_PROJECT && g.ProjectFilter != nil {
		fmt.Printf("\n  %s\n", ui.Bold(ui.Muted("Applies to")))
		fmt.Printf("    - %s\n", filterToHumanReadableString("projects", g.ProjectFilter))
	}

	fmt.Println()
}

func formatThreshold(r *rational.Rat) string {
	v := rat.FromProto(r)
	if v == nil {
		return "0"
	}
	return v.StringFixed(2)
}
