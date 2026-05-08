package cmds

import (
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
)

func WhoAmI(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the current user",
		RunE: func(cmd *cobra.Command, _ []string) error {
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))
			user, err := client.CurrentUser(cmd.Context())
			if err != nil {
				return fmt.Errorf("fetching current user: %w", err)
			}

			cached := cacheUser(cfg, user)
			currentSlug, _, orgSrc := currentOrgSlug(cfg, cached.Organizations, cached.SelectedOrgID)

			fmt.Println()
			fmt.Printf("  %s  %s\n", ui.Muted("Name:"), user.Name)
			fmt.Printf("  %s %s\n", ui.Muted("Email:"), user.Email)

			fmt.Println()
			ui.Heading("Organizations")
			fmt.Println()
			for _, org := range user.Organizations {
				role := "member"
				for _, r := range org.Roles {
					if r.ID == "organization_owner" {
						role = "owner"
						break
					}
				}
				var marker, suffix string
				if strings.EqualFold(org.Slug, currentSlug) {
					marker = "  " + ui.Positive("✔") + "  "
					switch orgSrc {
					case orgSourceRepo:
						suffix = "  " + ui.Muted("← set for this repo")
					case orgSourceFlag:
						suffix = "  " + ui.Muted("← --org flag")
					case orgSourceGlobal:
						suffix = "  " + ui.Muted("← active")
					}
				} else {
					marker = "  -  "
				}
				fmt.Printf("%s%s %s%s\n", marker, ui.Accent(org.Slug), ui.Mutedf("(%s)", role), suffix)
			}

			if currentSlug == "" && len(user.Organizations) > 1 {
				fmt.Println()
				ui.Warn("No organization selected. Subsequent commands will fail with 'no organization selected'.")
				ui.Hint(2, "Pick one with one of:")
				ui.Hint(4, "pass --org <slug> on each command")
				ui.Hint(4, "set INFRACOST_CLI_ORG=<slug> in the environment")
				ui.Hint(4, "run 'infracost org switch <slug>' to save it globally")
				ui.Hint(4, "run 'infracost org switch <slug> --repo' to pin it to this repository")
			}

			return nil
		},
	}
}
