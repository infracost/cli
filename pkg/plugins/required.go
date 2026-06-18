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
	// Key is the short identifier used in env vars and CLI flags.
	Key string
	// Name is the binary filename on disk (without the .exe suffix).
	Name string
	// DisplayName is the name the plugin reports via GetPluginInfo —
	// used in user-facing output when the plugin isn't installed yet so
	// we can't ask it directly.
	DisplayName string
	Type        string
}

// requiredPlugins is the set of plugins the CLI manages automatically.
// Adding a binary to the plugin directory not in this list still loads
// it — the CLI just won't try to download or update it.
var requiredPlugins = []requiredPlugin{
	{Key: "terraform", Name: "infracost-plugin-terraform", DisplayName: "infracost/terraform", Type: pluginTypeParser},
	{Key: "terragrunt", Name: "infracost-plugin-terragrunt", DisplayName: "infracost/terragrunt", Type: pluginTypeParser},
	{Key: "cloudformation", Name: "infracost-plugin-cloudformation", DisplayName: "infracost/cloudformation", Type: pluginTypeParser},
	{Key: "ciscostacks", Name: "infracost-plugin-ciscostacks", DisplayName: "infracost/ciscostacks", Type: pluginTypeParser},
	{Key: "terraform-plan", Name: "infracost-plugin-terraform-plan", DisplayName: "infracost/terraform-plan", Type: pluginTypeParser},
	{Key: "aws", Name: "infracost-plugin-aws", DisplayName: "infracost/aws", Type: pluginTypeProvider},
	{Key: "google", Name: "infracost-plugin-google", DisplayName: "infracost/google", Type: pluginTypeProvider},
	{Key: "azure", Name: "infracost-plugin-azure", DisplayName: "infracost/azure", Type: pluginTypeProvider},
}

// requiredPluginVersion returns the user-pinned version for the required
// plugin with the given key, or "" if no pin is set.
func requiredPluginVersion(key string) string {
	return os.Getenv("INFRACOST_CLI_PLUGIN_" + strings.ToUpper(key) + "_VERSION")
}
