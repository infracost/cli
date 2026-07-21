package cmds

import (
	"context"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/update"
	"github.com/spf13/cobra"
)

func Update(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update to the latest version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := update.Update(cmd.Context()); err != nil {
				return err
			}

			// Also refresh plugins to their latest versions. Like the CLI
			// update itself, this is an explicit, on-demand action, so it
			// ignores the auto-update setting and always pulls the newest
			// plugins alongside the CLI. Per-plugin download lines nest above
			// this spinner, which resolves to "Plugins up to date.".
			return ui.RunWithSpinnerErr(cmd.Context(), "Updating plugins...", "Plugins up to date.", func(ctx context.Context) error {
				return cfg.Plugins.UpdatePlugins(ctx)
			})
		},
	}
}
