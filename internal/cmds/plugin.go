package cmds

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/spf13/cobra"
)

func PluginsCmd(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage Infracost plugins",
	}
	cmd.AddCommand(pluginsListCmd(cfg))
	cmd.AddCommand(pluginsUpdateCmd(cfg))
	return cmd
}

func pluginListCell(value string, width int) string {
	return fmt.Sprintf("%-*s", width, value)
}

func printPluginListGroup(title, pluginType string, items []plugins.ListItem) {
	fmt.Println(ui.Bold(title + ":"))
	for _, plugin := range items {
		if plugin.Type != pluginType {
			continue
		}

		version := plugin.Version
		if !plugin.Installed {
			fmt.Printf("  %s %s\n",
				ui.Accent(pluginListCell(plugin.Name, 34)),
				ui.Muted("not installed"),
			)
			continue
		}
		if version == "" {
			version = "unknown"
		}

		fmt.Printf("  %s %s\n",
			ui.Accent(pluginListCell(plugin.Name, 34)),
			version,
		)
	}
}

func printPluginList(cfg *config.Config) {
	fmt.Println()
	ui.Heading("Plugins")
	fmt.Printf("%s %s\n\n", ui.Muted("Path:"), ui.Muted(cfg.Plugins.PluginDir()))

	items := cfg.Plugins.List()
	printPluginListGroup("Parsers", "parser", items)
	fmt.Println()
	printPluginListGroup("Providers", "provider", items)
}

func pluginsListCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Infracost parser and provider plugins",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			printPluginList(cfg)
			return nil
		},
	}
}

func pluginsUpdateCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update all Infracost plugins to the latest version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Per-plugin download lines nest above this spinner, which resolves
			// to "Plugins up to date." — the only output when everything was
			// already current.
			return ui.RunWithSpinnerErr(cmd.Context(), "Updating plugins...", "Plugins up to date.", func(ctx context.Context) error {
				return cfg.Plugins.UpdatePlugins(ctx)
			})
		},
	}
}
