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
	// Name is the binary filename on disk (without the .exe suffix) and the
	// asset name fetched from the release host.
	Name string
	// LegacyName is the pre-rename binary/asset name (infracost-plugin-<key>).
	// A binary with this name is removed from the plugin directory on install
	// so an orphaned old binary doesn't keep getting discovered alongside its
	// renamed replacement — both report the same (name, type) and would trip
	// the duplicate-plugin check. Empty for plugins that never shipped under
	// the old name.
	LegacyName string
	// DisplayName is the name the plugin reports via GetPluginInfo —
	// used in user-facing output when the plugin isn't installed yet so
	// we can't ask it directly.
	DisplayName string
	Type        string
}

// requiredPlugins is the set of plugins the CLI manages automatically.
// Adding a binary to the plugin directory not in this list still loads
// it — the CLI just won't try to download or update it.
//
// Parser and provider assets are namespaced by type (infracost-parser-<key> /
// infracost-provider-<key>) so a parser and a provider can share a key (e.g.
// kubernetes) without colliding on the asset name or on-disk binary.
var requiredPlugins = []requiredPlugin{
	{Key: "terraform", Name: "infracost-parser-terraform", LegacyName: "infracost-plugin-terraform", DisplayName: "infracost/terraform", Type: pluginTypeParser},
	{Key: "terragrunt", Name: "infracost-parser-terragrunt", LegacyName: "infracost-plugin-terragrunt", DisplayName: "infracost/terragrunt", Type: pluginTypeParser},
	{Key: "cloudformation", Name: "infracost-parser-cloudformation", LegacyName: "infracost-plugin-cloudformation", DisplayName: "infracost/cloudformation", Type: pluginTypeParser},
	{Key: "ciscostacks", Name: "infracost-parser-ciscostacks", LegacyName: "infracost-plugin-ciscostacks", DisplayName: "infracost/ciscostacks", Type: pluginTypeParser},
	{Key: "terraform-plan", Name: "infracost-parser-terraform-plan", LegacyName: "infracost-plugin-terraform-plan", DisplayName: "infracost/terraform-plan", Type: pluginTypeParser},
	{Key: "aws", Name: "infracost-provider-aws", LegacyName: "infracost-plugin-aws", DisplayName: "infracost/aws", Type: pluginTypeProvider},
	{Key: "google", Name: "infracost-provider-google", LegacyName: "infracost-plugin-google", DisplayName: "infracost/google", Type: pluginTypeProvider},
	{Key: "azure", Name: "infracost-provider-azure", LegacyName: "infracost-plugin-azure", DisplayName: "infracost/azure", Type: pluginTypeProvider},
	// The Kubernetes parser and provider share the "kubernetes" key (and so the
	// same version pin env var) — namespacing by type keeps their asset names
	// and on-disk binaries distinct.
	{Key: "kubernetes", Name: "infracost-parser-kubernetes", DisplayName: "infracost/kubernetes", Type: pluginTypeParser},
	{Key: "kubernetes", Name: "infracost-provider-kubernetes", DisplayName: "infracost/kubernetes", Type: pluginTypeProvider},
}

// requiredPluginVersion returns the user-pinned version for the required
// plugin with the given key, or "" if no pin is set.
func requiredPluginVersion(key string) string {
	return os.Getenv("INFRACOST_CLI_PLUGIN_" + strings.ToUpper(key) + "_VERSION")
}
