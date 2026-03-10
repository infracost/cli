package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/version"
	"github.com/spf13/cobra"
)

func Version(_ *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the current version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println(version.Version)
			return nil
		},
	}
}
