package cmds

import (
	"strings"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/spf13/cobra"
)

func pluginsInstallCmd(cfg *config.Config) *cobra.Command {
	var allowUnofficial bool

	cmd := &cobra.Command{
		Use:   "install <name>[@<version>]",
		Short: "Install a plugin from the Infracost plugin registry",
		Long: "Install a plugin from the Infracost plugin registry.\n\n" +
			"Provide the registry name (e.g. infracost/terraform) and optionally an\n" +
			"exact version with @<version>. Unofficial plugins run third-party native\n" +
			"code and require an interactive confirmation, or --allow-unofficial to\n" +
			"install non-interactively.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, wantVersion := parsePluginNameVersion(args[0])

			reg, err := registry.NewClient().Load(cmd.Context())
			if err != nil {
				return err
			}

			entry, err := reg.Resolve(name, plugins.RequiredAliases())
			if err != nil {
				return err
			}

			// The trust gate is invoked by the installer only when a download is
			// actually required — a no-op install never prompts.
			trust := func(e *registry.Entry) (bool, error) {
				return confirmUnofficialInstall(e, allowUnofficial, trustFail)
			}

			res, err := cfg.Plugins.InstallRegistryEntry(cmd.Context(), entry, wantVersion, trust)
			if err != nil {
				return err
			}

			printInstallResult(res)
			return nil
		},
	}

	cmd.Flags().BoolVar(&allowUnofficial, "allow-unofficial", false,
		"Install an unofficial plugin without the interactive confirmation prompt")
	return cmd
}

// parsePluginNameVersion splits "<name>[@<version>]" into its parts. Registry
// names never contain '@', so the first '@' unambiguously starts the version.
func parsePluginNameVersion(arg string) (name, version string) {
	name, version, _ = strings.Cut(arg, "@")
	return name, version
}

// printInstallResult renders the human-readable outcome of an install. There is
// no --json for install.
func printInstallResult(res *plugins.RegistryInstallResult) {
	if res == nil || res.Declined {
		return
	}

	if res.NoOp {
		if res.Version != "" {
			ui.Successf("%s %s is already installed.", ui.Accent(res.Entry.Name), res.Version)
		} else {
			ui.Successf("%s is already installed.", ui.Accent(res.Entry.Name))
		}
		ui.Hintf(2, "Run %s to update it.", ui.Code("infracost plugin update "+res.Entry.Name))
		return
	}

	for _, ci := range res.Installed {
		if res.Version != "" {
			ui.Successf("Installed %s %s (%s)", ui.Accent(res.Entry.Name), installVersionLabel(res), ci.Component.Type)
		} else {
			ui.Successf("Installed %s (%s)", ui.Accent(res.Entry.Name), ci.Component.Type)
		}
		ui.Hintf(4, "%s", ui.Muted(ci.Path))
	}

	// Report components left untouched (partial install completing the set).
	for _, ci := range res.Current {
		ui.Stepf("%s (%s) already up to date", ui.Accent(res.Entry.Name), ci.Component.Type)
	}
}

// installVersionLabel annotates the shared version with "(pinned)" when an
// explicit @<version> was requested.
func installVersionLabel(res *plugins.RegistryInstallResult) string {
	if res.Pinned {
		return res.Version + " (pinned)"
	}
	return res.Version
}
