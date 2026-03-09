package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/config"
	"github.com/spf13/cobra"
)

func Login(config *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login to Infracost",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(config.Auth.AuthenticationToken) > 0 {
				fmt.Println("Authentication token provided directly, skipping login.")
				return nil
			}

			if _, err := config.Auth.Token(cmd.Context()); err != nil {
				return err
			}
			fmt.Println("Retrieved valid access token.")
			return nil
		},
	}
}
