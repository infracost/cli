package cmds

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Login(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login to Infracost",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(cfg.Auth.AuthenticationToken) > 0 {
				fmt.Println("Authentication token provided directly, skipping login.")
				return nil
			}

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Println("Retrieved valid access token.")

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, ""))
			uc, err := fetchAndCacheUser(cmd.Context(), cfg, client)
			if err != nil {
				logging.WithError(err).Msg("failed to cache user info")
				return nil
			}

			if len(uc.Organizations) == 0 || uc.SelectedOrgID != "" {
				return nil
			}

			if len(uc.Organizations) == 1 {
				uc.SelectedOrgID = uc.Organizations[0].ID
				if saveErr := cfg.Auth.SaveUserCache(uc); saveErr != nil {
					logging.WithError(saveErr).Msg("failed to save org selection")
				}
				fmt.Printf("Organization set to %s.\n", uc.Organizations[0].Slug)
				return nil
			}

			title := fmt.Sprintf("You have %d organizations, which would you like to be your default?", len(uc.Organizations))
			slug, pickErr := pickOrg(uc.Organizations, cfg, "", title)
			if pickErr != nil {
				if !errors.Is(pickErr, huh.ErrUserAborted) {
					return pickErr
				}
				fmt.Println("No organization selected. Run 'infracost org switch' to set one.")
				return nil
			}

			for _, org := range uc.Organizations {
				if org.Slug == slug {
					uc.SelectedOrgID = org.ID
					break
				}
			}

			if saveErr := cfg.Auth.SaveUserCache(uc); saveErr != nil {
				logging.WithError(saveErr).Msg("failed to save org selection")
			}
			fmt.Printf("Organization set to %s.\n", slug)

			return nil
		},
	}
}
