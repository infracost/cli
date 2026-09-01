package cmds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/spf13/cobra"
)

// errPluginValidationFailed is returned when at least one binary fails a
// check, so the process exits non-zero. The checklist is already printed by
// the time this is returned, so the message stays terse.
var errPluginValidationFailed = errors.New("plugin validation failed")

// validateRegistryLoad loads the plugin registry for `--release <registry-name>`.
// It is a seam so tests can drive resolution without a network fetch; production
// code never reassigns it.
var validateRegistryLoad = func(ctx context.Context) (*registry.Registry, error) {
	return registry.NewClient().Load(ctx)
}

func pluginsValidateCmd(cfg *config.Config) *cobra.Command {
	var asJSON bool
	var release string
	var allPlatforms bool
	var allowUnofficial bool

	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate a plugin binary against the CLI's plugin expectations",
		Long: `Validate a plugin binary against the CLI's plugin expectations.

With a path argument, validates that single binary. With no argument, validates
every plugin binary in the plugin directory. Each binary is checked for an
executable file, a successful go-plugin handshake, well-formed metadata, and its
type-specific RPC surface.

With --release <manifest-entry.json | registry-name>[@<version>], validates a
published release instead: the manifest entry's schema, its resolved shared
version, every declared component/platform artifact + .sha256 reachability, and
a download-verify + execute of the current-platform components (every platform
with --all-platforms). Executing an unofficial plugin requires the same
confirmation, or --allow-unofficial, as installing one.

Exits non-zero if any check fails.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if release != "" {
				if len(args) > 0 {
					return fmt.Errorf("cannot combine a path argument with --release")
				}
				return runValidateRelease(cmd, release, allPlatforms, allowUnofficial, asJSON)
			}

			var results []plugins.ValidationResult

			if len(args) == 1 {
				// Explicit path: still collision-check against whatever is in
				// the plugin directory, since that's where discovery loads from.
				results = []plugins.ValidationResult{plugins.ValidateBinary(args[0], cfg.Plugins.PluginDir())}
			} else {
				dir := cfg.Plugins.PluginDir()
				var err error
				results, err = plugins.ValidateDir(dir)
				if err != nil {
					return fmt.Errorf("failed to read plugin directory %s: %w", dir, err)
				}
				if len(results) == 0 {
					if asJSON {
						return printPluginValidateJSON(results)
					}
					fmt.Printf("No plugins found in %s\n", dir)
					return nil
				}
			}

			if asJSON {
				return printPluginValidateJSON(results)
			}

			return printPluginValidateHuman(results)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the validation checklist as JSON")
	cmd.Flags().StringVar(&release, "release", "", "Validate a published release: a manifest-entry.json file or a registry name[@version]")
	cmd.Flags().BoolVar(&allPlatforms, "all-platforms", false, "With --release, download-verify every declared platform's artifact, not just the current one")
	cmd.Flags().BoolVar(&allowUnofficial, "allow-unofficial", false, "With --release, execute an unofficial plugin's binary without the interactive confirmation prompt")
	return cmd
}

// runValidateRelease drives `plugin validate --release`. The target is a local
// manifest-entry.json file (for pre-PR checks) or a registry name[@version];
// an unknown name yields install's "not found in registry" error shape.
func runValidateRelease(cmd *cobra.Command, target string, allPlatforms, allowUnofficial, asJSON bool) error {
	entry, source, wantVersion, err := resolveReleaseEntry(cmd.Context(), target)
	if err != nil {
		return err
	}

	// trustSkip so a non-interactive run without --allow-unofficial warns and
	// runs the network checks only; the resulting incomplete checklist is what
	// makes the command exit non-zero, not a trust-callback error.
	trust := func(e *registry.Entry) (bool, error) {
		return confirmUnofficialInstall(e, allowUnofficial, trustSkip)
	}

	res, err := plugins.ValidateReleaseEntry(cmd.Context(), entry,
		plugins.ReleaseOptions{Version: wantVersion, AllPlatforms: allPlatforms}, trust)
	if err != nil {
		return err
	}
	res.Source = source

	if asJSON {
		return printReleaseValidateJSON(res)
	}
	return printReleaseValidateHuman(res)
}

// resolveReleaseEntry turns a --release target into a validated entry, reporting
// whether it came from a local file or the registry and the pinned version (if
// any). A path is preferred first (so a path containing '@' still resolves),
// then a name[@version] split is tried as a file, then as a registry name.
func resolveReleaseEntry(ctx context.Context, target string) (entry *registry.Entry, source, wantVersion string, err error) {
	if regularFileExists(target) {
		return parseReleaseEntryFile(target, "")
	}

	name, ver := parsePluginNameVersion(target)
	if regularFileExists(name) {
		return parseReleaseEntryFile(name, ver)
	}

	reg, err := validateRegistryLoad(ctx)
	if err != nil {
		return nil, "", "", err
	}
	e, err := reg.Resolve(name, plugins.RequiredAliases())
	if err != nil {
		return nil, "", "", err
	}
	return e, "registry", ver, nil
}

func parseReleaseEntryFile(path, ver string) (*registry.Entry, string, string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a user-supplied manifest file to validate
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to read manifest entry %s: %w", path, err)
	}
	e, err := registry.ParseEntry(data)
	if err != nil {
		return nil, "", "", err
	}
	return e, "file", ver, nil
}

// regularFileExists reports whether path names an existing regular file. It is
// stricter than ci.go's fileExists (which accepts directories) because a
// --release target must be a manifest file, not a directory.
func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// releaseValidateJSON is the stable --release --json envelope.
type releaseValidateJSON struct {
	OK     bool                             `json:"ok"`
	Result *plugins.ReleaseValidationResult `json:"result"`
}

func printReleaseValidateJSON(res *plugins.ReleaseValidationResult) error {
	out := releaseValidateJSON{OK: res.OK(), Result: res}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if !out.OK {
		return errPluginValidationFailed
	}
	return nil
}

func printReleaseValidateHuman(res *plugins.ReleaseValidationResult) error {
	fmt.Printf("%s %s %s\n", ui.Bold("Validating release"), ui.Accent(res.Name), ui.Muted(releaseVersionText(res.Version)))
	for _, c := range res.Checks {
		fmt.Println(renderCheckLine(c))
	}
	for _, comp := range res.Components {
		fmt.Printf("  %s %s\n", ui.Bold(comp.Type), ui.Muted(comp.BinaryName))
		for _, c := range comp.Checks {
			// renderCheckLine already indents by two spaces; component checks
			// nest one level deeper under their component header.
			fmt.Println("  " + renderCheckLine(c))
		}
	}

	switch {
	case res.OK():
		ui.Success("Passed")
	case res.Incomplete:
		ui.Fail("Incomplete — execution checks were skipped (pass --allow-unofficial to run them)")
	default:
		ui.Fail("Failed")
	}

	if !res.OK() {
		return errPluginValidationFailed
	}
	return nil
}

// releaseVersionText renders the resolved version for the human header.
func releaseVersionText(v string) string {
	if v == "" {
		return "(version unresolved)"
	}
	return v
}

// pluginValidateJSON is the stable --json envelope: an overall ok plus one
// checklist per validated binary.
type pluginValidateJSON struct {
	OK      bool                       `json:"ok"`
	Results []plugins.ValidationResult `json:"results"`
}

func printPluginValidateJSON(results []plugins.ValidationResult) error {
	out := pluginValidateJSON{OK: allValidationsOK(results), Results: results}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if !out.OK {
		return errPluginValidationFailed
	}
	return nil
}

func printPluginValidateHuman(results []plugins.ValidationResult) error {
	for i, res := range results {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s %s\n", ui.Bold("Validating"), ui.Accent(res.Path))
		for _, c := range res.Checks {
			line := renderCheckLine(c)
			fmt.Println(line)
		}
		if res.OK() {
			ui.Success("Passed")
		} else {
			ui.Fail("Failed")
		}
	}

	if !allValidationsOK(results) {
		return errPluginValidationFailed
	}
	return nil
}

func renderCheckLine(c plugins.CheckResult) string {
	var symbol string
	switch c.Status {
	case plugins.CheckPass:
		symbol = ui.Positive("✔")
	case plugins.CheckFail:
		symbol = ui.Danger("✗")
	case plugins.CheckWarn:
		symbol = ui.Caution("!")
	case plugins.CheckSkip:
		symbol = ui.Muted("–")
	default:
		symbol = " "
	}

	line := fmt.Sprintf("  %s  %s", symbol, c.Name)
	if c.Detail != "" {
		line += ui.Muted(" — " + c.Detail)
	}
	return line
}

func allValidationsOK(results []plugins.ValidationResult) bool {
	for _, r := range results {
		if !r.OK() {
			return false
		}
	}
	return true
}
