package cmds

import (
	"context"
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/spf13/cobra"
)

// updateRegistryLoad loads the plugin registry. It is a seam so tests can drive
// the resolution/removed-from-registry paths without a network fetch. Production
// code never reassigns it.
var updateRegistryLoad = func(ctx context.Context) (*registry.Registry, error) {
	return registry.NewClient().Load(ctx)
}

func pluginsUpdateCmd(cfg *config.Config) *cobra.Command {
	var allowUnofficial bool

	cmd := &cobra.Command{
		Use:   "update [<name>]",
		Short: "Update Infracost plugins to the latest version",
		Long: "Update Infracost plugins to the latest version.\n\n" +
			"With no argument, every built-in plugin and every plugin installed from\n" +
			"the registry is updated to its latest version; version-pinned and local\n" +
			"dev-build plugins are left as they are, and hand-copied binaries are\n" +
			"reported as unmanaged.\n\n" +
			"Provide a name (a registry name, a required-plugin key, or a component\n" +
			"binary name) to update just that plugin. Naming one component of a\n" +
			"multi-component plugin updates the whole entry, and updating a\n" +
			"version-pinned plugin explicitly clears its pin. Unofficial plugins run\n" +
			"third-party native code and require an interactive confirmation, or\n" +
			"--allow-unofficial to update non-interactively.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return runPluginUpdateAll(cmd, cfg, allowUnofficial)
			}
			return runPluginUpdateOne(cmd, cfg, args[0], allowUnofficial)
		},
	}

	cmd.Flags().BoolVar(&allowUnofficial, "allow-unofficial", false,
		"Update an unofficial plugin without the interactive confirmation prompt")
	return cmd
}

// runPluginUpdateAll updates the built-in required set plus every
// provenance-recorded registry entry. One entry's failure never aborts the rest;
// the collected outcomes are reported and the command exits non-zero if any
// failed.
func runPluginUpdateAll(cmd *cobra.Command, cfg *config.Config, allowUnofficial bool) error {
	if cfg.Plugins.Dir != "" {
		return pluginDevOverrideRefusal(cfg.Plugins.Dir)
	}

	var res *plugins.UpdateResult
	// The outer spinner gives feedback during the required-set version checks
	// (which download nothing when everything is current). Per-plugin download
	// lines nest above it; an empty done-title prints nothing on success so the
	// summary below is the only closing output.
	err := ui.RunWithSpinnerErr(cmd.Context(), "Updating plugins...", "", func(ctx context.Context) error {
		// Only fetch the registry when there is at least one registry-installed
		// entry to check — with just the built-in set present this path behaves
		// exactly as it did before registry installs existed.
		var reg *registry.Registry
		var regErr error
		if cfg.Plugins.HasRegistryInstalls() {
			reg, regErr = updateRegistryLoad(ctx)
		}

		trust := spinnerPausedTrust(allowUnofficial, trustSkip)
		var e error
		res, e = cfg.Plugins.UpdateAll(ctx, reg, regErr, trust)
		return e
	})
	if err != nil {
		return err
	}

	reportUpdateAll(res)
	if res.Failed() {
		return fmt.Errorf("one or more plugins could not be updated")
	}
	return nil
}

// runPluginUpdateOne updates a single named plugin. A required-plugin name is
// resolved locally and updated via the managed required-set path without a
// registry fetch; any other name is resolved against the registry.
func runPluginUpdateOne(cmd *cobra.Command, cfg *config.Config, input string, allowUnofficial bool) error {
	if cfg.Plugins.Dir != "" {
		return pluginDevOverrideRefusal(cfg.Plugins.Dir)
	}

	// Built-in required plugins resolve without touching the registry.
	if displayName, ok := plugins.RequiredDisplayNameFor(input); ok {
		return updateRequiredByName(cmd, cfg, displayName)
	}

	reg, err := updateRegistryLoad(cmd.Context())
	if err != nil {
		return err
	}

	entry, err := reg.Resolve(input, plugins.RequiredAliases())
	if err != nil {
		return fmt.Errorf("%w — run `infracost plugin list` to see installed plugins", err)
	}

	// Defensive: a name that resolves through the registry to a built-in entry
	// is still handled by the managed required-set path.
	if plugins.IsRequiredName(entry.Name) {
		return updateRequiredByName(cmd, cfg, entry.Name)
	}

	if comp := namedComponent(entry, input); comp != "" {
		ui.Stepf("%s is one component of %s — updating updates all of its components.",
			ui.Accent(comp), ui.Accent(entry.Name))
	}

	trust := spinnerPausedTrust(allowUnofficial, trustFail)
	res, err := cfg.Plugins.UpdateEntry(cmd.Context(), entry, trust)
	if err != nil {
		return err
	}

	reportUpdateOne(res)
	return nil
}

// updateRequiredByName updates one built-in required plugin (all components
// sharing its display name) through the managed required-set path.
func updateRequiredByName(cmd *cobra.Command, cfg *config.Config, displayName string) error {
	err := ui.RunWithSpinnerErr(cmd.Context(),
		fmt.Sprintf("Updating %s...", displayName), "",
		func(ctx context.Context) error {
			return cfg.Plugins.UpdateRequiredEntry(ctx, displayName)
		})
	if err != nil {
		return err
	}
	ui.Successf("%s is up to date.", ui.Accent(displayName))
	return nil
}

// spinnerPausedTrust adapts the unofficial-plugin trust gate into a
// plugins.TrustFunc, pausing any active spinner while the confirmation prompt
// (or its non-interactive warning) runs so the two don't paint over each other.
func spinnerPausedTrust(allowUnofficial bool, mode unofficialTrustMode) plugins.TrustFunc {
	return func(e *registry.Entry) (bool, error) {
		var proceed bool
		err := ui.WithSpinnerPaused(func() error {
			var innerErr error
			proceed, innerErr = confirmUnofficialInstall(e, allowUnofficial, mode)
			return innerErr
		})
		return proceed, err
	}
}

// namedComponent returns the component binary name when input named a specific
// component of the entry (tolerating a trailing .exe), or "" when input named
// the entry itself or an alias. It drives the "one component of …" notice.
func namedComponent(e *registry.Entry, input string) string {
	base := strings.TrimSuffix(input, ".exe")
	for _, c := range e.Components {
		if c.BinaryName == input || c.BinaryName == base {
			return c.BinaryName
		}
	}
	return ""
}

// pluginDevOverrideRefusal is the shared refusal returned when a management
// command runs while INFRACOST_CLI_PLUGIN_DIR is set. Its shape matches the
// install/uninstall refusals.
func pluginDevOverrideRefusal(dir string) error {
	return fmt.Errorf("plugin updates are disabled while INFRACOST_CLI_PLUGIN_DIR is set (%s) — plugins are loaded from that directory; unset it to manage plugins automatically", dir)
}

// reportUpdateAll renders the outcome of an update-all run. Successful required
// updates produce no entry — their nested "Downloaded" lines are the feedback —
// so a fully-quiet run (nothing updated, skipped, failed, or unmanaged) closes
// with "Plugins up to date.", preserving the prior UX.
func reportUpdateAll(res *plugins.UpdateResult) {
	printed := false
	for _, e := range res.Entries {
		switch e.Status {
		case plugins.UpdateStatusUpdated:
			printed = true
			ui.Successf("Updated %s %s → %s", ui.Accent(e.Name), versionLabel(e.FromVersion), versionLabel(e.ToVersion))
		case plugins.UpdateStatusUpToDate:
			// Already current — quiet, like the required-set successes.
		case plugins.UpdateStatusSkippedPinned:
			printed = true
			ui.Stepf("%s is pinned to %s — skipped. Run %s to update and unpin it.",
				ui.Accent(e.Name), versionLabel(e.FromVersion), ui.Code("infracost plugin update "+e.Name))
		case plugins.UpdateStatusSkippedDev:
			printed = true
			ui.Stepf("%s is a local dev build — skipped.", ui.Accent(e.Name))
		case plugins.UpdateStatusSkippedRemoved:
			printed = true
			ui.Warnf("%s is no longer in the registry; cannot check for updates — skipped (it is still installed).",
				ui.Accent(e.Name))
		case plugins.UpdateStatusSkippedMissing:
			printed = true
			ui.Stepf("%s has a missing component (%s) — skipped. Run %s to reinstall it.",
				ui.Accent(e.Name), e.Detail, ui.Code("infracost plugin update "+e.Name))
		case plugins.UpdateStatusSkippedUnofficial:
			// confirmUnofficialInstall already warned (trustSkip) or the user
			// declined interactively — nothing more to add.
		case plugins.UpdateStatusFailed:
			printed = true
			ui.Failf("Failed to update %s: %v", ui.Accent(e.Name), e.Err)
		}
	}

	for _, name := range res.Unmanaged {
		printed = true
		ui.Stepf("%s — unmanaged, skipped (not installed from the registry).", ui.Accent(name))
	}

	if !printed {
		ui.Success("Plugins up to date.")
	}
}

// reportUpdateOne renders the outcome of an explicit single-plugin update.
// Failures are surfaced by the caller as a returned error, so they are not
// printed here.
func reportUpdateOne(res *plugins.UpdateResult) {
	if res == nil || len(res.Entries) == 0 {
		return
	}
	e := res.Entries[0]
	switch e.Status {
	case plugins.UpdateStatusUpdated:
		ui.Successf("Updated %s %s → %s", ui.Accent(e.Name), versionLabel(e.FromVersion), versionLabel(e.ToVersion))
		for _, ci := range e.Components {
			ui.Hintf(4, "%s", ui.Muted(ci.Path))
		}
	case plugins.UpdateStatusUpToDate:
		ui.Successf("%s is already at the latest version (%s).", ui.Accent(e.Name), versionLabel(e.ToVersion))
	case plugins.UpdateStatusSkippedDev:
		ui.Stepf("%s is a local dev build — left untouched.", ui.Accent(e.Name))
	case plugins.UpdateStatusSkippedUnofficial:
		// Declined interactively — a clean no-op.
	}
}

// versionLabel renders a possibly-empty version for display.
func versionLabel(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
