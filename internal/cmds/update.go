package cmds

import (
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/update"
	"github.com/spf13/cobra"
)

func Update(_ *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update to the latest version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return update.Update(cmd.Context())
		},
	}
}
