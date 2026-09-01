package cmds

import (
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
	cmd.AddCommand(pluginsInstallCmd(cfg))
	cmd.AddCommand(pluginsUninstallCmd(cfg))
	cmd.AddCommand(pluginsUpdateCmd(cfg))
	cmd.AddCommand(pluginsValidateCmd(cfg))
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

