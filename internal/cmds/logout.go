package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/config"
	"github.com/spf13/cobra"
)

func Logout(config *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Logout of Infracost",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := config.Auth.ClearCache(); err != nil {
				return err
			}
			fmt.Println("Logged out")
			return nil
		},
	}
}
