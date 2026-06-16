package plugins

import (
	"os"
	"strings"
)

const (
	pluginTypeParser   = "parser"
	pluginTypeProvider = "provider"
)

// requiredPlugin describes a plugin the CLI installs and keeps up to date.
// Any other plugin dropped into the plugin directory is loaded too, but
// only the ones in this list are subject to auto-download and auto-update.
type requiredPlugin struct {
	Key  string
	Name string
	Type string
}

// requiredPlugins is the set of plugins the CLI manages automatically.
// Adding a binary to the plugin directory not in this list still loads
// it — the CLI just won't try to download or update it.
var requiredPlugins = []requiredPlugin{
	{Key: "terraform", Name: "infracost-plugin-terraform", Type: pluginTypeParser},
	{Key: "terragrunt", Name: "infracost-plugin-terragrunt", Type: pluginTypeParser},
	{Key: "cloudformation", Name: "infracost-plugin-cloudformation", Type: pluginTypeParser},
	{Key: "ciscostacks", Name: "infracost-plugin-ciscostacks", Type: pluginTypeParser},
	{Key: "aws", Name: "infracost-plugin-aws", Type: pluginTypeProvider},
	{Key: "google", Name: "infracost-plugin-google", Type: pluginTypeProvider},
	{Key: "azure", Name: "infracost-plugin-azure", Type: pluginTypeProvider},
}

// requiredPluginVersion returns the user-pinned version for the required
// plugin with the given key, or "" if no pin is set.
func requiredPluginVersion(key string) string {
	return os.Getenv("INFRACOST_CLI_PLUGIN_" + strings.ToUpper(key) + "_VERSION")
}
