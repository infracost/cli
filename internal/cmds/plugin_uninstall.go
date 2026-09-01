package cmds

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/spf13/cobra"
)

// Test seams for the required-plugin uninstall confirmation, mirroring the
// trust-gate seams in plugin_trust.go. Unit tests can't drive a real TTY, so
// interactivity and the prompt are indirected through package vars the tests
// override. Production code never reassigns them.
var (
	uninstallIsInteractive = ui.IsInteractive
	uninstallConfirm       = promptUninstallConfirm
	// uninstallRegistryLoad loads the registry for the best-effort "exists in
	// the registry but is not installed" message. It is a seam so tests can
	// resolve that path without a network fetch or touching the global cache.
	uninstallRegistryLoad = func(ctx context.Context) (*registry.Registry, error) {
		return registry.NewClient().Load(ctx)
	}
)

func pluginsUninstallCmd(cfg *config.Config) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "uninstall <name>",
		Short: "Remove an installed plugin",
		Long: "Remove an installed plugin's component binaries, sidecars, and\n" +
			"provenance record from the plugin directory.\n\n" +
			"Provide the registry name (e.g. infracost/terraform), a required-plugin\n" +
			"key, or a component binary name. Naming one component of a multi-component\n" +
			"plugin removes the whole entry. Uninstalling a built-in required plugin\n" +
			"prompts for confirmation, since the CLI will re-download it automatically\n" +
			"when it is next needed; --yes skips the prompt.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginUninstall(cmd, cfg, args[0], yes)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false,
		"Skip the confirmation prompt when uninstalling an auto-managed required plugin")
	return cmd
}

func runPluginUninstall(cmd *cobra.Command, cfg *config.Config, input string, yes bool) error {
	if cfg.Plugins.Dir != "" {
		return fmt.Errorf("plugin uninstalls are disabled while INFRACOST_CLI_PLUGIN_DIR is set (%s) — plugins are loaded from that directory; unset it to manage plugins automatically", cfg.Plugins.Dir)
	}

	target, err := cfg.Plugins.ResolveUninstall(input)
	if err != nil {
		if errors.Is(err, plugins.ErrPluginNotInstalled) {
			return notInstalledError(cmd.Context(), input)
		}
		return err
	}

	// A resolved-but-not-installed target is a known required plugin the CLI
	// would just re-download — there is nothing to remove.
	if !target.Actionable() {
		return fmt.Errorf("%s is not installed", target.Name)
	}

	if target.NamedComponent != "" {
		ui.Stepf("%s is one component of %s — uninstalling removes all of its components.",
			ui.Accent(target.NamedComponent), ui.Accent(target.Name))
	}

	if target.Required {
		proceed, err := confirmRequiredUninstall(target, yes)
		if err != nil {
			return err
		}
		if !proceed {
			ui.Stepf("Left %s untouched.", ui.Accent(target.Name))
			return nil
		}
	}

	res, err := cfg.Plugins.Uninstall(target)
	if err != nil {
		return err
	}

	printUninstallResult(res)
	return nil
}

// confirmRequiredUninstall gates the removal of an auto-managed required
// plugin. --yes skips the prompt. On a non-interactive terminal without --yes
// it is a hard error naming the flag; on a TTY it shows the auto-reinstall
// warning and requires an explicit Yes (default No). A Ctrl-C/Esc decline is
// not an error.
func confirmRequiredUninstall(target *plugins.UninstallTarget, yes bool) (proceed bool, err error) {
	if yes {
		return true, nil
	}

	if !uninstallIsInteractive() {
		return false, fmt.Errorf("%s is a built-in plugin the CLI re-downloads automatically — pass --yes to uninstall it in a non-interactive terminal", target.Name)
	}

	renderRequiredUninstallWarning(target)
	confirmed, err := uninstallConfirm(target)
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return confirmed, nil
}

// renderRequiredUninstallWarning prints the boxed notice that precedes the
// confirmation for a required plugin: the uninstall is allowed, but the CLI
// re-downloads it automatically the next time a command needs it.
func renderRequiredUninstallWarning(target *plugins.UninstallTarget) {
	var b strings.Builder
	b.WriteString(ui.Bold(ui.Caution("Built-in plugin — managed automatically by Infracost")))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "%s is part of the set Infracost installs and keeps up to date.\n", ui.Accent(target.Name))
	b.WriteString("You can uninstall it, but the CLI will re-download it automatically\n")
	b.WriteString("the next time a command needs it (parsers up front, providers on demand).")

	fmt.Println()
	fmt.Print(ui.Box(b.String()))
}

// promptUninstallConfirm shows the TTY confirmation for a required plugin,
// defaulting to No, following the huh.NewConfirm + ui.BrandTheme convention.
func promptUninstallConfirm(target *plugins.UninstallTarget) (bool, error) {
	// confirm starts false, so the No button is the default selection.
	var confirm bool
	err := huh.NewConfirm().
		Title(fmt.Sprintf("Uninstall %s?", target.Name)).
		Description("The CLI will re-download it automatically when it is next needed.").
		Affirmative("Yes, uninstall").
		Negative("No, keep it").
		Value(&confirm).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		return false, err
	}
	return confirm, nil
}

// notInstalledError builds the error for a name that resolves to nothing
// installed. It makes a best-effort registry lookup so a name that exists in
// the registry but isn't installed says so explicitly; if the registry can't
// be reached the message degrades to a plain "not installed".
func notInstalledError(ctx context.Context, input string) error {
	reg, err := uninstallRegistryLoad(ctx)
	if err == nil && reg != nil {
		if e, rerr := reg.Resolve(input, plugins.RequiredAliases()); rerr == nil {
			return fmt.Errorf("%s exists in the registry but is not installed — run `infracost plugin install %s` to install it", e.Name, e.Name)
		}
	}
	return fmt.Errorf("plugin %q is not installed", input)
}

// printUninstallResult renders the human-readable outcome of an uninstall.
func printUninstallResult(res *plugins.UninstallResult) {
	name := res.Target.Name

	for _, comp := range res.Removed {
		if comp.Type != "" {
			ui.Successf("Removed %s (%s)", ui.Accent(name), comp.Type)
		} else {
			ui.Successf("Removed %s", ui.Accent(name))
		}
		ui.Hintf(4, "%s", ui.Muted(comp.Path))
	}

	for _, comp := range res.Missing {
		if comp.Type != "" {
			ui.Stepf("%s (%s) was already removed", ui.Accent(name), comp.Type)
		} else {
			ui.Stepf("%s was already removed", ui.Accent(name))
		}
	}

	if len(res.Removed) == 0 && res.RecordRemoved {
		ui.Successf("Cleaned up %s — its binaries were already gone.", ui.Accent(name))
	}

	if res.Target.Required {
		ui.Hintf(2, "%s", ui.Muted("Infracost will re-download this plugin automatically when it is next needed."))
	}
}
