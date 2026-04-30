package cmds

import (
	"github.com/infracost/cli/internal/config"
	"github.com/spf13/cobra"
)

func Auth(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
	}
	cmd.AddCommand(Login(cfg))
	cmd.AddCommand(Logout(cfg))
	cmd.AddCommand(WhoAmI(cfg))
	return cmd
}
