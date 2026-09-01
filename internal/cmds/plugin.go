package cmds

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	cmd.AddCommand(pluginsSearchCmd(cfg))
	cmd.AddCommand(pluginsInfoCmd(cfg))
	cmd.AddCommand(pluginsInstallCmd(cfg))
	cmd.AddCommand(pluginsUninstallCmd(cfg))
	cmd.AddCommand(pluginsUpdateCmd(cfg))
	cmd.AddCommand(pluginsValidateCmd(cfg))
	return cmd
}

func pluginListCell(value string, width int) string {
	return fmt.Sprintf("%-*s", width, value)
}

// pluginListMarkers builds the trailing provenance annotations for an installed
// row (author, unofficial, unmanaged). The pinned marker is folded into the
// version cell instead, so it is not returned here.
func pluginListMarkers(plugin plugins.ListItem) string {
	var parts []string
	switch plugin.Source {
	case plugins.SourceRegistry:
		if plugin.Author != "" {
			parts = append(parts, ui.Muted("by "+plugin.Author))
		}
		if !plugin.Official {
			parts = append(parts, ui.Danger("unofficial"))
		}
	case plugins.SourceUnmanaged:
		parts = append(parts, ui.Muted("unmanaged"))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func printPluginListGroup(title string, items []plugins.ListItem, match func(plugins.ListItem) bool) {
	fmt.Println(ui.Bold(title + ":"))
	for _, plugin := range items {
		if !match(plugin) {
			continue
		}

		if !plugin.Installed {
			fmt.Printf("  %s %s\n",
				ui.Accent(pluginListCell(plugin.Name, 34)),
				ui.Muted("not installed"),
			)
			continue
		}

		version := plugin.Version
		if version == "" {
			version = "unknown"
		}
		if plugin.Pinned {
			version += " (pinned)"
		}

		fmt.Printf("  %s %s%s\n",
			ui.Accent(pluginListCell(plugin.Name, 34)),
			version,
			pluginListMarkers(plugin),
		)
	}
}

// isKnownPluginType reports whether a type maps to one of the fixed groups.
func isKnownPluginType(t string) bool {
	return t == "parser" || t == "provider"
}

// hasUnknownTypePlugin reports whether any installed item has an
// undeterminable type, so the Unknown group is only shown when it has content.
func hasUnknownTypePlugin(items []plugins.ListItem) bool {
	for _, plugin := range items {
		if plugin.Installed && !isKnownPluginType(plugin.Type) {
			return true
		}
	}
	return false
}

func printPluginList(cfg *config.Config) {
	fmt.Println()
	ui.Heading("Plugins")
	fmt.Printf("%s %s\n\n", ui.Muted("Path:"), ui.Muted(cfg.Plugins.PluginDir()))

	items := cfg.Plugins.List()
	printPluginListGroup("Parsers", items, func(p plugins.ListItem) bool { return p.Type == "parser" })
	fmt.Println()
	printPluginListGroup("Providers", items, func(p plugins.ListItem) bool { return p.Type == "provider" })

	// Only show the Unknown group when there is an installed plugin whose type
	// could not be determined, so stock output stays unchanged.
	if hasUnknownTypePlugin(items) {
		fmt.Println()
		printPluginListGroup("Unknown", items, func(p plugins.ListItem) bool {
			return p.Installed && !isKnownPluginType(p.Type)
		})
	}
}

func printPluginListJSON(cfg *config.Config) error {
	items := cfg.Plugins.List()
	if items == nil {
		items = []plugins.ListItem{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func pluginsListCmd(cfg *config.Config) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Infracost parser and provider plugins",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if asJSON {
				return printPluginListJSON(cfg)
			}
			printPluginList(cfg)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the plugin list as JSON, including provenance fields")
	return cmd
}

