package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
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

			cacheUser(cfg, user)

			fmt.Printf("Name:  %s\n", user.Name)
			fmt.Printf("Email: %s\n", user.Email)
			fmt.Println()
			fmt.Println("Organizations:")
			for _, org := range user.Organizations {
				role := "member"
				for _, r := range org.Roles {
					if r.ID == "organization_owner" {
						role = "owner"
						break
					}
				}
				fmt.Printf("  - %s (%s)\n", org.Name, role)
			}

			return nil
		},
	}
}
