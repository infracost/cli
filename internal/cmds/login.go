package cmds

import (
	"fmt"

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
			if _, err := fetchAndCacheUser(cmd.Context(), cfg, client); err != nil {
				logging.WithError(err).Msg("failed to cache user info")
			}

			return nil
		},
	}
}
